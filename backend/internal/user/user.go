// Package user serves the profile, data-processing consent and aggregated statistics.
package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"finadvisor/internal/infrastructure/httpx"
)

// ConsentVersion identifies the text of the consent the user accepted.
const (
	ConsentVersion      = "2026-11"
	StatsConsentVersion = "2026-11"
)

// Prefs are UI settings stored as JSON; unknown keys are rejected at the API.
type Prefs struct {
	BigText      *bool    `json:"big_text,omitempty"`
	VoiceAnswers *bool    `json:"voice_answers,omitempty"`
	VoiceSpeed   *float64 `json:"voice_speed,omitempty"`
}

type Business struct {
	Trade   string `json:"trade"`
	Sector  string `json:"sector"`
	Market  string `json:"market"`
	Status  string `json:"status"`
	Regime  string `json:"regime"`
	Reserve int64  `json:"reserve"`
}

type Profile struct {
	ID             string     `json:"id"`
	Phone          *string    `json:"phone"`
	Email          *string    `json:"email"`
	Role           string     `json:"role"`
	Lang           string     `json:"lang"`
	Prefs          Prefs      `json:"prefs"`
	ConsentAt      *time.Time `json:"consent_at"`
	ConsentVersion *string    `json:"consent_version"`
	StatsConsentAt *time.Time `json:"stats_consent_at"`
	Business       *Business  `json:"business"`
	CreatedAt      time.Time  `json:"created_at"`
}

type RecentPlan struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

type Stats struct {
	Email                *string      `json:"email"`
	TotalPlans           int          `json:"total_plans"`
	TotalCalculations    int          `json:"total_calculations"`
	LargeLoansCalculated int          `json:"large_loans_calculated"` // loans > 1 000 000 UZS
	RecentPlans          []RecentPlan `json:"recent_plans"`
}

var ErrNotFound = errors.New("user not found")

type Repo struct{ db *sql.DB }

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

func (r *Repo) Profile(ctx context.Context, id string) (Profile, error) {
	var p Profile
	var prefs []byte
	var trade, sector, market, status, regime sql.NullString
	var reserve sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT u.id, u.phone, u.email, u.role, u.lang, u.prefs, u.consent_at, u.consent_version,
		        u.stats_consent_at, u.created_at,
		        b.trade, b.sector, b.market, b.status, b.regime, b.reserve
		   FROM users u LEFT JOIN businesses b ON b.user_id = u.id
		  WHERE u.id = $1`, id).
		Scan(&p.ID, &p.Phone, &p.Email, &p.Role, &p.Lang, &prefs, &p.ConsentAt, &p.ConsentVersion,
			&p.StatsConsentAt, &p.CreatedAt, &trade, &sector, &market, &status, &regime, &reserve)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if len(prefs) > 0 {
		_ = json.Unmarshal(prefs, &p.Prefs)
	}
	if sector.Valid {
		p.Business = &Business{trade.String, sector.String, market.String, status.String, regime.String, reserve.Int64}
	}
	return p, nil
}

// PutBusiness creates or replaces the business profile; a status change is recorded in
// business_status_history (the Chamber report counts switches to official status).
func (r *Repo) PutBusiness(ctx context.Context, id string, b Business) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status FROM businesses WHERE user_id = $1 FOR UPDATE`, id).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO businesses (user_id, trade, sector, market, status, regime, reserve, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		 ON CONFLICT (user_id) DO UPDATE SET trade = EXCLUDED.trade, sector = EXCLUDED.sector,
		   market = EXCLUDED.market, status = EXCLUDED.status, regime = EXCLUDED.regime,
		   reserve = EXCLUDED.reserve, updated_at = now()`,
		id, b.Trade, b.Sector, b.Market, b.Status, b.Regime, b.Reserve); err != nil {
		return err
	}
	if !old.Valid || old.String != b.Status {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO business_status_history (user_id, old_status, new_status) VALUES ($1, $2, $3)`,
			id, old, b.Status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repo) SetStatsConsent(ctx context.Context, id string, on bool) error {
	var err error
	if on {
		_, err = r.db.ExecContext(ctx, `UPDATE users SET stats_consent_at = now(), stats_consent_version = $2 WHERE id = $1`, id, StatsConsentVersion)
	} else {
		_, err = r.db.ExecContext(ctx, `UPDATE users SET stats_consent_at = NULL, stats_consent_version = NULL WHERE id = $1`, id)
	}
	return err
}

// MergePrefs updates only the keys present in p.
func (r *Repo) MergePrefs(ctx context.Context, id string, p Prefs) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// JSON goes as string: under PgBouncer's simple protocol []byte would be sent as bytea.
	_, err = r.db.ExecContext(ctx, `UPDATE users SET prefs = prefs || $2::jsonb WHERE id = $1`, id, string(b))
	return err
}

func (r *Repo) GiveConsent(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET consent_at = now(), consent_version = $2 WHERE id = $1`, id, ConsentVersion)
	return err
}

func (r *Repo) WithdrawConsent(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET consent_at = NULL, consent_version = NULL WHERE id = $1`, id)
	return err
}

func (r *Repo) SetLang(ctx context.Context, id, lang string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET lang = $2 WHERE id = $1`, id, lang)
	return err
}

// HasConsent implements advisor.ConsentChecker.
func (r *Repo) HasConsent(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := r.db.QueryRowContext(ctx, `SELECT consent_at IS NOT NULL FROM users WHERE id = $1`, id).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return ok, err
}

// statsQuery is the corrected version of the example from the spec. Joining plans and
// calculations directly multiplies rows (N plans × M calcs) and inflates COUNT, so both
// aggregates are computed in CTEs first and the paginated plan list is joined afterwards.
const statsQuery = `
WITH calc AS (
    SELECT COUNT(*)                                                AS total,
           COUNT(*) FILTER (WHERE kind = 'loan' AND amount > 1000000) AS large_loans
      FROM loan_calculations
     WHERE user_id = $1
), plans_total AS (
    SELECT COUNT(*) AS total FROM business_plans WHERE user_id = $1
), recent AS (
    SELECT id, title, created_at
      FROM business_plans
     WHERE user_id = $1
     ORDER BY created_at DESC
     LIMIT $2 OFFSET $3
)
SELECT u.email, pt.total, c.total, c.large_loans, r.id, r.title, r.created_at
  FROM users u
 CROSS JOIN calc c
 CROSS JOIN plans_total pt
  LEFT JOIN recent r ON true
 WHERE u.id = $1
 ORDER BY r.created_at DESC`

func (r *Repo) Stats(ctx context.Context, id string, limit, offset int) (Stats, error) {
	rows, err := r.db.QueryContext(ctx, statsQuery, id, limit, offset)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	s := Stats{RecentPlans: []RecentPlan{}}
	found := false
	for rows.Next() {
		found = true
		var pid, title sql.NullString
		var created sql.NullTime
		if err := rows.Scan(&s.Email, &s.TotalPlans, &s.TotalCalculations, &s.LargeLoansCalculated, &pid, &title, &created); err != nil {
			return s, err
		}
		if pid.Valid {
			s.RecentPlans = append(s.RecentPlans, RecentPlan{pid.String, title.String, created.Time})
		}
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	if !found {
		return s, ErrNotFound
	}
	return s, nil
}

type Handler struct{ repo *Repo }

func NewHandler(r *Repo) *Handler { return &Handler{repo: r} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("GET /me", reqAuth(http.HandlerFunc(h.me)))
	mux.Handle("PATCH /me", reqAuth(http.HandlerFunc(h.update)))
	mux.Handle("POST /me/consent", reqAuth(http.HandlerFunc(h.consent)))
	mux.Handle("DELETE /me/consent", reqAuth(http.HandlerFunc(h.withdraw)))
	mux.Handle("GET /me/stats", reqAuth(http.HandlerFunc(h.stats)))
	mux.Handle("PUT /me/business", reqAuth(http.HandlerFunc(h.putBusiness)))
	mux.Handle("POST /me/survey", reqAuth(http.HandlerFunc(h.survey)))
	mux.Handle("POST /me/consent/stats", reqAuth(http.HandlerFunc(h.statsConsent(true))))
	mux.Handle("DELETE /me/consent/stats", reqAuth(http.HandlerFunc(h.statsConsent(false))))
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Profile(r.Context(), httpx.UserID(r.Context()))
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lang  *string `json:"lang"`
		Prefs *Prefs  `json:"prefs"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id := httpx.UserID(r.Context())
	if in.Lang != nil {
		if l := *in.Lang; l != "uz" && l != "ru" && l != "en" {
			httpx.FieldError(w, http.StatusUnprocessableEntity, "lang_invalid", "lang", "lang must be uz, ru or en")
			return
		}
		if err := h.repo.SetLang(r.Context(), id, *in.Lang); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	if in.Prefs != nil {
		if v := in.Prefs.VoiceSpeed; v != nil && (*v < 0.5 || *v > 2) {
			httpx.FieldError(w, http.StatusUnprocessableEntity, "voice_speed_invalid", "prefs.voice_speed", "0.5..2")
			return
		}
		if err := h.repo.MergePrefs(r.Context(), id, *in.Prefs); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	h.me(w, r)
}

var (
	sectors  = map[string]bool{"dried_fruit": true, "food": true, "clothes": true, "services": true, "other": true}
	markets  = map[string]bool{"chorsu": true, "oloy": true, "parkent": true, "quyliq": true, "other": true}
	statuses = map[string]bool{"yatt": true, "oob": true, "mchj": true, "none": true}
	regimes  = map[string]bool{"turnover": true, "vat6": true, "vat12": true}
)

func validateBusiness(b *Business) (code, field string) {
	b.Trade = strings.TrimSpace(b.Trade)
	switch {
	case b.Trade == "" || utf8.RuneCountInString(b.Trade) > 200:
		return "trade_invalid", "trade"
	case !sectors[b.Sector]:
		return "sector_invalid", "sector"
	case !markets[b.Market]:
		return "market_invalid", "market"
	case !statuses[b.Status]:
		return "status_invalid", "status"
	case !regimes[b.Regime]:
		return "regime_invalid", "regime"
	case b.Reserve < 0 || b.Reserve > 10_000_000_000_000:
		return "reserve_invalid", "reserve"
	}
	return "", ""
}

func (h *Handler) putBusiness(w http.ResponseWriter, r *http.Request) {
	var b Business
	if err := httpx.Decode(r, &b); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if code, field := validateBusiness(&b); code != "" {
		httpx.FieldError(w, http.StatusUnprocessableEntity, code, field, "invalid "+field)
		return
	}
	if err := h.repo.PutBusiness(r.Context(), httpx.UserID(r.Context()), b); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.me(w, r)
}

func (h *Handler) statsConsent(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.repo.SetStatsConsent(r.Context(), httpx.UserID(r.Context()), on); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		h.me(w, r)
	}
}

func (h *Handler) consent(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.GiveConsent(r.Context(), httpx.UserID(r.Context())); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.me(w, r)
}

func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.WithdrawConsent(r.Context(), httpx.UserID(r.Context())); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.me(w, r)
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	limit, offset := httpx.Page(r, 5, 50)
	s, err := h.repo.Stats(r.Context(), httpx.UserID(r.Context()), limit, offset)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

var surveyAnswers = map[string]bool{"fear_tax": true, "complex_docs": true, "no_benefit": true, "other": true}

// SetSurveyAnswer keeps one answer per user and question (the latest).
func (r *Repo) SetSurveyAnswer(ctx context.Context, id, question, answer string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM survey_answers WHERE user_id = $1 AND question = $2`, id, question); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO survey_answers (user_id, question, answer) VALUES ($1, $2, $3)`, id, question, answer); err != nil {
		return err
	}
	return tx.Commit()
}

// survey records "why don't you register?" for the Chamber report (shares only, n >= 5).
func (h *Handler) survey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.Question != "why_not_registered" {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "question_invalid", "question", "unknown question")
		return
	}
	if !surveyAnswers[in.Answer] {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "answer_invalid", "answer", "fear_tax, complex_docs, no_benefit or other")
		return
	}
	if err := h.repo.SetSurveyAnswer(r.Context(), httpx.UserID(r.Context()), in.Question, in.Answer); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

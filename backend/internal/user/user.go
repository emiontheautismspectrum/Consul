// Package user serves the profile, data-processing consent and aggregated statistics.
package user

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// ConsentVersion identifies the text of the consent the user accepted.
const ConsentVersion = "2026-11"

type Profile struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	Lang           string     `json:"lang"`
	ConsentAt      *time.Time `json:"consent_at"`
	ConsentVersion *string    `json:"consent_version"`
	CreatedAt      time.Time  `json:"created_at"`
}

type RecentPlan struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

type Stats struct {
	Email                string       `json:"email"`
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
	err := r.db.QueryRowContext(ctx,
		`SELECT id, email, lang, consent_at, consent_version, created_at FROM users WHERE id = $1`, id).
		Scan(&p.ID, &p.Email, &p.Lang, &p.ConsentAt, &p.ConsentVersion, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
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
	mux.Handle("GET /api/v1/me", reqAuth(http.HandlerFunc(h.me)))
	mux.Handle("PATCH /api/v1/me", reqAuth(http.HandlerFunc(h.update)))
	mux.Handle("POST /api/v1/me/consent", reqAuth(http.HandlerFunc(h.consent)))
	mux.Handle("DELETE /api/v1/me/consent", reqAuth(http.HandlerFunc(h.withdraw)))
	mux.Handle("GET /api/v1/me/stats", reqAuth(http.HandlerFunc(h.stats)))
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
		Lang string `json:"lang"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.Lang != "uz" && in.Lang != "ru" && in.Lang != "en" {
		httpx.Error(w, http.StatusUnprocessableEntity, "validation", "lang must be uz, ru or en")
		return
	}
	if err := h.repo.SetLang(r.Context(), httpx.UserID(r.Context()), in.Lang); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.me(w, r)
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

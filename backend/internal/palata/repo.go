package palata

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"time"
)

//go:embed weights.json
var weightsJSON []byte

type weightsFile struct {
	Note    string             `json:"note"`
	Sectors map[string]float64 `json:"sectors"`
}

func loadWeights() (map[string]float64, error) {
	var w weightsFile
	return w.Sectors, json.Unmarshal(weightsJSON, &w)
}

var tashkent = time.FixedZone("Asia/Tashkent", 5*60*60)

type Repo struct{ db *sql.DB }

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// entrepreneurs loads per-user aggregates for [from, to): only users with the statistics
// consent and at least MinSales sales. Only aggregates leave the database.
func (r *Repo) entrepreneurs(ctx context.Context, from, to time.Time) ([]Entrepreneur, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT b.sector, b.market, b.status <> 'none',
		       SUM(o.amount), COALESCE(SUM(o.amount) FILTER (WHERE o.fiscal), 0),
		       EXISTS (SELECT 1 FROM business_status_history h
		                WHERE h.user_id = u.id AND h.old_status = 'none' AND h.new_status <> 'none'
		                  AND h.changed_at >= $1 AND h.changed_at < $2),
		       u.synthetic OR bool_or(o.source = 'seed')
		  FROM users u
		  JOIN businesses b ON b.user_id = u.id
		  JOIN ops o ON o.user_id = u.id AND o.kind = 'sale' AND o.deleted_at IS NULL
		            AND o.occurred_at >= $1 AND o.occurred_at < $2
		 WHERE u.stats_consent_at IS NOT NULL
		 GROUP BY u.id, b.sector, b.market, b.status
		HAVING COUNT(*) >= $3`, from, to, MinSales)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entrepreneur
	for rows.Next() {
		var e Entrepreneur
		if err := rows.Scan(&e.Sector, &e.Market, &e.Official, &e.Turnover, &e.Recorded, &e.Switched, &e.Synthetic); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repo) survey(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.answer, COUNT(DISTINCT s.user_id)
		  FROM survey_answers s JOIN users u ON u.id = s.user_id
		 WHERE u.stats_consent_at IS NOT NULL AND s.question = 'why_not_registered'
		 GROUP BY s.answer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var a string
		var n int
		if err := rows.Scan(&a, &n); err != nil {
			return nil, err
		}
		out[a] = n
	}
	return out, rows.Err()
}

type row struct {
	window    int
	cut, key  string
	n         int
	metrics   any
	synthetic bool
}

// Refresh recomputes the snapshots for periodEnd (inclusive, Asia/Tashkent): windows 30
// and 90 days by all / sector / market, plus the 6-month trend. It replaces earlier
// snapshots of the same day, so re-running it is safe.
func (r *Repo) Refresh(ctx context.Context, periodEnd time.Time) error {
	weights, err := loadWeights()
	if err != nil {
		return err
	}
	pe := periodEnd.In(tashkent)
	end := time.Date(pe.Year(), pe.Month(), pe.Day()+1, 0, 0, 0, 0, tashkent)
	counts, err := r.survey(ctx)
	if err != nil {
		return err
	}
	surveyN, shares := Survey(counts)

	var rows []row
	for _, window := range []int{30, 90} {
		es, err := r.entrepreneurs(ctx, end.AddDate(0, 0, -window), end)
		if err != nil {
			return err
		}
		snap, ok := Summarize(es, weights)
		if !ok {
			continue
		}
		rows = append(rows, row{window, "all", "all", snap.All.N, map[string]any{
			"group": snap.All, "city_unrecorded_pct": snap.CityPct, "ci_pp": snap.CIpp,
			"survey": map[string]any{"n": surveyN, "answers": shares}}, snap.Synthetic})
		for _, g := range snap.Sectors {
			rows = append(rows, row{window, "sector", g.Key, g.N, g, g.Synthetic})
		}
		for _, g := range snap.Markets {
			rows = append(rows, row{window, "market", g.Key, g.N, g, g.Synthetic})
		}
	}
	// Trend: the 30-day picture at the end of each of the last 6 months (the current one
	// up to periodEnd).
	for i := 5; i >= 0; i-- {
		monthEnd := time.Date(pe.Year(), pe.Month()-time.Month(i)+1, 1, 0, 0, 0, 0, tashkent)
		if i == 0 {
			monthEnd = end
		}
		es, err := r.entrepreneurs(ctx, monthEnd.AddDate(0, 0, -30), monthEnd)
		if err != nil {
			return err
		}
		snap, ok := Summarize(es, weights)
		if !ok {
			continue
		}
		key := monthEnd.AddDate(0, 0, -1).Format("2006-01")
		rows = append(rows, row{30, "trend", key, snap.All.N, map[string]any{"month": key, "unrecorded_pct": snap.CityPct, "n": snap.All.N}, snap.Synthetic})
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	day := pe.Format(time.DateOnly)
	if _, err := tx.ExecContext(ctx, `DELETE FROM palata_snapshots WHERE period_end = $1`, day); err != nil {
		return err
	}
	for _, rw := range rows {
		if rw.n < K { // defence in depth: the CHECK constraint enforces it too
			continue
		}
		m, err := json.Marshal(rw.metrics)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO palata_snapshots (period_end, window_days, cut, cut_key, n, metrics, synthetic)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`, day, rw.window, rw.cut, rw.key, rw.n, string(m), rw.synthetic); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var ErrNoReport = errors.New("no report yet")

type stored struct {
	Cut, Key  string
	N         int
	Metrics   json.RawMessage
	Synthetic bool
	Period    time.Time
}

// latest reads the newest snapshot set for a window. It never touches ops.
func (r *Repo) latest(ctx context.Context, window int) ([]stored, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT cut, cut_key, n, metrics, synthetic, period_end
		  FROM palata_snapshots
		 WHERE window_days = $1
		   AND period_end = (SELECT max(period_end) FROM palata_snapshots WHERE window_days = $1 AND cut = 'all')
		 ORDER BY cut, cut_key`, window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.Cut, &s.Key, &s.N, &s.Metrics, &s.Synthetic, &s.Period); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if len(out) == 0 && rows.Err() == nil {
		return nil, ErrNoReport
	}
	return out, rows.Err()
}

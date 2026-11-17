package calculator

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type HistoryItem struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Input     json.RawMessage `json:"input"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"created_at"`
}

type Repo interface {
	Save(ctx context.Context, userID, kind string, in, result any) error
	History(ctx context.Context, userID, kind string, limit, offset int) ([]HistoryItem, error)
}

type PGRepo struct{ db *sql.DB }

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

func (r *PGRepo) Save(ctx context.Context, userID, kind string, in, result any) error {
	inJSON, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var amount, rate sql.NullFloat64
	var term sql.NullInt64
	if l, ok := in.(LoanInput); ok {
		amount = sql.NullFloat64{Float64: l.Amount, Valid: true}
		rate = sql.NullFloat64{Float64: l.AnnualRate, Valid: true}
		term = sql.NullInt64{Int64: int64(l.TermMonths), Valid: true}
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO loan_calculations (user_id, kind, amount, rate, term, input, result_jsonb)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		// JSON goes as string: under PgBouncer's simple protocol pgx would send []byte as bytea.
		userID, kind, amount, rate, term, string(inJSON), string(resJSON))
	return err
}

func (r *PGRepo) History(ctx context.Context, userID, kind string, limit, offset int) ([]HistoryItem, error) {
	// kind = '' means all kinds; still a single parameterized query.
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, kind, input, result_jsonb, created_at
		   FROM loan_calculations
		  WHERE user_id = $1 AND ($2 = '' OR kind = $2)
		  ORDER BY created_at DESC
		  LIMIT $3 OFFSET $4`, userID, kind, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []HistoryItem{}
	for rows.Next() {
		var it HistoryItem
		if err := rows.Scan(&it.ID, &it.Kind, &it.Input, &it.Result, &it.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

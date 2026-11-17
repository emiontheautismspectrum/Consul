package advisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("plan not found")

type Plan struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Input      json.RawMessage `json:"input,omitempty"`
	Content    string          `json:"content,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	Lang       string          `json:"lang"`
	Model      string          `json:"model,omitempty"`
	TokensIn   int             `json:"tokens_in,omitempty"`
	TokensOut  int             `json:"tokens_out,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Repo interface {
	Save(ctx context.Context, userID string, p *Plan) error
	List(ctx context.Context, userID string, limit, offset int) ([]Plan, int, error)
	Get(ctx context.Context, userID, id string) (Plan, error)
	Delete(ctx context.Context, userID, id string) error
}

type PGRepo struct{ db *sql.DB }

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

func (r *PGRepo) Save(ctx context.Context, userID string, p *Plan) error {
	// JSON params are passed as strings: under PgBouncer's simple protocol pgx would send
	// []byte as bytea, which PostgreSQL rejects for jsonb columns.
	var structured any
	if len(p.Structured) > 0 {
		structured = string(p.Structured)
	}
	return r.db.QueryRowContext(ctx,
		`INSERT INTO business_plans (user_id, title, input, content, structured, tool_calls, lang, model, tokens_in, tokens_out)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		userID, p.Title, string(p.Input), p.Content, structured, string(p.ToolCalls), p.Lang, p.Model, p.TokensIn, p.TokensOut).
		Scan(&p.ID, &p.CreatedAt)
}

// List returns plan headers (newest first, served by idx_bplans_user_created) and the total count.
func (r *PGRepo) List(ctx context.Context, userID string, limit, offset int) ([]Plan, int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, lang, created_at, COUNT(*) OVER () AS total
		   FROM business_plans
		  WHERE user_id = $1
		  ORDER BY created_at DESC
		  LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	plans, total := []Plan{}, 0
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Title, &p.Lang, &p.CreatedAt, &total); err != nil {
			return nil, 0, err
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(plans) == 0 && offset > 0 { // page past the end: still report the real total
		err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM business_plans WHERE user_id = $1`, userID).Scan(&total)
	}
	return plans, total, err
}

func (r *PGRepo) Get(ctx context.Context, userID, id string) (Plan, error) {
	var p Plan
	var structured []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT id, title, input, content, structured, tool_calls, lang, model, tokens_in, tokens_out, created_at
		   FROM business_plans WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&p.ID, &p.Title, &p.Input, &p.Content, &structured, &p.ToolCalls, &p.Lang, &p.Model, &p.TokensIn, &p.TokensOut, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.Structured = structured
	return p, err
}

func (r *PGRepo) Delete(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM business_plans WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

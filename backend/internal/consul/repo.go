package consul

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
)

type PGRepo struct{ db *sql.DB }

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

func (r *PGRepo) EnsureChat(ctx context.Context, userID, chatID, title, topic string) (string, error) {
	if chatID != "" {
		if uuid.Validate(chatID) != nil {
			return "", ErrChatNotFound
		}
		res, err := r.db.ExecContext(ctx, `UPDATE chats SET updated_at = now() WHERE id = $1 AND user_id = $2`, chatID, userID)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", ErrChatNotFound
		}
		return chatID, nil
	}
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO chats (user_id, title, topic) VALUES ($1, $2, $3) RETURNING id`, userID, title, topic).Scan(&id)
	return id, err
}

func (r *PGRepo) SaveTurn(ctx context.Context, chatID, userID, userText string, reply Reply, meta Meta) error {
	rb, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	var trace any
	if len(meta.Trace) > 0 {
		b, _ := json.Marshal(meta.Trace)
		trace = string(b) // JSON as string: PgBouncer's simple protocol would send []byte as bytea
	}
	var fallback, model sql.NullString
	if meta.Fallback != "" {
		fallback = sql.NullString{String: meta.Fallback, Valid: true}
	}
	if meta.Model != "" {
		model = sql.NullString{String: meta.Model, Valid: true}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (chat_id, user_id, role, text) VALUES ($1, $2, 'user', $3)`, chatID, userID, userText); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (chat_id, user_id, role, text, reply, tool_calls, model, tokens_in, tokens_out, latency_ms, fallback)
		 VALUES ($1, $2, 'assistant', $3, $4, $5, $6, $7, $8, $9, $10)`,
		chatID, userID, reply.Text, string(rb), trace, model, meta.TokensIn, meta.TokensOut, meta.LatencyMS, fallback); err != nil {
		return err
	}
	return tx.Commit()
}

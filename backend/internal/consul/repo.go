package consul

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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

// --- chat history ---

type ChatSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Topic     string    `json:"topic"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ChatMessage struct {
	Role      string          `json:"role"`
	Text      string          `json:"text"`
	Reply     json.RawMessage `json:"reply,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (r *PGRepo) ListChats(ctx context.Context, userID string, limit int) ([]ChatSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, topic, updated_at FROM chats WHERE user_id = $1 ORDER BY updated_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatSummary{}
	for rows.Next() {
		var c ChatSummary
		if err := rows.Scan(&c.ID, &c.Title, &c.Topic, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PGRepo) ChatMessages(ctx context.Context, userID, chatID string) ([]ChatMessage, error) {
	if uuid.Validate(chatID) != nil {
		return nil, ErrChatNotFound
	}
	var owned bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM chats WHERE id = $1 AND user_id = $2)`, chatID, userID).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrChatNotFound
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT role, text, reply, created_at FROM messages WHERE chat_id = $1 AND user_id = $2 ORDER BY created_at, id`, chatID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMessage{}
	for rows.Next() {
		var m ChatMessage
		var reply sql.NullString
		if err := rows.Scan(&m.Role, &m.Text, &reply, &m.CreatedAt); err != nil {
			return nil, err
		}
		if reply.Valid {
			m.Reply = json.RawMessage(reply.String)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *PGRepo) DeleteChat(ctx context.Context, userID, chatID string) error {
	if uuid.Validate(chatID) != nil {
		return ErrChatNotFound
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM chats WHERE id = $1 AND user_id = $2`, chatID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrChatNotFound
	}
	return nil
}

// --- Consul memory: facts the user can see, edit and erase ---

type Fact struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

const maxFacts = 50

var ErrTooManyFacts = errors.New("too many facts")

func (r *PGRepo) Facts(ctx context.Context, userID string) ([]Fact, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value, updated_at FROM memory_facts WHERE user_id = $1 ORDER BY updated_at, key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fact{}
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.Key, &f.Value, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PutFact upserts a fact; a new key beyond maxFacts is refused.
func (r *PGRepo) PutFact(ctx context.Context, userID, key, value string) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO memory_facts (user_id, key, value)
		 SELECT $1, $2, $3 WHERE EXISTS (SELECT 1 FROM memory_facts WHERE user_id = $1 AND key = $2)
		                      OR (SELECT count(*) FROM memory_facts WHERE user_id = $1) < $4
		 ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, key, value, maxFacts)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTooManyFacts
	}
	return nil
}

func (r *PGRepo) DeleteFact(ctx context.Context, userID, key string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM memory_facts WHERE user_id = $1 AND key = $2`, userID, key)
	return err
}

func (r *PGRepo) DeleteAllFacts(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM memory_facts WHERE user_id = $1`, userID)
	return err
}

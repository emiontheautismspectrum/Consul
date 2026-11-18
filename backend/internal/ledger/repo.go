package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a row does not exist or belongs to another user.
var ErrNotFound = errors.New("not found")

// Replay signals that the Idempotency-Key was already used; Response is the stored
// first answer and must be returned as-is instead of inserting a duplicate op.
type Replay struct{ Response json.RawMessage }

func (Replay) Error() string { return "idempotent replay" }

type PGRepo struct{ db *sql.DB }

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db} }

func nullStr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

// LoadState loads everything the pure computations need for one user.
func (r *PGRepo) LoadState(ctx context.Context, userID string) (State, error) {
	var st State
	st.Business = DefaultBusiness()
	st.Counterparties = map[string]Counterparty{}

	err := r.db.QueryRowContext(ctx,
		`SELECT trade, sector, market, status, regime, reserve
		   FROM businesses WHERE user_id = $1`, userID).
		Scan(&st.Business.Trade, &st.Business.Sector, &st.Business.Market,
			&st.Business.Status, &st.Business.Regime, &st.Business.Reserve)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("load business: %w", err)
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, phone FROM counterparties WHERE user_id = $1`, userID)
	if err != nil {
		return st, fmt.Errorf("load counterparties: %w", err)
	}
	for rows.Next() {
		var id, name string
		var phone sql.NullString
		if err := rows.Scan(&id, &name, &phone); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan counterparty: %w", err)
		}
		cp := Counterparty{Name: name}
		if phone.Valid {
			cp.Phone = &phone.String
		}
		st.Counterparties[id] = cp
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	rows, err = r.db.QueryContext(ctx,
		`SELECT o.id, o.kind, o.amount, o.method, o.label, o.counterparty_id, c.name,
		        o.fiscal, o.source, o.items, o.occurred_at, o.created_at
		   FROM ops o
		   LEFT JOIN counterparties c ON c.id = o.counterparty_id
		  WHERE o.user_id = $1 AND o.deleted_at IS NULL
		  ORDER BY o.occurred_at, o.created_at`, userID)
	if err != nil {
		return st, fmt.Errorf("load ops: %w", err)
	}
	for rows.Next() {
		var o Op
		var cpID, cpName, label, source sql.NullString
		var items sql.NullString
		if err := rows.Scan(&o.ID, &o.Kind, &o.Amount, &o.Method, &label, &cpID, &cpName,
			&o.Fiscal, &source, &items, &o.OccurredAt, &o.CreatedAt); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan op: %w", err)
		}
		o.Label = label.String
		o.Source = source.String
		if cpID.Valid {
			o.CounterpartyID = &cpID.String
		}
		if cpName.Valid {
			o.Counterparty = &cpName.String
		}
		if items.Valid {
			o.Items = json.RawMessage(items.String)
		}
		st.Ops = append(st.Ops, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	rows, err = r.db.QueryContext(ctx,
		`SELECT ob.id, ob.kind, ob.label, ob.amount, ob.due_day, ob.due_date, ob.recurring,
		        ob.counterparty_id, c.name, ob.created_at
		   FROM obligations ob
		   LEFT JOIN counterparties c ON c.id = ob.counterparty_id
		  WHERE ob.user_id = $1`, userID)
	if err != nil {
		return st, fmt.Errorf("load obligations: %w", err)
	}
	for rows.Next() {
		var ob Obligation
		var dueDay sql.NullInt64
		var dueDate sql.NullTime
		var cpID, cpName sql.NullString
		if err := rows.Scan(&ob.ID, &ob.Kind, &ob.Label, &ob.Amount, &dueDay, &dueDate,
			&ob.Recurring, &cpID, &cpName, &ob.CreatedAt); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan obligation: %w", err)
		}
		if dueDay.Valid {
			d := int(dueDay.Int64)
			ob.DueDay = &d
		}
		if dueDate.Valid {
			ob.DueDate = &dueDate.Time
		}
		if cpID.Valid {
			ob.CounterpartyID = &cpID.String
		}
		if cpName.Valid {
			ob.Counterparty = &cpName.String
		}
		st.Obligations = append(st.Obligations, ob)
	}
	rows.Close()
	return st, rows.Err()
}

// InsertOp stores the op together with its API response; a repeated Idempotency-Key
// returns a Replay error carrying the first response and inserts nothing.
func (r *PGRepo) InsertOp(ctx context.Context, userID string, op *Op, idemKey string, response json.RawMessage) error {
	if op.ID == "" {
		op.ID = uuid.NewString()
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO ops (id, user_id, kind, amount, method, label, counterparty_id, fiscal,
		                  source, raw_text, items, occurred_at, idempotency_key, response)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), $14)
		 RETURNING created_at`,
		op.ID, userID, op.Kind, op.Amount, op.Method, op.Label, nullStr(op.CounterpartyID), op.Fiscal,
		op.Source, nullStr2(op.RawText), nullStr2(string(op.Items)), op.OccurredAt, idemKey, string(response)).
		Scan(&op.CreatedAt)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || idemKey == "" {
		op.ID = "" // not stored after all
		return err
	}
	var stored sql.NullString
	qerr := r.db.QueryRowContext(ctx,
		`SELECT response FROM ops WHERE user_id = $1 AND idempotency_key = $2`, userID, idemKey).Scan(&stored)
	if qerr != nil {
		return qerr
	}
	return Replay{Response: json.RawMessage(stored.String)}
}

func nullStr2(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// SoftDeleteOp marks the op deleted; ErrNotFound covers both missing and another user's ops.
func (r *PGRepo) SoftDeleteOp(ctx context.Context, userID, opID string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE ops SET deleted_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		opID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OpFilter narrows ListOps. From/To are day bounds in Asia/Tashkent, both inclusive;
// zero values mean unbounded.
type OpFilter struct {
	Kind     string
	From, To time.Time
	Limit    int
	// Cursor points at the last row of the previous page (occurred_at, id).
	CursorOccurredAt time.Time
	CursorID         string
	HasCursor        bool
}

// ListOps returns up to Limit ops, newest first, and the cursor pointing at the
// last returned row (nil when the page was not full).
func (r *PGRepo) ListOps(ctx context.Context, userID string, f OpFilter) ([]Op, *Cursor, error) {
	var from, to, curAt *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		end := day(f.To).AddDate(0, 0, 1)
		to = &end
	}
	var curID sql.NullString
	if f.HasCursor {
		curAt = &f.CursorOccurredAt
		curID = sql.NullString{String: f.CursorID, Valid: true}
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT o.id, o.kind, o.amount, o.method, o.label, o.counterparty_id, c.name,
		        o.fiscal, o.source, o.items, o.occurred_at, o.created_at
		   FROM ops o
		   LEFT JOIN counterparties c ON c.id = o.counterparty_id
		  WHERE o.user_id = $1 AND o.deleted_at IS NULL
		    AND ($2 = '' OR o.kind = $2)
		    AND ($3::timestamptz IS NULL OR o.occurred_at >= $3)
		    AND ($4::timestamptz IS NULL OR o.occurred_at < $4)
		    AND (NOT $5 OR (o.occurred_at, o.id) < ($6, $7::uuid))
		  ORDER BY o.occurred_at DESC, o.id DESC
		  LIMIT $8`, userID, f.Kind, from, to, f.HasCursor, curAt, curID, f.Limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ops := []Op{}
	for rows.Next() {
		var o Op
		var cpID, cpName, label, source, items sql.NullString
		if err := rows.Scan(&o.ID, &o.Kind, &o.Amount, &o.Method, &label, &cpID, &cpName,
			&o.Fiscal, &source, &items, &o.OccurredAt, &o.CreatedAt); err != nil {
			return nil, nil, err
		}
		o.Label = label.String
		o.Source = source.String
		if cpID.Valid {
			o.CounterpartyID = &cpID.String
		}
		if cpName.Valid {
			o.Counterparty = &cpName.String
		}
		if items.Valid {
			o.Items = json.RawMessage(items.String)
		}
		ops = append(ops, o)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(ops) < f.Limit || len(ops) == 0 {
		return ops, nil, nil
	}
	last := ops[len(ops)-1]
	return ops, &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}, nil
}

// FindOrCreateCounterparty returns the id of the (user, kind, name_key) row,
// creating it on first use. The latest spelling of the name wins.
func (r *PGRepo) FindOrCreateCounterparty(ctx context.Context, userID, kind, name string) (string, error) {
	key := NameKey(name)
	if key == "" {
		return "", fmt.Errorf("counterparty name has no letters: %q", name)
	}
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO counterparties (user_id, kind, name, name_key)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, kind, name_key) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, userID, kind, name, key).Scan(&id)
	return id, err
}

// InsertObligation stores a payment obligation and fills ID and CreatedAt.
func (r *PGRepo) InsertObligation(ctx context.Context, userID string, ob *Obligation) error {
	return r.db.QueryRowContext(ctx,
		`INSERT INTO obligations (user_id, kind, label, amount, due_day, due_date, recurring, counterparty_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at`,
		userID, ob.Kind, ob.Label, ob.Amount, nullInt(ob.DueDay), nullDate(ob.DueDate),
		ob.Recurring, nullStr(ob.CounterpartyID)).Scan(&ob.ID, &ob.CreatedAt)
}

// UpdateObligation fully replaces the obligation (PUT semantics behind PATCH).
func (r *PGRepo) UpdateObligation(ctx context.Context, userID string, ob *Obligation) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE obligations
		    SET kind = $3, label = $4, amount = $5, due_day = $6, due_date = $7,
		        recurring = $8, counterparty_id = $9
		  WHERE id = $1 AND user_id = $2`,
		ob.ID, userID, ob.Kind, ob.Label, ob.Amount, nullInt(ob.DueDay), nullDate(ob.DueDate),
		ob.Recurring, nullStr(ob.CounterpartyID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PGRepo) DeleteObligation(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM obligations WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func nullDate(p *time.Time) sql.NullTime {
	if p == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}

// UserLang returns the profile language for templates (uz, ru).
func (r *PGRepo) UserLang(ctx context.Context, userID string) (string, error) {
	var lang string
	err := r.db.QueryRowContext(ctx,
		`SELECT lang FROM users WHERE id = $1`, userID).Scan(&lang)
	if err != nil {
		return "", err
	}
	return lang, nil
}

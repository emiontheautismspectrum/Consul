package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrEmailTaken = errors.New("email already registered")
	ErrNotFound   = errors.New("not found")
)

type User struct {
	ID, Email, PasswordHash, Salt, Lang string
}

type Repo interface {
	Create(ctx context.Context, email, hash, salt, lang string) (string, error)
	ByEmail(ctx context.Context, email string) (User, error)
}

// Deleter removes a user and, by ON DELETE CASCADE, everything they own (operations, debts,
// obligations, chats, memory, plans, survey answers). Aggregated Chamber snapshots are
// anonymous and stay.
type Deleter interface {
	DeleteUser(ctx context.Context, id string) error
}

// PhoneRepo is the phone-login part of the user store.
type PhoneRepo interface {
	// FindOrCreateByPhone returns the user id and whether the user was just created.
	FindOrCreateByPhone(ctx context.Context, phone, lang string) (string, bool, error)
}

type PGRepo struct{ db *sql.DB }

func NewPGRepo(db *sql.DB) *PGRepo { return &PGRepo{db: db} }

func (r *PGRepo) Create(ctx context.Context, email, hash, salt, lang string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, salt, lang) VALUES ($1, $2, $3, $4) RETURNING id`,
		email, hash, salt, lang).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrEmailTaken
	}
	return id, err
}

func (r *PGRepo) ByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := r.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, salt, lang FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Salt, &u.Lang)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (r *PGRepo) FindOrCreateByPhone(ctx context.Context, phone, lang string) (string, bool, error) {
	var id string
	var created bool
	// xmax = 0 only for a freshly inserted row: tells "created" from "already existed"
	// in one round trip, also under concurrent first logins.
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (phone, lang) VALUES ($1, $2)
		 ON CONFLICT (phone) WHERE phone IS NOT NULL DO UPDATE SET phone = EXCLUDED.phone
		 RETURNING id, (xmax = 0)`, phone, lang).Scan(&id, &created)
	return id, created, err
}

func (r *PGRepo) DeleteUser(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

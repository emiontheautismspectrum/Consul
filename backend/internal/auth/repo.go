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

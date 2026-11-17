package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrBadCredentials = errors.New("invalid email or password")
	ErrValidation     = errors.New("validation")
)

// Blacklist stores revoked refresh-token ids (Redis).
type Blacklist interface {
	Revoke(ctx context.Context, jti string, ttl time.Duration) error
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

type Service struct {
	repo   Repo
	hasher *Hasher
	jwt    *JWTIssuer
	bl     Blacklist
	// dummy hash used to keep login timing equal for unknown emails
	dummySalt, dummyHash string
}

func NewService(repo Repo, h *Hasher, j *JWTIssuer, bl Blacklist) *Service {
	s := &Service{repo: repo, hasher: h, jwt: j, bl: bl}
	s.dummySalt, _ = h.NewSalt()
	s.dummyHash = h.Hash("dummy-password", s.dummySalt)
	return s
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func validate(email, password string) error {
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 255 {
		return errors.Join(ErrValidation, errors.New("invalid email"))
	}
	if len(password) < 8 || len(password) > 128 {
		return errors.Join(ErrValidation, errors.New("password must be 8-128 characters"))
	}
	return nil
}

func (s *Service) Register(ctx context.Context, email, password, lang string) (string, Tokens, error) {
	email = normalizeEmail(email)
	if err := validate(email, password); err != nil {
		return "", Tokens{}, err
	}
	salt, err := s.hasher.NewSalt()
	if err != nil {
		return "", Tokens{}, err
	}
	id, err := s.repo.Create(ctx, email, s.hasher.Hash(password, salt), salt, NormalizeLang(lang))
	if err != nil {
		return "", Tokens{}, err
	}
	t, err := s.jwt.Issue(id)
	return id, t, err
}

func (s *Service) Login(ctx context.Context, email, password string) (string, Tokens, error) {
	u, err := s.repo.ByEmail(ctx, normalizeEmail(email))
	if errors.Is(err, ErrNotFound) {
		s.hasher.Verify(password, s.dummySalt, s.dummyHash)
		return "", Tokens{}, ErrBadCredentials
	}
	if err != nil {
		return "", Tokens{}, err
	}
	if !s.hasher.Verify(password, u.Salt, u.PasswordHash) {
		return "", Tokens{}, ErrBadCredentials
	}
	t, err := s.jwt.Issue(u.ID)
	return u.ID, t, err
}

// Refresh validates the refresh token, revokes it (rotation) and issues a new pair.
// Reusing an already rotated token fails with ErrInvalidToken.
func (s *Service) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	c, err := s.jwt.ParseRefresh(refresh)
	if err != nil {
		return Tokens{}, ErrInvalidToken
	}
	revoked, err := s.bl.IsRevoked(ctx, c.ID)
	if err != nil {
		return Tokens{}, err
	}
	if revoked {
		return Tokens{}, ErrInvalidToken
	}
	if err := s.bl.Revoke(ctx, c.ID, time.Until(c.ExpiresAt.Time)); err != nil {
		return Tokens{}, err
	}
	return s.jwt.Issue(c.Subject)
}

func (s *Service) Logout(ctx context.Context, refresh string) error {
	c, err := s.jwt.ParseRefresh(refresh)
	if err != nil {
		return nil // nothing to revoke; cookies are cleared anyway
	}
	return s.bl.Revoke(ctx, c.ID, time.Until(c.ExpiresAt.Time))
}

// NormalizeLang maps any input to one of the supported languages.
func NormalizeLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if len(l) >= 2 {
		l = l[:2]
	}
	switch l {
	case "uz", "ru", "en":
		return l
	}
	return "ru"
}

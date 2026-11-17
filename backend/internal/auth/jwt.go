package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	typAccess  = "access"
	typRefresh = "refresh"
)

type Claims struct {
	Typ string `json:"typ"`
	jwt.RegisteredClaims
}

type Tokens struct {
	Access, Refresh       string
	AccessExp, RefreshExp time.Time
}

type JWTIssuer struct {
	accessSecret, refreshSecret []byte
	accessTTL, refreshTTL       time.Duration
	now                         func() time.Time
}

func NewJWTIssuer(accessSecret, refreshSecret string, accessTTL, refreshTTL time.Duration) *JWTIssuer {
	return &JWTIssuer{[]byte(accessSecret), []byte(refreshSecret), accessTTL, refreshTTL, time.Now}
}

func (j *JWTIssuer) Issue(userID string) (Tokens, error) {
	now := j.now()
	t := Tokens{AccessExp: now.Add(j.accessTTL), RefreshExp: now.Add(j.refreshTTL)}
	var err error
	if t.Access, err = j.sign(userID, typAccess, t.AccessExp, j.accessSecret); err != nil {
		return t, err
	}
	t.Refresh, err = j.sign(userID, typRefresh, t.RefreshExp, j.refreshSecret)
	return t, err
}

func (j *JWTIssuer) sign(sub, typ string, exp time.Time, secret []byte) (string, error) {
	c := Claims{Typ: typ, RegisteredClaims: jwt.RegisteredClaims{
		Subject:   sub,
		ID:        uuid.NewString(),
		IssuedAt:  jwt.NewNumericDate(j.now()),
		ExpiresAt: jwt.NewNumericDate(exp),
		Issuer:    "finadvisor",
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

var ErrInvalidToken = errors.New("invalid token")

func (j *JWTIssuer) ParseAccess(s string) (*Claims, error) {
	return j.parse(s, typAccess, j.accessSecret)
}
func (j *JWTIssuer) ParseRefresh(s string) (*Claims, error) {
	return j.parse(s, typRefresh, j.refreshSecret)
}

func (j *JWTIssuer) parse(s, typ string, secret []byte) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(s, c, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("finadvisor"),
		jwt.WithTimeFunc(j.now),
		jwt.WithExpirationRequired())
	if err != nil || c.Typ != typ || c.Subject == "" || c.ID == "" {
		return nil, ErrInvalidToken
	}
	return c, nil
}

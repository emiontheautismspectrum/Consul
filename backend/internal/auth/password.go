package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP baseline: m=64MiB, t=3, p=2).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// Hasher implements hash = Argon2id(password + salt + pepper). The salt is unique per user
// and stored in the DB; the pepper lives only in the backend environment.
type Hasher struct{ pepper []byte }

func NewHasher(pepper string) *Hasher { return &Hasher{pepper: []byte(pepper)} }

func (h *Hasher) NewSalt() (string, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

func (h *Hasher) Hash(password, salt string) string {
	input := append([]byte(password+salt), h.pepper...)
	key := argon2.IDKey(input, []byte(salt), argonTime, argonMemory, argonThreads, argonKeyLen)
	return base64.RawStdEncoding.EncodeToString(key)
}

func (h *Hasher) Verify(password, salt, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(h.Hash(password, salt)), []byte(hash)) == 1
}

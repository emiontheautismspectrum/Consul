package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"runtime"

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
//
// Each hash allocates argonMemory (64 MiB) and already uses argonThreads cores, so running
// more hashes in parallel than there are cores adds no throughput — only memory. Without
// this bound, ~16 concurrent logins exceed a 1 GiB container and the API is OOM-killed
// (found by load testing). The semaphore caps Argon2 memory at NumCPU × 64 MiB.
type Hasher struct {
	pepper []byte
	sem    chan struct{}
}

func NewHasher(pepper string) *Hasher {
	return &Hasher{pepper: []byte(pepper), sem: make(chan struct{}, max(2, runtime.NumCPU()))}
}

func (h *Hasher) NewSalt() (string, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b), nil
}

func (h *Hasher) Hash(password, salt string) string {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()
	input := append([]byte(password+salt), h.pepper...)
	key := argon2.IDKey(input, []byte(salt), argonTime, argonMemory, argonThreads, argonKeyLen)
	return base64.RawStdEncoding.EncodeToString(key)
}

func (h *Hasher) Verify(password, salt, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(h.Hash(password, salt)), []byte(hash)) == 1
}

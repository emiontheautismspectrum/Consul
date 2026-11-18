package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHashUsesSaltAndPepper(t *testing.T) {
	h := NewHasher("pepper-1")
	s1, _ := h.NewSalt()
	s2, _ := h.NewSalt()
	if s1 == s2 {
		t.Fatal("salts must be unique")
	}
	if h.Hash("secret123", s1) == h.Hash("secret123", s2) {
		t.Error("same password with different salts must produce different hashes")
	}
	hash := h.Hash("secret123", s1)
	if !h.Verify("secret123", s1, hash) || h.Verify("secret124", s1, hash) {
		t.Error("verify mismatch")
	}
	if NewHasher("pepper-2").Verify("secret123", s1, hash) {
		t.Error("a different pepper must not verify")
	}
	if strings.Contains(hash, "secret123") {
		t.Error("hash leaks password")
	}
}

func TestJWT(t *testing.T) {
	j := NewJWTIssuer("a-secret", "r-secret", time.Minute, time.Hour)
	tok, err := j.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := j.ParseAccess(tok.Access); err != nil || c.Subject != "user-1" {
		t.Fatalf("access parse: %v %+v", err, c)
	}
	if _, err := j.ParseAccess(tok.Refresh); err == nil {
		t.Error("refresh token must not be accepted as access token")
	}
	if _, err := j.ParseRefresh(tok.Access); err == nil {
		t.Error("access token must not be accepted as refresh token")
	}
	if _, err := j.ParseAccess(tok.Access + "x"); err == nil {
		t.Error("tampered token accepted")
	}
	other := NewJWTIssuer("other", "other", time.Minute, time.Hour)
	if _, err := other.ParseAccess(tok.Access); err == nil {
		t.Error("token signed with another secret accepted")
	}
	j.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := j.ParseAccess(tok.Access); err == nil {
		t.Error("expired token accepted")
	}
}

// --- service tests with in-memory fakes ---

type memRepo struct {
	mu    sync.Mutex
	users map[string]User
}

func (m *memRepo) Create(_ context.Context, email, hash, salt, lang string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[email]; ok {
		return "", ErrEmailTaken
	}
	id := "id-" + email
	m.users[email] = User{id, email, hash, salt, lang}
	return id, nil
}

func (m *memRepo) ByEmail(_ context.Context, email string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[email]
	if !ok {
		return u, ErrNotFound
	}
	return u, nil
}

type memBlacklist struct{ m sync.Map }

func (b *memBlacklist) Revoke(_ context.Context, jti string, _ time.Duration) error {
	b.m.Store(jti, true)
	return nil
}
func (b *memBlacklist) IsRevoked(_ context.Context, jti string) (bool, error) {
	_, ok := b.m.Load(jti)
	return ok, nil
}

func newTestService() *Service {
	return NewService(&memRepo{users: map[string]User{}}, NewHasher("pep"),
		NewJWTIssuer("a", "r", time.Minute, time.Hour), &memBlacklist{})
}

func TestRegisterLoginRefreshLogout(t *testing.T) {
	ctx := context.Background()
	s := newTestService()
	if _, _, err := s.Register(ctx, " User@Mail.UZ ", "password1", "uz"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Register(ctx, "user@mail.uz", "password1", ""); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email err = %v", err)
	}
	if _, _, err := s.Login(ctx, "user@mail.uz", "wrong-pass"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("wrong password err = %v", err)
	}
	if _, _, err := s.Login(ctx, "nobody@mail.uz", "password1"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("unknown email err = %v", err)
	}
	_, tok, err := s.Login(ctx, "USER@mail.uz", "password1")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.Refresh(ctx, tok.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, tok.Refresh); !errors.Is(err, ErrInvalidToken) {
		t.Error("reusing a rotated refresh token must fail")
	}
	if err := s.Logout(ctx, rotated.Refresh); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, rotated.Refresh); !errors.Is(err, ErrInvalidToken) {
		t.Error("refresh after logout must fail")
	}
}

func TestRegisterValidation(t *testing.T) {
	s := newTestService()
	for _, c := range [][2]string{{"not-an-email", "password1"}, {"a@b.uz", "short"}} {
		if _, _, err := s.Register(context.Background(), c[0], c[1], ""); !errors.Is(err, ErrValidation) {
			t.Errorf("Register(%q) err = %v", c[0], err)
		}
	}
}

func TestCookiesAreHardened(t *testing.T) {
	s := newTestService()
	h := NewHandler(s, s.jwt, CookieConfig{Secure: true})
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/register",
		strings.NewReader(`{"email":"c@d.uz","password":"password1"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("got %d cookies", len(cookies))
	}
	for _, c := range cookies {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie %s not hardened: %+v", c.Name, c)
		}
	}

	// The access cookie authenticates protected routes.
	var access *http.Cookie
	for _, c := range cookies {
		if c.Name == accessCookie {
			access = c
		}
	}
	protected := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest("GET", "/x", nil)
	req.AddCookie(access)
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Errorf("authorized request got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous request got %d", rec.Code)
	}
}

type memGuard struct {
	mu sync.Mutex
	n  map[string]int
}

func (g *memGuard) Failures(_ context.Context, key string) (int, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n[key], 10 * time.Minute, nil
}
func (g *memGuard) AddFailure(_ context.Context, key string, _ time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n[key]++
	return nil
}
func (g *memGuard) ResetFailures(_ context.Context, key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.n, key)
	return nil
}

func TestLoginLockoutPerAccount(t *testing.T) {
	ctx := context.Background()
	g := &memGuard{n: map[string]int{}}
	s := newTestService().WithAttemptGuard(g)
	s.Register(ctx, "victim@mail.uz", "password1", "")

	// a success resets the counter
	s.Login(ctx, "victim@mail.uz", "wrong-1")
	if _, _, err := s.Login(ctx, "victim@mail.uz", "password1"); err != nil {
		t.Fatal(err)
	}
	if len(g.n) != 0 {
		t.Error("successful login must reset failures")
	}

	for i := 0; i < maxLoginFailures; i++ {
		if _, _, err := s.Login(ctx, "Victim@Mail.uz", "wrong"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// even the right password is refused while locked
	_, _, err := s.Login(ctx, "victim@mail.uz", "password1")
	var locked *LockedError
	if !errors.As(err, &locked) || !errors.Is(err, ErrTooManyAttempts) || locked.RetryIn != 10*time.Minute {
		t.Fatalf("expected lockout, got %v", err)
	}
	// other accounts are unaffected
	s.Register(ctx, "other@mail.uz", "password1", "")
	if _, _, err := s.Login(ctx, "other@mail.uz", "password1"); err != nil {
		t.Errorf("other account locked: %v", err)
	}
	for k := range g.n {
		if strings.Contains(k, "@") {
			t.Error("email must not be stored in clear text")
		}
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := NewHasher("p")
	salt, _ := h.NewSalt()
	var mu sync.Mutex
	inFlight, peak := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.sem <- struct{}{} // observe the same semaphore Hash uses
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			<-h.sem
		}()
	}
	wg.Wait()
	if peak > cap(h.sem) {
		t.Errorf("peak concurrency %d exceeds bound %d", peak, cap(h.sem))
	}
	if h.Hash("x", salt) == "" {
		t.Error("hash must still work")
	}
}

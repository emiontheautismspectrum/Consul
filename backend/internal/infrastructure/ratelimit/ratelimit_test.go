package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// memLimiter is an in-memory fixed-window limiter (window never expires in tests).
type memLimiter struct {
	mu   sync.Mutex
	hits map[string]int
	err  error
}

func (m *memLimiter) Take(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if m.err != nil {
		return Result{}, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hits == nil {
		m.hits = map[string]int{}
	}
	m.hits[key]++
	n := m.hits[key]
	return Result{Allowed: n <= limit, Limit: limit, Remaining: max(limit-n, 0), ResetIn: window}, nil
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

func do(h http.Handler, method, path, ip, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Real-IP", ip)
	if user != "" {
		r = r.WithContext(httpx.WithUserID(r.Context(), user))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestGlobalLimitHeadersAnd429(t *testing.T) {
	h := Global(&memLimiter{}, Rule{Name: "api", Limit: 2, Window: time.Minute, Key: ByIP}, "/healthz")(ok)
	for i := 0; i < 2; i++ {
		if rec := do(h, "GET", "/x", "1.1.1.1", ""); rec.Code != 204 || rec.Header().Get("X-RateLimit-Limit") != "2" {
			t.Fatalf("request %d: %d %v", i, rec.Code, rec.Header())
		}
	}
	rec := do(h, "GET", "/x", "1.1.1.1", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" || rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("over limit: %d %v", rec.Code, rec.Header())
	}
	if rec := do(h, "GET", "/x", "2.2.2.2", ""); rec.Code != 204 {
		t.Error("another IP must have its own budget")
	}
	for i := 0; i < 5; i++ {
		if rec := do(h, "GET", "/healthz", "1.1.1.1", ""); rec.Code != 204 {
			t.Fatal("health checks must not be limited")
		}
	}
}

func TestUserKeyFollowsUserAcrossIPs(t *testing.T) {
	h := Global(&memLimiter{}, Rule{Name: "api", Limit: 1, Window: time.Minute, Key: ByUserOrIP})(ok)
	do(h, "GET", "/x", "1.1.1.1", "alice")
	if rec := do(h, "GET", "/x", "9.9.9.9", "alice"); rec.Code != http.StatusTooManyRequests {
		t.Error("a user must not get a fresh budget by switching IP")
	}
}

func TestRoutesOnlyMatchConfiguredRoutes(t *testing.T) {
	h := Routes(&memLimiter{}, map[string]Rule{
		"POST /photo": {Name: "photo", Limit: 1, Window: time.Minute, Key: ByIP},
	})(ok)
	do(h, "POST", "/photo", "1.1.1.1", "")
	if rec := do(h, "POST", "/photo", "1.1.1.1", ""); rec.Code != http.StatusTooManyRequests {
		t.Error("route limit not applied")
	}
	if rec := do(h, "GET", "/photo", "1.1.1.1", ""); rec.Code != 204 {
		t.Error("other method must not be limited")
	}
	if rec := do(h, "POST", "/other", "1.1.1.1", ""); rec.Code != 204 {
		t.Error("other path must not be limited")
	}
}

func TestFailOpenWhenRedisDown(t *testing.T) {
	h := Global(&memLimiter{err: errors.New("redis down")}, Rule{Name: "api", Limit: 1, Window: time.Minute, Key: ByIP})(ok)
	for i := 0; i < 3; i++ {
		if rec := do(h, "GET", "/x", "1.1.1.1", ""); rec.Code != 204 {
			t.Fatal("limiter outage must not block requests")
		}
	}
}

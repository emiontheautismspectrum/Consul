package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/ratelimit"
)

// memStore imitates the Redis calls the OTP flow uses (TTLs are not simulated).
type memStore struct {
	mu   sync.Mutex
	kv   map[string][]byte
	hits map[string]int
}

func newMemStore() *memStore { return &memStore{kv: map[string][]byte{}, hits: map[string]int{}} }

func (m *memStore) SetJSON(_ context.Context, k string, v any, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(v)
	m.kv[k] = b
	return nil
}
func (m *memStore) GetJSON(_ context.Context, k string, dst any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.kv[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, dst)
}
func (m *memStore) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kv, k)
	delete(m.hits, strings.TrimPrefix(k, "rl:"))
	return nil
}
func (m *memStore) Take(_ context.Context, k string, limit int, w time.Duration) (ratelimit.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits[k]++
	n := m.hits[k]
	return ratelimit.Result{Allowed: n <= limit, Limit: limit, Remaining: max(limit-n, 0), ResetIn: w}, nil
}

// captureSender keeps the last SMS so tests can read the code.
type captureSender struct{ last string }

func (c *captureSender) Send(_ context.Context, _, text string) error { c.last = text; return nil }
func (c *captureSender) code() string                                 { return c.last[len(c.last)-6:] }

func TestNormalizePhone(t *testing.T) {
	for _, in := range []string{"+998901234567", "998901234567", "901234567", "+998 90 123-45-67", "(90) 123 45 67", " 90-123-45-67 "} {
		if got, err := NormalizePhone(in); err != nil || got != "+998901234567" {
			t.Errorf("NormalizePhone(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "12345", "+7 901 234 56 78", "0901234567", "+998 090 123 45 6", "90123456a", "++998901234567", "+998901234567; drop"} {
		if _, err := NormalizePhone(in); !errors.Is(err, ErrInvalidPhone) {
			t.Errorf("NormalizePhone(%q) accepted", in)
		}
	}
	if m := MaskPhone("+998901234567"); m != "+998***45 67" || strings.Contains(m, "90123") {
		t.Errorf("mask = %q", m)
	}
}

func TestOTPHappyPathAndSingleUse(t *testing.T) {
	ctx := context.Background()
	sms := &captureSender{}
	o := NewOTP(newMemStore(), sms, "pep", "")
	if err := o.Send(ctx, "+998901234567", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	code := sms.code()
	if err := o.Verify(ctx, "+998901234567", code); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if err := o.Verify(ctx, "+998901234567", code); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("code reused: %v", err)
	}
}

func TestOTPLockoutAfterThreeWrong(t *testing.T) {
	ctx := context.Background()
	sms := &captureSender{}
	o := NewOTP(newMemStore(), sms, "pep", "")
	o.Send(ctx, "+998901234567", "1.1.1.1")
	good := sms.code()
	bad := "000000"
	if good == bad {
		bad = "111111"
	}
	for i := 1; i <= 2; i++ {
		if err := o.Verify(ctx, "+998901234567", bad); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("wrong #%d: %v", i, err)
		}
	}
	if err := o.Verify(ctx, "+998901234567", bad); !errors.Is(err, ErrOTPLocked) {
		t.Fatalf("third wrong must lock: %v", err)
	}
	if err := o.Verify(ctx, "+998901234567", good); !errors.Is(err, ErrOTPLocked) {
		t.Errorf("locked phone accepted the right code: %v", err)
	}
	if err := o.Send(ctx, "+998901234567", "1.1.1.1"); !errors.Is(err, ErrOTPLocked) {
		t.Errorf("locked phone can request a new code: %v", err)
	}
}

func TestOTPSendLimitPerPhoneAndIP(t *testing.T) {
	ctx := context.Background()
	o := NewOTP(newMemStore(), &captureSender{}, "pep", "")
	for i := 0; i < otpSendLimit; i++ {
		if err := o.Send(ctx, "+998901234567", "1.1.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Send(ctx, "+998901234567", "2.2.2.2"); !errors.Is(err, ErrOTPRateLimited) {
		t.Errorf("4th code for the same phone from another IP: %v", err)
	}
	// Many people behind one venue IP can each get a code, up to otpSendLimitIP.
	o = NewOTP(newMemStore(), &captureSender{}, "pep", "")
	for i := 0; i < otpSendLimitIP; i++ {
		if err := o.Send(ctx, fmt.Sprintf("+9989000%05d", i), "3.3.3.3"); err != nil {
			t.Fatalf("code %d from a shared IP: %v", i, err)
		}
	}
	if err := o.Send(ctx, "+998909999999", "3.3.3.3"); !errors.Is(err, ErrOTPRateLimited) {
		t.Errorf("code %d from the same IP: %v", otpSendLimitIP+1, err)
	}
}

func TestOTPDemoFixedCodeAndNoCodeInStore(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	o := NewOTP(st, FixedSender{}, "pep", "424242")
	o.Send(ctx, "+998901234567", "1.1.1.1")
	for k, v := range st.kv {
		if strings.Contains(string(v), "424242") || strings.Contains(k, "901234567") {
			t.Errorf("code or phone stored in clear text: %s = %s", k, v)
		}
	}
	if err := o.Verify(ctx, "+998901234567", "424242"); err != nil {
		t.Errorf("demo code rejected: %v", err)
	}
}

func TestDemoRouteIs404OutsideDemo(t *testing.T) {
	s := newTestService()
	mux := http.NewServeMux()
	NewHandler(s, s.jwt, CookieConfig{}).Register(mux) // no WithDemo
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/demo", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /auth/demo outside demo = %d, want 404", rec.Code)
	}
}

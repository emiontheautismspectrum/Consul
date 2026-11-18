// Package ratelimit is the application-level rate limiter backed by Redis.
//
// nginx limits requests per IP on one machine. This layer adds what nginx cannot see:
// limits per authenticated user (across IPs), per-route business limits, and counters
// shared by every API instance. It answers with X-RateLimit-* headers and Retry-After.
package ratelimit

import (
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// Result describes the state of a fixed window after counting one hit.
type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	ResetIn   time.Duration
}

type Limiter interface {
	Take(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// Rule limits requests matching it to Limit per Window, counted per Key(r).
type Rule struct {
	Name   string
	Limit  int
	Window time.Duration
	Key    func(*http.Request) string
}

// ClientIP is the client address as seen by nginx (X-Real-IP). The API is reachable only
// through nginx inside the Docker network, so the header cannot be forged by clients.
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func ByIP(r *http.Request) string { return "ip:" + ClientIP(r) }

// ByUserOrIP keys authenticated requests by user (so a user cannot dodge limits by
// changing IP) and anonymous ones by IP.
func ByUserOrIP(r *http.Request) string {
	if id := httpx.UserID(r.Context()); id != "" {
		return "u:" + id
	}
	return ByIP(r)
}

// Global applies rule to every request except the paths in skip (health checks).
func Global(l Limiter, rule Rule, skip ...string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range skip {
				if r.URL.Path == p {
					next.ServeHTTP(w, r)
					return
				}
			}
			if check(l, rule, w, r) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// Routes applies per-route rules keyed by "METHOD /path" (paths relative to the API version).
// Keeping every route limit in one table makes them easy to review.
func Routes(l Limiter, rules map[string]Rule) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rule, ok := rules[r.Method+" "+r.URL.Path]; ok && !check(l, rule, w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// check counts the request and writes 429 when over the limit. Redis failures fail open:
// an outage of the cache must not take the API down (nginx limits still apply).
func check(l Limiter, rule Rule, w http.ResponseWriter, r *http.Request) bool {
	res, err := l.Take(r.Context(), rule.Name+":"+rule.Key(r), rule.Limit, rule.Window)
	if err != nil {
		slog.WarnContext(r.Context(), "rate limiter unavailable, allowing request", "rule", rule.Name, "err", err)
		return true
	}
	reset := strconv.Itoa(int(math.Ceil(res.ResetIn.Seconds())))
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(res.Limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
	h.Set("X-RateLimit-Reset", reset)
	if !res.Allowed {
		h.Set("Retry-After", reset)
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited",
			"too many requests ("+rule.Name+"), retry in "+reset+"s")
		return false
	}
	return true
}

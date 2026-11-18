// Package cache wraps Redis: token blacklist, JSON cache and fixed-window rate limiting.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"finadvisor/internal/infrastructure/ratelimit"
)

type Redis struct{ c *redis.Client }

func New(ctx context.Context, addr, password string) (*Redis, error) {
	c := redis.NewClient(&redis.Options{Addr: addr, Password: password})
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return &Redis{c: c}, nil
}

func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }
func (r *Redis) Close() error                   { return r.c.Close() }

// Revoke puts a token id into the blacklist until the token would have expired anyway.
func (r *Redis) Revoke(ctx context.Context, jti string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	return r.c.Set(ctx, "bl:"+jti, 1, ttl).Err()
}

func (r *Redis) IsRevoked(ctx context.Context, jti string) (bool, error) {
	n, err := r.c.Exists(ctx, "bl:"+jti).Result()
	return n > 0, err
}

func (r *Redis) Delete(ctx context.Context, key string) error { return r.c.Del(ctx, key).Err() }

// GetJSON returns false when the key is absent.
func (r *Redis) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	b, err := r.c.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, dst)
}

func (r *Redis) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.c.Set(ctx, key, b, ttl).Err()
}

// Take counts one hit in a fixed window and reports the window state. INCR, PEXPIRE NX and
// PTTL run in one MULTI, so concurrent requests cannot leave a counter without a TTL.
func (r *Redis) Take(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error) {
	k := "rl:" + key
	pipe := r.c.TxPipeline()
	incr := pipe.Incr(ctx, k)
	pipe.Do(ctx, "PEXPIRE", k, window.Milliseconds(), "NX")
	ttl := pipe.PTTL(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return ratelimit.Result{}, err
	}
	n := int(incr.Val())
	reset := ttl.Val()
	if reset < 0 {
		reset = window
	}
	return ratelimit.Result{Allowed: n <= limit, Limit: limit, Remaining: max(limit-n, 0), ResetIn: reset}, nil
}

// Allow is Take for callers that only need the verdict (daily quotas).
func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	res, err := r.Take(ctx, key, limit, window)
	return res.Allowed, res.Remaining, err
}

// Failures returns the number of recorded failures for key and the time until they expire.
func (r *Redis) Failures(ctx context.Context, key string) (int, time.Duration, error) {
	pipe := r.c.Pipeline()
	get := pipe.Get(ctx, "fail:"+key)
	ttl := pipe.PTTL(ctx, "fail:"+key)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, err
	}
	n, _ := get.Int()
	return n, max(ttl.Val(), 0), nil
}

// AddFailure records a failure; the window starts at the first failure.
func (r *Redis) AddFailure(ctx context.Context, key string, window time.Duration) error {
	pipe := r.c.TxPipeline()
	pipe.Incr(ctx, "fail:"+key)
	pipe.Do(ctx, "PEXPIRE", "fail:"+key, window.Milliseconds(), "NX")
	_, err := pipe.Exec(ctx)
	return err
}

func (r *Redis) ResetFailures(ctx context.Context, key string) error {
	return r.c.Del(ctx, "fail:"+key).Err()
}

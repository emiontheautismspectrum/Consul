// Package cache wraps Redis: token blacklist, JSON cache and fixed-window rate limiting.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
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

// Allow increments a fixed-window counter and reports whether it is still within limit.
func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	pipe := r.c.TxPipeline()
	incr := pipe.Incr(ctx, "rl:"+key)
	pipe.ExpireNX(ctx, "rl:"+key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}
	n := int(incr.Val())
	return n <= limit, max(limit-n, 0), nil
}

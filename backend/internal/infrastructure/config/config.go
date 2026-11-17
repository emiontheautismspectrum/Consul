// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr        string
	Env         string
	DatabaseURL string // via PgBouncer (transaction mode)
	MigrateURL  string // direct to PostgreSQL, used only for migrations
	RedisAddr   string
	RedisPass   string

	Pepper           string
	JWTAccessSecret  string
	JWTRefreshSecret string
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	CookieSecure     bool
	CookieDomain     string

	AllowedOrigins []string

	AIBaseURL    string
	AIAPIKey     string
	AIModel      string
	AIMaxTokens  int
	AIDailyLimit int // generations per user per day
	AITimeout    time.Duration
}

func Load() (Config, error) {
	c := Config{
		Addr:             env("APP_ADDR", ":8080"),
		Env:              env("APP_ENV", "dev"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		MigrateURL:       os.Getenv("MIGRATE_DATABASE_URL"),
		RedisAddr:        env("REDIS_ADDR", "localhost:6379"),
		RedisPass:        os.Getenv("REDIS_PASSWORD"),
		Pepper:           os.Getenv("PEPPER"),
		JWTAccessSecret:  os.Getenv("JWT_ACCESS_SECRET"),
		JWTRefreshSecret: os.Getenv("JWT_REFRESH_SECRET"),
		AccessTTL:        envDuration("JWT_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:       envDuration("JWT_REFRESH_TTL", 14*24*time.Hour),
		CookieSecure:     envBool("COOKIE_SECURE", true),
		CookieDomain:     os.Getenv("COOKIE_DOMAIN"),
		AllowedOrigins:   splitCSV(env("ALLOWED_ORIGINS", "http://localhost:3000")),
		AIBaseURL:        strings.TrimRight(os.Getenv("BASE_URL"), "/"),
		AIAPIKey:         os.Getenv("AI_API_KEY"),
		AIModel:          env("AI_MODEL", "deepseek-chat"),
		AIMaxTokens:      envInt("AI_MAX_TOKENS", 16000), // reasoning models spend part of it on thinking
		AIDailyLimit:     envInt("AI_DAILY_LIMIT", 20),
		AITimeout:        envDuration("AI_TIMEOUT", 90*time.Second),
	}
	if c.MigrateURL == "" {
		c.MigrateURL = c.DatabaseURL
	}
	var missing []string
	for k, v := range map[string]string{
		"DATABASE_URL": c.DatabaseURL, "PEPPER": c.Pepper,
		"JWT_ACCESS_SECRET": c.JWTAccessSecret, "JWT_REFRESH_SECRET": c.JWTRefreshSecret,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

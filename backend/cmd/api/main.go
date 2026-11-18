// Command api is the FinAdvisor modular monolith: auth, calculator, advisor and user modules
// wired together through interfaces and served by the standard net/http ServeMux.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"finadvisor/api"
	"finadvisor/internal/advisor"
	"finadvisor/internal/auth"
	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/cache"
	"finadvisor/internal/infrastructure/config"
	"finadvisor/internal/infrastructure/db"
	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/infrastructure/llm"
	"finadvisor/internal/infrastructure/ratelimit"
	"finadvisor/internal/user"
	"finadvisor/internal/vision"
	"finadvisor/migrations"
)

func main() {
	// Distroless images have no curl, so the binary doubles as its own healthcheck probe.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		resp, err := http.Get("http://127.0.0.1" + envOr("APP_ADDR", ":8080") + httpx.APIv1 + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(ctx, cfg.MigrateURL, migrations.FS); err != nil {
		return err
	}
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	rdb, err := cache.New(ctx, cfg.RedisAddr, cfg.RedisPass)
	if err != nil {
		return err
	}
	defer rdb.Close()

	rules, err := calculator.LoadUZRules()
	if err != nil {
		return err
	}

	// Auth
	jwtIssuer := auth.NewJWTIssuer(cfg.JWTAccessSecret, cfg.JWTRefreshSecret, cfg.AccessTTL, cfg.RefreshTTL)
	authSvc := auth.NewService(auth.NewPGRepo(database), auth.NewHasher(cfg.Pepper), jwtIssuer, rdb).
		WithAttemptGuard(rdb) // per-account login lockout
	authH := auth.NewHandler(authSvc, jwtIssuer, auth.CookieConfig{Secure: cfg.CookieSecure, Domain: cfg.CookieDomain})

	// Calculator
	engine := calculator.NewService(rules, rdb)
	calcH := calculator.NewHandler(engine, calculator.NewPGRepo(database))

	// User
	userRepo := user.NewRepo(database)
	userH := user.NewHandler(userRepo)

	// Advisor: depends on calculator.Engine and the consent checker via interfaces only.
	llmClient := llm.NewHTTPClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel, cfg.AITimeout)
	agent := advisor.NewAgent(llmClient, engine, cfg.AIMaxTokens)
	advisorSvc := advisor.NewService(agent, cfg.AIModel, advisor.NewPGRepo(database), userRepo, rdb, cfg.AIDailyLimit)
	advisorH := advisor.NewHandler(advisorSvc)
	// Vision: same gateway, multimodal model; reuses consent and rate limiting.
	visionClient := llm.NewHTTPClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIVisionModel, cfg.AITimeout)
	visionH := vision.NewHandler(vision.NewService(visionClient, userRepo, rdb))
	if cfg.AIBaseURL == "" || cfg.AIAPIKey == "" {
		slog.Warn("BASE_URL / AI_API_KEY not set: /advisor/generate will return 503")
	}

	// All routes live under /api/v1: modules register relative paths on v1, which is
	// mounted once. A future v2 is another mux mounted next to it.
	v1 := http.NewServeMux()
	authH.Register(v1)
	calcH.Register(v1, authH.OptionalAuth, authH.RequireAuth)
	userH.Register(v1, authH.RequireAuth)
	advisorH.Register(v1, authH.RequireAuth)
	visionH.Register(v1, authH.RequireAuth)
	api.Register(v1)

	v1.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	v1.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := database.PingContext(c); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db_unavailable", err.Error())
			return
		}
		if err := rdb.Ping(c); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "redis_unavailable", err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not_found", "route not found")
	}
	v1.HandleFunc("/", notFound)

	// Application rate limits (Redis). nginx in front limits per IP; these add per-user and
	// per-route business limits shared by all API instances. Daily AI quotas live in the
	// advisor and vision services.
	const minute = time.Minute
	globalLimit := ratelimit.Rule{Name: "api", Limit: 120, Window: minute, Key: ratelimit.ByUserOrIP}
	routeLimits := map[string]ratelimit.Rule{
		"POST /auth/register":        {Name: "register", Limit: 10, Window: 60 * minute, Key: ratelimit.ByIP},
		"POST /auth/login":           {Name: "login", Limit: 20, Window: 10 * minute, Key: ratelimit.ByIP},
		"POST /auth/refresh":         {Name: "refresh", Limit: 30, Window: 10 * minute, Key: ratelimit.ByIP},
		"POST /advisor/generate":     {Name: "generate", Limit: 5, Window: 10 * minute, Key: ratelimit.ByUserOrIP},
		"POST " + vision.Route:       {Name: "photo", Limit: 10, Window: minute, Key: ratelimit.ByUserOrIP},
		"POST /calculator/tax":       {Name: "calc", Limit: 60, Window: minute, Key: ratelimit.ByUserOrIP},
		"POST /calculator/calculate": {Name: "calc", Limit: 60, Window: minute, Key: ratelimit.ByUserOrIP},
		"POST /calculator/breakeven": {Name: "calc", Limit: 60, Window: minute, Key: ratelimit.ByUserOrIP},
	}
	api1 := httpx.Chain(v1,
		authH.OptionalAuth, // identify the user (never rejects) so limits can key by user
		ratelimit.Global(rdb, globalLimit, "/healthz", "/readyz"),
		ratelimit.Routes(rdb, routeLimits))

	mux := http.NewServeMux()
	httpx.Mount(mux, httpx.APIv1, api1)
	mux.HandleFunc("/", notFound)

	handler := httpx.Chain(mux,
		httpx.WithRequestID, httpx.Recover, httpx.Logger,
		httpx.CORS(cfg.AllowedOrigins), httpx.BodyLimit(1<<20, httpx.APIv1+vision.Route))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,                 // photo uploads (nginx buffers them, this is a safety net)
		WriteTimeout:      cfg.AITimeout*3 + 30*time.Second, // AI generation makes several LLM round trips
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "env", cfg.Env)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/proxy"
	"ollama-gateway/internal/ratelimit"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	var opts config.LoaderOptions
	fs := flag.CommandLine
	config.RegisterFlags(fs, &opts)
	flag.Parse()

	cfg, err := config.LoadWithFlags(opts, "configs/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger.Info("configuration loaded", "listen_addr", cfg.Server.ListenAddr, "backends", len(cfg.Backends))

	// Phase 2: Auth layer setup
	authStore := auth.NewStore(cfg)
	if err := authStore.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "auth config error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("auth store initialized", "users", len(cfg.Users))

	// Phase 3: Rate limiting setup
	limiterStore := ratelimit.NewLimiterStore(cfg)
	rateLimitMw := ratelimit.NewMiddleware(limiterStore)
	logger.Info("rate limiter initialized",
		"default_rate", cfg.RateLimit.DefaultRate,
		"default_burst", cfg.RateLimit.DefaultBurst,
		"ttl", cfg.RateLimit.TTL)

	// Phase 4: Model registry & backend routing setup
	resolver, err := models.NewResolver(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model/backend error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("model resolver initialized",
		"models", len(resolver.Registry().AllModels()),
		"backends", len(resolver.Manager().Backends()))

	// Phase 5: Reverse proxy handler setup.
	proxyHandler := proxy.NewProxyHandler(resolver)

	// Start health checker in background.
	ctx := context.Background()
	go func() {
		resolver.Manager().HealthChecker().Run(ctx)
	}()
	logger.Info("health checker started",
		"interval_seconds", cfg.HealthCheck.IntervalSeconds,
		"timeout_seconds", cfg.HealthCheck.TimeoutSeconds)

	// Build the HTTP server with auth + rate limit middleware applied to /api/* routes.
	mux := http.NewServeMux()
	apiRouter := http.NewServeMux()
	apiRouter.Handle("/", proxyHandler)

	mux.Handle("/api/", authStore.Middleware(rateLimitMw.Handler(apiRouter)))

	srv := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	logger.Info("server starting", "listen_addr", cfg.Server.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

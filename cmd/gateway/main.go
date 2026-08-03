package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/dashboard"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/proxy"
	"ollama-gateway/internal/ratelimit"
	"ollama-gateway/internal/usage"

	_ "github.com/mattn/go-sqlite3" // SQLite driver for usage tracking database
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

	activeCatalog := models.CatalogFromConfig(cfg)

	// Phase 4: Usage tracking setup and DB-backed model catalog storage
	var (
		usageLogger *usage.UsageLogger
		dbStore     *usage.Store
		modelStore  *models.Store
	)
	if cfg.Database.Path != "" {
		dbStore, err = usage.NewStore(cfg.Database.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "database error: %v\n", err)
			os.Exit(1)
		}
		defer dbStore.Close()

		modelStore = models.NewStore(dbStore.DB())

		discoveryCtx, cancelDiscovery := context.WithTimeout(context.Background(), 20*time.Second)
		discoveredCatalog, discoverErr := models.DiscoverCatalogFromBackends(discoveryCtx, cfg)
		cancelDiscovery()

		if discoverErr != nil {
			logger.Warn("model discovery had issues", "error", discoverErr)
		}

		if discoverErr == nil || len(discoveredCatalog) > 0 {
			if err := modelStore.SyncDiscoveredCatalog(discoveredCatalog); err != nil {
				logger.Warn("model sync failed; continuing with cached database state", "error", err)
			} else {
				logger.Info("model sync completed", "discovered_models", len(discoveredCatalog))
			}
		}

		dbCatalog, err := modelStore.LoadActiveCatalog()
		if err != nil {
			logger.Warn("failed to load active model catalog from database; using config fallback", "error", err)
		} else if len(dbCatalog) == 0 {
			logger.Warn("database model catalog is empty; using config fallback")
		} else {
			activeCatalog = dbCatalog
		}

		pricingCfg := &usage.PricingConfig{
			DefaultInputPer1M:  cfg.Pricing.DefaultInputPer1M,
			DefaultOutputPer1M: cfg.Pricing.DefaultOutputPer1M,
			ModelPricing:       make(map[string]usage.ModelPricing),
		}
		for modelName, mp := range cfg.Pricing.Models {
			pricingCfg.ModelPricing[modelName] = usage.ModelPricing{
				InputCostPer1M:  mp.InputCostPer1M,
				OutputCostPer1M: mp.OutputCostPer1M,
			}
		}

		usageLogger = usage.NewUsageLogger(dbStore, usage.DefaultLoggerOptions())
		logger.Info("usage logger initialized", "database", cfg.Database.Path)
	} else {
		logger.Warn("no database path configured; usage tracking disabled")
	}

	// Phase 5: Model registry & backend routing setup
	resolver, err := models.NewResolverWithCatalog(cfg, activeCatalog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model/backend error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("model resolver initialized",
		"models", len(resolver.Registry().AllModels()),
		"backends", len(resolver.Manager().Backends()))

	// Phase 5: Reverse proxy handler setup.
	proxyHandler := proxy.NewProxyHandler(resolver, usageLogger, authStore)
	pricingCfg := &usage.PricingConfig{
		DefaultInputPer1M:  cfg.Pricing.DefaultInputPer1M,
		DefaultOutputPer1M: cfg.Pricing.DefaultOutputPer1M,
		ModelPricing:       make(map[string]usage.ModelPricing),
	}
	for modelName, mp := range cfg.Pricing.Models {
		pricingCfg.ModelPricing[modelName] = usage.ModelPricing{
			InputCostPer1M:  mp.InputCostPer1M,
			OutputCostPer1M: mp.OutputCostPer1M,
		}
	}
	proxyHandler.SetPricingConfig(pricingCfg)

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

	dashboardHandler, err := dashboard.NewHandler(cfg, authStore, dbStore, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard init error: %v\n", err)
		os.Exit(1)
	}
	dashboardHandler.SetManager(resolver.Manager())
	dashboardHandler.SetModelCatalog(activeCatalog)

	mux.Handle("/api/", authStore.Middleware(rateLimitMw.Handler(apiRouter)))
	mux.Handle("/admin/", dashboardHandler)

	srv := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	logger.Info("server starting", "listen_addr", cfg.Server.ListenAddr)

	// Set up graceful shutdown: on SIGTERM/SIGINT, stop accepting connections,
	// wait for active requests to complete (30s), then flush usage logger.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		logger.Info("received signal; initiating graceful shutdown", "signal", sig.String())

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful server shutdown failed", "error", err)
		}

		if usageLogger != nil {
			done := make(chan struct{})
			go func() {
				usageLogger.Shutdown(done) // wait for it to be ready
				close(done)
			}()
			select {
			case <-done:
				logger.Info("usage logger flushed and stopped")
			case <-shutdownCtx.Done():
				logger.Warn("usage logger shutdown timed out; some records may be lost", "error", shutdownCtx.Err())
			}
		}

		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

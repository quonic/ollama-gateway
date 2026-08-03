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

	activeCatalog := map[string]config.ModelEntry{}

	// Phase 2: Usage tracking setup and DB-backed model catalog storage
	var (
		usageLogger *usage.UsageLogger
		dbStore     *usage.Store
		modelStore  *models.Store
		authStore   *auth.Store
		userCount   int
	)
	if cfg.Database.Path != "" {
		dbStore, err = usage.NewStore(cfg.Database.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "database error: %v\n", err)
			os.Exit(1)
		}
		defer dbStore.Close()

		authStore = auth.NewStore(cfg, dbStore.DB())
		if err := authStore.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "auth config error: %v\n", err)
			os.Exit(1)
		}
		if users, listErr := authStore.ListUsers(); listErr == nil {
			userCount = len(users)
		} else {
			userCount = len(cfg.Users)
		}
		logger.Info("auth store initialized", "users", userCount)

		modelStore = models.NewStore(dbStore.DB())
		syncStats := models.SyncStats{}
		discoveryFailures := 0
		dbCatalog, err := modelStore.LoadActiveCatalog()
		if err != nil {
			logger.Warn("failed to load active model catalog from database before sync", "error", err)
		} else {
			activeCatalog = dbCatalog
		}

		if opts.SeedModelCatalog && len(activeCatalog) == 0 && len(cfg.Models.Models) > 0 {
			seedCatalog := models.NormalizeCatalog(models.CatalogFromConfig(cfg))
			seedStats, seedErr := modelStore.SyncDiscoveredCatalog(seedCatalog)
			if seedErr != nil {
				logger.Warn("model seed from YAML failed", "error", seedErr)
			} else {
				logger.Info("model catalog seeded from YAML",
					"added", seedStats.Added,
					"updated", seedStats.Updated,
					"deactivated", seedStats.Deactivated,
				)
				if reloaded, reloadErr := modelStore.LoadActiveCatalog(); reloadErr == nil {
					activeCatalog = reloaded
				}
			}
		}

		discoveryCtx, cancelDiscovery := context.WithTimeout(context.Background(), 20*time.Second)
		discoveredCatalog, discoveryStats, discoverErr := models.DiscoverCatalogFromBackendsWithStats(discoveryCtx, cfg)
		cancelDiscovery()
		discoveryFailures = discoveryStats.FailedBackends

		if discoverErr != nil {
			logger.Warn("model discovery had issues", "error", discoverErr)
		}

		if discoverErr == nil || len(discoveredCatalog) > 0 {
			syncedStats, syncErr := modelStore.SyncDiscoveredCatalog(discoveredCatalog)
			if syncErr != nil {
				logger.Warn("model sync failed; continuing with cached database state", "error", syncErr)
			} else {
				syncStats = syncedStats
			}
		}

		reloadedCatalog, reloadErr := modelStore.LoadActiveCatalog()
		if reloadErr != nil {
			logger.Warn("failed to reload active model catalog from database after sync", "error", reloadErr)
		} else {
			activeCatalog = reloadedCatalog
		}

		if len(activeCatalog) == 0 {
			fmt.Fprintf(os.Stderr, "model catalog error: no active models found in database after discovery/sync\n")
			os.Exit(1)
		}

		logger.Info("model startup sync metrics",
			"added", syncStats.Added,
			"updated", syncStats.Updated,
			"deactivated", syncStats.Deactivated,
			"discovery_failures", discoveryFailures,
			"total_active_models_loaded", len(activeCatalog),
		)

		dbPricing, err := modelStore.LoadModelPricing()
		if err != nil {
			logger.Warn("failed to load model pricing from database; using config pricing", "error", err)
		} else if len(dbPricing) == 0 {
			if len(cfg.Pricing.Models) > 0 {
				if err := modelStore.ReplaceModelPricing(cfg.Pricing.Models); err != nil {
					logger.Warn("failed to seed model pricing table from config", "error", err)
				} else {
					logger.Info("seeded model pricing table from config", "models", len(cfg.Pricing.Models))
				}
			}
		} else {
			cfg.Pricing.Models = dbPricing
			logger.Info("loaded model pricing from database", "models", len(dbPricing))
		}

		usageLogger = usage.NewUsageLogger(dbStore, usage.DefaultLoggerOptions())
		logger.Info("usage logger initialized", "database", cfg.Database.Path)
	} else {
		fmt.Fprintf(os.Stderr, "config error: database.path is required for DB-backed model catalog runtime\n")
		os.Exit(1)
	}

	// Phase 3: Rate limiting setup
	limiterStore := ratelimit.NewLimiterStore(cfg, authStore)
	rateLimitMw := ratelimit.NewMiddleware(limiterStore)
	logger.Info("rate limiter initialized",
		"default_rate", cfg.RateLimit.DefaultRate,
		"default_burst", cfg.RateLimit.DefaultBurst,
		"ttl", cfg.RateLimit.TTL)

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
	pricingCfg := usagePricingFromConfig(cfg.Pricing)
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
	dashboardHandler.SetModelRuntimeRefreshers(
		resolver.RefreshCatalog,
		proxyHandler.SetPricingConfig,
	)

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

func usagePricingFromConfig(pricing config.PricingConfig) *usage.PricingConfig {
	out := &usage.PricingConfig{
		DefaultInputPer1M:  pricing.DefaultInputPer1M,
		DefaultOutputPer1M: pricing.DefaultOutputPer1M,
		ModelPricing:       make(map[string]usage.ModelPricing, len(pricing.Models)),
	}
	for modelName, mp := range pricing.Models {
		out.ModelPricing[modelName] = usage.ModelPricing{
			InputCostPer1M:  mp.InputCostPer1M,
			OutputCostPer1M: mp.OutputCostPer1M,
		}
	}
	return out
}

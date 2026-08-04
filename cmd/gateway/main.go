package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ollama-gateway/internal/auth"
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
	"ollama-gateway/internal/dashboard"
	"ollama-gateway/internal/models"
	"ollama-gateway/internal/proxy"
	"ollama-gateway/internal/ratelimit"
	"ollama-gateway/internal/tlsruntime"
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

	cfgPath, err := config.ResolveConfigPath(opts, config.DefaultConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger.Info("configuration loaded", "listen_addr", cfg.Server.ListenAddr, "backends", len(cfg.Backends))

	activeCatalog := map[string]config.ModelEntry{}

	// Phase 2: Usage tracking setup and DB-backed model catalog storage
	var (
		usageLogger  *usage.UsageLogger
		dbStore      *usage.Store
		backendStore *backends.Store
		modelStore   *models.Store
		authStore    *auth.Store
		userCount    int
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

		backendStore = backends.NewStore(dbStore.DB())
		if err := backendStore.SeedBackends(cfg.Backends); err != nil {
			logger.Warn("backend seed from config failed", "error", err)
		}
		dbBackends, err := backendStore.LoadActiveBackends()
		if err != nil {
			logger.Warn("failed to load active backends from database; using config backends", "error", err)
		} else if len(dbBackends) > 0 {
			cfg.Backends = dbBackends
			logger.Info("loaded active backends from database", "backends", len(dbBackends))
		}

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
		"backend", cfg.RateLimit.Backend,
		"default_rate", cfg.RateLimit.DefaultRate,
		"default_burst", cfg.RateLimit.DefaultBurst,
		"ttl", cfg.RateLimit.TTL,
		"redis_addr", cfg.RateLimit.RedisAddr,
		"redis_fallback_to_local", cfg.RateLimit.RedisFallbackToLocal)

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
	ctx, stopBackground := context.WithCancel(context.Background())
	go func() {
		resolver.Manager().HealthChecker().Run(ctx)
	}()
	logger.Info("health checker started",
		"interval_seconds", cfg.HealthCheck.IntervalSeconds,
		"timeout_seconds", cfg.HealthCheck.TimeoutSeconds)

	tlsEnabled := cfg.Server.TLSCertPath != "" && cfg.Server.TLSKeyPath != ""
	var certManager *tlsruntime.Manager
	var certProvider *tlsruntime.CertificateProvider
	if tlsEnabled {
		certManager = tlsruntime.NewManager(
			cfg.Server.TLSCertPath,
			cfg.Server.TLSKeyPath,
			cfg.Server.TLSCheckInterval,
			cfg.Server.TLSExpiryWarningDays,
			logger,
		)
		if err := certManager.LoadInitial(); err != nil {
			fmt.Fprintf(os.Stderr, "TLS certificate error: %v\n", err)
			os.Exit(1)
		}
		certProvider = tlsruntime.NewCertificateProvider(logger)
		certProvider.SetManager(certManager)
		certProvider.Attach(ctx)
		logger.Info("TLS certificate watcher started",
			"cert_path", cfg.Server.TLSCertPath,
			"check_interval", cfg.Server.TLSCheckInterval,
			"expiry_warning_days", cfg.Server.TLSExpiryWarningDays,
		)
	}

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
	dashboardHandler.SetTLSManager(certManager)
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
	if tlsEnabled {
		srv.TLSConfig = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certProvider.GetCertificate,
		}
	}

	reloader := config.NewReloader(
		cfgPath,
		cfg,
		logger,
		config.DefaultRuntimePolicy(),
		config.RuntimeApplier{
			ApplyAdminTokenHash: func(tokenHash string) {
				authStore.ApplyAdminTokenHash(tokenHash)
			},
			ApplyBackends: func(_ context.Context, next []config.Backend) error {
				return applyReloadBackends(resolver.Manager(), backendStore, logger, next)
			},
			ApplyTLSPaths: func(_ context.Context, certPath, keyPath string, checkInterval time.Duration, warningDays int) error {
				if !tlsEnabled {
					return nil
				}
				if certProvider == nil {
					return fmt.Errorf("TLS certificate provider is not initialized")
				}
				if err := certProvider.UpdatePaths(certPath, keyPath, checkInterval, warningDays); err != nil {
					return err
				}
				return nil
			},
		},
	)
	dashboardHandler.SetReloadStatusProvider(reloader.Status)

	go func() {
		if err := reloader.Run(ctx); err != nil && !config.IsExpectedStop(err) {
			status := reloader.Status()
			logger.Error(
				"config watcher stopped",
				"error", err,
				"source_trigger", status.LastTrigger,
				"last_reload_at", status.LastReloadAt.Format(time.RFC3339),
				"last_error", status.LastError,
			)
		}
	}()

	logger.Info("server starting", "listen_addr", cfg.Server.ListenAddr, "tls_enabled", tlsEnabled)

	// Set up graceful shutdown: on SIGTERM/SIGINT, stop accepting connections,
	// wait for active requests to complete (30s), then flush usage logger.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	go func() {
		for {
			sig := <-sigCh
			if sig == syscall.SIGHUP {
				logger.Info("received signal; initiating config reload", "signal", sig.String())
				reloadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := reloader.TriggerReload(reloadCtx, "sighup")
				cancel()
				status := reloader.Status()
				if err != nil {
					logger.Error(
						"config reload failed",
						"error", err,
						"source_trigger", status.LastTrigger,
						"last_reload_at", status.LastReloadAt.Format(time.RFC3339),
						"last_error", status.LastError,
					)
				} else {
					logger.Info(
						"config reload completed",
						"source_trigger", status.LastTrigger,
						"last_reload_at", status.LastReloadAt.Format(time.RFC3339),
						"last_error", status.LastError,
					)
				}
				continue
			}

			logger.Info("received signal; initiating graceful shutdown", "signal", sig.String())
			stopBackground()
			if certProvider != nil {
				certProvider.Close()
			}

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
		}
	}()

	if tlsEnabled {
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

func applyReloadBackends(manager *backends.Manager, backendStore *backends.Store, logger *slog.Logger, next []config.Backend) error {
	if backendStore != nil {
		hasRows, err := backendStore.HasAnyBackends()
		if err != nil {
			return err
		}
		if hasRows {
			logger.Info("skipping YAML backend hot-reload because database state is authoritative")
			return nil
		}
	}

	target := make(map[string]config.Backend, len(next))
	for _, b := range next {
		target[b.Name] = b
		if err := manager.UpsertBackend(b); err != nil {
			return err
		}
	}

	for _, existing := range manager.Backends() {
		if _, ok := target[existing.Name]; ok {
			continue
		}
		if err := manager.RemoveBackend(existing.Name); err != nil {
			return err
		}
	}

	return nil
}

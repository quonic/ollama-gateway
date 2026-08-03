package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// RuntimeApplier applies runtime-safe config fragments to active subsystems.
type RuntimeApplier struct {
	ApplyConfigSnapshot func(cfg *Config)
	ApplyAdminTokenHash func(tokenHash string)
	ApplyBackends       func(ctx context.Context, backends []Backend) error
	ApplyTLSPaths       func(ctx context.Context, certPath, keyPath string, checkInterval time.Duration, warningDays int) error
}

// Reloader provides fail-safe config reload from disk.
type Reloader struct {
	path string

	logger *slog.Logger
	policy RuntimePolicy
	apply  RuntimeApplier

	watchInterval time.Duration
	debounce      time.Duration

	mu      sync.Mutex
	current *Config

	hasHash      bool
	lastHash     [sha256.Size]byte
	pending      bool
	pendingHash  [sha256.Size]byte
	pendingSince time.Time

	hasAttemptedHash bool
	attemptedHash    [sha256.Size]byte
}

// NewReloader creates a runtime config reloader.
func NewReloader(path string, current *Config, logger *slog.Logger, policy RuntimePolicy, apply RuntimeApplier) *Reloader {
	if logger == nil {
		logger = slog.Default()
	}
	if policy == (RuntimePolicy{}) {
		policy = DefaultRuntimePolicy()
	}
	return &Reloader{
		path:          path,
		current:       current,
		logger:        logger,
		policy:        policy,
		apply:         apply,
		watchInterval: 1 * time.Second,
		debounce:      300 * time.Millisecond,
	}
}

// TriggerReload runs a one-off reload, used by SIGHUP.
func (r *Reloader) TriggerReload(ctx context.Context, reason string) error {
	return r.reload(ctx, reason)
}

// Run polls the config file and reloads when its content changes.
func (r *Reloader) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.watchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.pollAndMaybeReload(ctx); err != nil {
				r.logger.Error("config reload poll failed", "error", err)
			}
		}
	}
}

func (r *Reloader) pollAndMaybeReload(ctx context.Context) error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	h := sha256.Sum256(data)
	now := time.Now()

	r.mu.Lock()
	if !r.hasHash {
		r.hasHash = true
		r.lastHash = h
		r.mu.Unlock()
		return nil
	}

	if h != r.lastHash {
		r.lastHash = h
		r.pending = true
		r.pendingHash = h
		r.pendingSince = now
	}

	ready := r.pending && now.Sub(r.pendingSince) >= r.debounce
	alreadyAttempted := r.hasAttemptedHash && r.attemptedHash == r.pendingHash
	if !ready || alreadyAttempted {
		r.mu.Unlock()
		return nil
	}

	targetHash := r.pendingHash
	r.mu.Unlock()

	err = r.reload(ctx, "watcher")

	r.mu.Lock()
	r.hasAttemptedHash = true
	r.attemptedHash = targetHash
	r.pending = false
	r.mu.Unlock()

	if err != nil {
		return err
	}
	return nil
}

func (r *Reloader) reload(ctx context.Context, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next, err := Load(r.path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := r.policy.ValidateRuntimeChange(r.current, next); err != nil {
		return err
	}

	if r.apply.ApplyBackends != nil {
		if err := r.apply.ApplyBackends(ctx, next.Backends); err != nil {
			return fmt.Errorf("apply backends: %w", err)
		}
	}
	if r.apply.ApplyTLSPaths != nil {
		if err := r.apply.ApplyTLSPaths(
			ctx,
			next.Server.TLSCertPath,
			next.Server.TLSKeyPath,
			next.Server.TLSCheckInterval,
			next.Server.TLSExpiryWarningDays,
		); err != nil {
			return fmt.Errorf("apply TLS paths: %w", err)
		}
	}
	if r.apply.ApplyAdminTokenHash != nil {
		r.apply.ApplyAdminTokenHash(next.Admin.TokenHash)
	}
	if r.apply.ApplyConfigSnapshot != nil {
		r.apply.ApplyConfigSnapshot(next)
	}

	r.current = next
	r.logger.Info("config reload applied", "reason", reason)
	return nil
}

// Current returns the active config snapshot.
func (r *Reloader) Current() *Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// IsExpectedStop reports whether Run returned due to context cancellation.
func IsExpectedStop(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

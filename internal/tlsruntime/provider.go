package tlsruntime

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// CertificateProvider supports runtime swapping of TLS managers.
type CertificateProvider struct {
	mu sync.RWMutex

	logger *slog.Logger
	root   context.Context

	manager *Manager
	cancel  context.CancelFunc
}

func NewCertificateProvider(logger *slog.Logger) *CertificateProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &CertificateProvider{logger: logger}
}

// Attach binds provider-managed manager goroutines to a root lifecycle context.
func (p *CertificateProvider) Attach(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.root = ctx
	p.restartLocked()
}

// SetManager swaps to a new manager and starts its watcher loop.
func (p *CertificateProvider) SetManager(mgr *Manager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manager = mgr
	p.restartLocked()
}

// UpdatePaths builds and activates a manager for a new certificate pair.
func (p *CertificateProvider) UpdatePaths(certPath, keyPath string, checkInterval time.Duration, warningDays int) error {
	p.mu.RLock()
	logger := p.logger
	p.mu.RUnlock()

	mgr := NewManager(certPath, keyPath, checkInterval, warningDays, logger)
	if err := mgr.LoadInitial(); err != nil {
		return err
	}
	p.SetManager(mgr)
	return nil
}

func (p *CertificateProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.RLock()
	mgr := p.manager
	p.mu.RUnlock()
	if mgr == nil {
		return nil, errors.New("TLS certificate manager is not configured")
	}
	return mgr.GetCertificate(hello)
}

func (p *CertificateProvider) Status() (Status, bool) {
	p.mu.RLock()
	mgr := p.manager
	p.mu.RUnlock()
	if mgr == nil {
		return Status{}, false
	}
	return mgr.Status(), true
}

func (p *CertificateProvider) Close() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()
}

func (p *CertificateProvider) restartLocked() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.root == nil || p.manager == nil {
		return
	}
	ctx, cancel := context.WithCancel(p.root)
	p.cancel = cancel
	mgr := p.manager
	go mgr.Run(ctx)
}

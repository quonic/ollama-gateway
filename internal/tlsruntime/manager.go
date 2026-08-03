package tlsruntime

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

const (
	DefaultCheckInterval    = 24 * time.Hour
	DefaultExpiryWarningDay = 30
)

// Manager holds the active server certificate and reloads it when files change.
type Manager struct {
	certPath      string
	keyPath       string
	checkInterval time.Duration
	warningDays   int
	logger        *slog.Logger

	mu             sync.RWMutex
	cert           *tls.Certificate
	fingerprint    [sha256.Size]byte
	hasFingerprint bool
}

func NewManager(certPath, keyPath string, checkInterval time.Duration, warningDays int, logger *slog.Logger) *Manager {
	if checkInterval <= 0 {
		checkInterval = DefaultCheckInterval
	}
	if warningDays <= 0 {
		warningDays = DefaultExpiryWarningDay
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		certPath:      certPath,
		keyPath:       keyPath,
		checkInterval: checkInterval,
		warningDays:   warningDays,
		logger:        logger,
	}
}

func (m *Manager) LoadInitial() error {
	return m.reloadIfChanged(true)
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.reloadIfChanged(false); err != nil {
				m.logger.Error("TLS certificate check failed", "error", err)
			}
			m.logExpiryStatus()
		}
	}
}

func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil, errors.New("TLS certificate is not loaded")
	}
	return m.cert, nil
}

func (m *Manager) reloadIfChanged(force bool) error {
	certPEM, err := os.ReadFile(m.certPath)
	if err != nil {
		return fmt.Errorf("read cert file: %w", err)
	}
	keyPEM, err := os.ReadFile(m.keyPath)
	if err != nil {
		return fmt.Errorf("read key file: %w", err)
	}

	fingerprint := sha256.Sum256(append(certPEM, keyPEM...))
	m.mu.RLock()
	unchanged := !force && m.hasFingerprint && m.fingerprint == fingerprint
	m.mu.RUnlock()
	if unchanged {
		return nil
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse certificate/key pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return errors.New("loaded certificate does not contain leaf data")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse leaf certificate: %w", err)
	}
	pair.Leaf = leaf

	m.mu.Lock()
	hadCert := m.cert != nil
	m.cert = &pair
	m.fingerprint = fingerprint
	m.hasFingerprint = true
	m.mu.Unlock()

	days := daysUntilExpiry(leaf.NotAfter)
	if hadCert {
		m.logger.Info("TLS certificate reloaded", "expires_at", leaf.NotAfter, "days_remaining", days)
	} else {
		m.logger.Info("TLS certificate loaded", "expires_at", leaf.NotAfter, "days_remaining", days)
	}
	m.logExpiryForLeaf(leaf)
	return nil
}

func (m *Manager) logExpiryStatus() {
	m.mu.RLock()
	cert := m.cert
	m.mu.RUnlock()
	if cert == nil || cert.Leaf == nil {
		return
	}
	m.logExpiryForLeaf(cert.Leaf)
}

func (m *Manager) logExpiryForLeaf(leaf *x509.Certificate) {
	days := daysUntilExpiry(leaf.NotAfter)
	if time.Now().After(leaf.NotAfter) {
		m.logger.Error("TLS certificate has expired", "expires_at", leaf.NotAfter, "days_remaining", days)
		return
	}
	if days <= m.warningDays {
		m.logger.Warn("TLS certificate expires soon", "expires_at", leaf.NotAfter, "days_remaining", days)
	}
}

func daysUntilExpiry(notAfter time.Time) int {
	return int(time.Until(notAfter).Hours() / 24)
}

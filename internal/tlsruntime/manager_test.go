package tlsruntime

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagerLoadAndReloadCertificate(t *testing.T) {
	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "cert.pem")
	keyPath := filepath.Join(tempDir, "key.pem")

	cert1, key1 := generateCertPairPEM(t, time.Now().Add(45*24*time.Hour), big.NewInt(1))
	writeTLSFiles(t, certPath, keyPath, cert1, key1)

	mgr := NewManager(certPath, keyPath, time.Hour, 30, nil)
	if err := mgr.LoadInitial(); err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}

	firstCert, err := mgr.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate() error = %v", err)
	}
	firstLeaf, err := x509.ParseCertificate(firstCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse first cert: %v", err)
	}

	cert2, key2 := generateCertPairPEM(t, time.Now().Add(90*24*time.Hour), big.NewInt(2))
	writeTLSFiles(t, certPath, keyPath, cert2, key2)

	if err := mgr.reloadIfChanged(false); err != nil {
		t.Fatalf("reloadIfChanged(false) error = %v", err)
	}

	secondCert, err := mgr.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate() after reload error = %v", err)
	}
	secondLeaf, err := x509.ParseCertificate(secondCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse second cert: %v", err)
	}

	if firstLeaf.SerialNumber.Cmp(secondLeaf.SerialNumber) == 0 {
		t.Fatalf("expected certificate serial to change after reload")
	}
}

func TestManagerLogsExpiryWarning(t *testing.T) {
	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "cert.pem")
	keyPath := filepath.Join(tempDir, "key.pem")

	cert, key := generateCertPairPEM(t, time.Now().Add(12*time.Hour), big.NewInt(11))
	writeTLSFiles(t, certPath, keyPath, cert, key)

	h := &captureHandler{}
	logger := slog.New(h)

	mgr := NewManager(certPath, keyPath, time.Hour, 30, logger)
	if err := mgr.LoadInitial(); err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}

	if !h.contains("TLS certificate expires soon") {
		t.Fatalf("expected expiry warning log, logs=%v", h.messages())
	}
}

func TestManagerRunStopsOnContextCancel(t *testing.T) {
	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "cert.pem")
	keyPath := filepath.Join(tempDir, "key.pem")

	cert, key := generateCertPairPEM(t, time.Now().Add(30*24*time.Hour), big.NewInt(21))
	writeTLSFiles(t, certPath, keyPath, cert, key)

	mgr := NewManager(certPath, keyPath, 5*time.Millisecond, 30, nil)
	if err := mgr.LoadInitial(); err != nil {
		t.Fatalf("LoadInitial() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Run() did not stop after context cancel")
	}
}

func generateCertPairPEM(t *testing.T, notAfter time.Time, serial *big.Int) ([]byte, []byte) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames: []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM
}

func writeTLSFiles(t *testing.T, certPath, keyPath string, certPEM, keyPEM []byte) {
	t.Helper()
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert file: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
}

type captureHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec.Message)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func (h *captureHandler) contains(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, msg := range h.records {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

func (h *captureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.records))
	copy(out, h.records)
	return out
}

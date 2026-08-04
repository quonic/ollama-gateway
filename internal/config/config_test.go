package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyDefaultsSetsTLSDefaults(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Server.TLSCheckInterval = 0
	cfg.Server.TLSExpiryWarningDays = 0

	applyDefaults(cfg)

	if cfg.Server.TLSCheckInterval != defaultTLSCheckInterval {
		t.Fatalf("expected TLS check interval default %v, got %v", defaultTLSCheckInterval, cfg.Server.TLSCheckInterval)
	}
	if cfg.Server.TLSExpiryWarningDays != defaultTLSExpiryWarningDays {
		t.Fatalf("expected TLS expiry warning days default %d, got %d", defaultTLSExpiryWarningDays, cfg.Server.TLSExpiryWarningDays)
	}
}

func TestValidateTLSPathsRequirePair(t *testing.T) {
	cfg := validConfigForTest()
	applyDefaults(cfg)
	cfg.Server.TLSCertPath = "/tmp/cert.pem"
	cfg.Server.TLSKeyPath = ""

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error when only tls_cert_path is set")
	}
}

func TestValidateTLSPathsMustBeReadable(t *testing.T) {
	cfg := validConfigForTest()
	applyDefaults(cfg)
	cfg.Server.TLSCertPath = "/no/such/cert.pem"
	cfg.Server.TLSKeyPath = "/no/such/key.pem"

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error for unreadable TLS files")
	}
}

func TestValidateTLSPassesWhenReadableFilesExist(t *testing.T) {
	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "cert.pem")
	keyPath := filepath.Join(tempDir, "key.pem")

	cfg := validConfigForTest()
	applyDefaults(cfg)
	cfg.Server.TLSCertPath = certPath
	cfg.Server.TLSKeyPath = keyPath

	writeFileForTest(t, certPath)
	writeFileForTest(t, keyPath)

	if err := validate(cfg); err != nil {
		t.Fatalf("expected TLS paths to validate, got error: %v", err)
	}
}

func TestApplyDefaultsSetsDatabasePath(t *testing.T) {
	cfg := validConfigForTest()
	cfg.Database.Path = ""

	applyDefaults(cfg)

	if cfg.Database.Path != defaultDatabasePath {
		t.Fatalf("expected database path default %q, got %q", defaultDatabasePath, cfg.Database.Path)
	}
}

func validConfigForTest() *Config {
	return &Config{
		Server: ServerConfig{
			ListenAddr:   "127.0.0.1:4080",
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			IdleTimeout:  time.Second,
		},
		Backends: []Backend{{Name: "local", URL: "http://127.0.0.1:11434", Weight: 1, Timeout: time.Second, HealthCheckPath: "/api/version"}},
		Models: ModelCatalog{Models: map[string]ModelEntry{
			"llama3": {Name: "llama3", Backends: []ModelBackendRef{{Backend: "local", Weight: 1}}},
		}},
		Users: map[string]UserConfig{
			"demo": {APIKeyHash: "hash"},
		},
	}
}

func writeFileForTest(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

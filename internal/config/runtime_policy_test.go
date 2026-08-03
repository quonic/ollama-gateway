package config

import (
	"testing"
	"time"
)

func TestRuntimePolicyRejectsImmutableChanges(t *testing.T) {
	current := &Config{
		Server: ServerConfig{
			ListenAddr:           "127.0.0.1:4080",
			ReadTimeout:          30 * time.Second,
			WriteTimeout:         120 * time.Second,
			IdleTimeout:          120 * time.Second,
			TLSCheckInterval:     24 * time.Hour,
			TLSExpiryWarningDays: 30,
		},
		Database: DatabaseConfig{Path: "gateway.db"},
		HealthCheck: HealthCheckConfig{
			IntervalSeconds:    10,
			TimeoutSeconds:     5,
			UnhealthyThreshold: 3,
		},
	}
	next := *current
	next.Server.ListenAddr = "127.0.0.1:5090"

	policy := DefaultRuntimePolicy()
	if err := policy.ValidateRuntimeChange(current, &next); err == nil {
		t.Fatalf("expected immutable field change to be rejected")
	}
}

func TestRuntimePolicyRejectsTLSEnableDisable(t *testing.T) {
	current := &Config{Server: ServerConfig{TLSCertPath: "", TLSKeyPath: ""}}
	next := &Config{Server: ServerConfig{TLSCertPath: "cert.pem", TLSKeyPath: "key.pem"}}

	policy := DefaultRuntimePolicy()
	if err := policy.ValidateRuntimeChange(current, next); err == nil {
		t.Fatalf("expected TLS enable/disable change to be rejected")
	}
}

func TestRuntimePolicyAllowsInScopeChanges(t *testing.T) {
	current := &Config{
		Server: ServerConfig{
			ListenAddr:           "127.0.0.1:4080",
			ReadTimeout:          30 * time.Second,
			WriteTimeout:         120 * time.Second,
			IdleTimeout:          120 * time.Second,
			TLSCertPath:          "a.pem",
			TLSKeyPath:           "b.pem",
			TLSCheckInterval:     24 * time.Hour,
			TLSExpiryWarningDays: 30,
		},
		Admin:    AdminConfig{TokenHash: "old"},
		Backends: []Backend{{Name: "primary", URL: "http://127.0.0.1:11434", Weight: 1}},
		Database: DatabaseConfig{Path: "gateway.db"},
		HealthCheck: HealthCheckConfig{
			IntervalSeconds:    10,
			TimeoutSeconds:     5,
			UnhealthyThreshold: 3,
		},
	}
	next := *current
	next.Admin.TokenHash = "new"
	next.Server.TLSCertPath = "new-cert.pem"
	next.Server.TLSKeyPath = "new-key.pem"
	next.Backends = []Backend{{Name: "primary", URL: "http://127.0.0.1:11435", Weight: 2}}

	policy := DefaultRuntimePolicy()
	if err := policy.ValidateRuntimeChange(current, &next); err != nil {
		t.Fatalf("expected in-scope changes to be allowed: %v", err)
	}
}

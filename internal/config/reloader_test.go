package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReloaderTriggerReloadAppliesAllowedChanges(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	initial := testConfigYAML("old-token", "http://127.0.0.1:11434")
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	current, err := Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	updated := testConfigYAML("new-token", "http://127.0.0.1:11435")
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		t.Fatalf("write updated config: %v", err)
	}

	var appliedToken string
	appliedBackends := 0
	r := NewReloader(configPath, current, nil, DefaultRuntimePolicy(), RuntimeApplier{
		ApplyAdminTokenHash: func(tokenHash string) {
			appliedToken = tokenHash
		},
		ApplyBackends: func(_ context.Context, backends []Backend) error {
			appliedBackends = len(backends)
			return nil
		},
	})

	if err := r.TriggerReload(context.Background(), "test"); err != nil {
		t.Fatalf("trigger reload: %v", err)
	}
	if appliedToken != "new-token" {
		t.Fatalf("expected admin token to be applied, got %q", appliedToken)
	}
	if appliedBackends != 1 {
		t.Fatalf("expected one backend applied, got %d", appliedBackends)
	}
	if got := r.Current().Admin.TokenHash; got != "new-token" {
		t.Fatalf("expected current snapshot token to be updated, got %q", got)
	}
	status := r.Status()
	if status.LastTrigger != "test" {
		t.Fatalf("expected last trigger to be recorded, got %q", status.LastTrigger)
	}
	if status.LastReloadAt.IsZero() {
		t.Fatalf("expected last reload time to be set")
	}
	if status.LastError != "" {
		t.Fatalf("expected empty last error on success, got %q", status.LastError)
	}
}

func TestReloaderRejectsInvalidYAMLAndKeepsCurrent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	initial := testConfigYAML("old-token", "http://127.0.0.1:11434")
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	current, err := Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}

	if err := os.WriteFile(configPath, []byte("not: [valid"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	callbackCalls := 0
	r := NewReloader(configPath, current, nil, DefaultRuntimePolicy(), RuntimeApplier{
		ApplyAdminTokenHash: func(tokenHash string) {
			callbackCalls++
		},
	})

	if err := r.TriggerReload(context.Background(), "test-invalid"); err == nil {
		t.Fatalf("expected reload to fail for invalid YAML")
	}
	if callbackCalls != 0 {
		t.Fatalf("expected callbacks to not be invoked on failed reload")
	}
	if got := r.Current().Admin.TokenHash; got != "old-token" {
		t.Fatalf("expected old config snapshot to remain active, got %q", got)
	}
	status := r.Status()
	if status.LastTrigger != "test-invalid" {
		t.Fatalf("expected last trigger to be recorded, got %q", status.LastTrigger)
	}
	if status.LastError == "" {
		t.Fatalf("expected last error to be set after failed reload")
	}
}

func TestIsExpectedStop(t *testing.T) {
	if !IsExpectedStop(context.Canceled) {
		t.Fatalf("expected canceled context to be recognized")
	}
	if IsExpectedStop(nil) {
		t.Fatalf("did not expect nil error to be considered expected stop")
	}
}

func testConfigYAML(tokenHash string, backendURL string) string {
	return "server:\n" +
		"  listen_addr: 127.0.0.1:4080\n" +
		"  read_timeout: 30s\n" +
		"  write_timeout: 120s\n" +
		"  idle_timeout: 120s\n" +
		"  tls_check_interval: 24h\n" +
		"  tls_expiry_warning_days: 30\n" +
		"admin:\n" +
		"  token_hash: " + tokenHash + "\n" +
		"rate_limiting:\n" +
		"  default_rate: 10\n" +
		"  default_burst: 50\n" +
		"  ttl: 1h\n" +
		"backends:\n" +
		"  - name: primary\n" +
		"    url: " + backendURL + "\n" +
		"    weight: 1\n" +
		"    timeout: 120s\n" +
		"    health_check_path: /api/version\n" +
		"models:\n" +
		"  models:\n" +
		"    llama3:\n" +
		"      name: llama3\n" +
		"      backends:\n" +
		"        - backend: primary\n" +
		"          weight: 1\n" +
		"users:\n" +
		"  demo:\n" +
		"    api_key_hash: demo-hash\n" +
		"pricing:\n" +
		"  default_input_per_1m_tokens: 0.2\n" +
		"  default_output_per_1m_tokens: 0.6\n" +
		"  models: {}\n" +
		"database:\n" +
		"  path: gateway.db\n" +
		"health_check:\n" +
		"  interval_seconds: 10\n" +
		"  timeout_seconds: 5\n" +
		"  unhealthy_threshold: 3\n"
}

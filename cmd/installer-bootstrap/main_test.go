package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRunCreatesConfigAndBootstrap(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	bootstrapPath := filepath.Join(dir, "bootstrap-admin.txt")
	dbPath := filepath.Join(dir, "gateway.db")

	opts := options{
		BackendName:   "local",
		BackendURL:    "http://127.0.0.1:11434",
		ConfigPath:    configPath,
		BootstrapPath: bootstrapPath,
		DatabasePath:  dbPath,
		ModelName:     "default-model",
		ListenAddr:    "0.0.0.0:4080",
	}

	var out bytes.Buffer
	if err := run(opts, &out); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	cfgData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}

	var cfg map[string]any
	if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	admin, ok := cfg["admin"].(map[string]any)
	if !ok {
		t.Fatalf("missing admin block")
	}
	hash, ok := admin["token_hash"].(string)
	if !ok || len(hash) != 64 {
		t.Fatalf("admin.token_hash not set to sha256 hex")
	}

	db, ok := cfg["database"].(map[string]any)
	if !ok {
		t.Fatalf("missing database block")
	}
	if db["path"] != dbPath {
		t.Fatalf("database.path = %v, want %q", db["path"], dbPath)
	}

	bootstrapData, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("read bootstrap details: %v", err)
	}
	if !strings.Contains(string(bootstrapData), "Admin token") {
		t.Fatalf("bootstrap file missing admin token header")
	}
	if !strings.Contains(out.String(), "ADMIN_TOKEN=") {
		t.Fatalf("stdout missing ADMIN_TOKEN export")
	}
}

func TestValidateOptionsRejectsInvalidURL(t *testing.T) {
	err := validateOptions(options{
		BackendName:   "local",
		BackendURL:    "127.0.0.1:11434",
		ConfigPath:    "config.yaml",
		BootstrapPath: "bootstrap.txt",
		DatabasePath:  "gateway.db",
		ModelName:     "m",
		ListenAddr:    "0.0.0.0:4080",
	})
	if err == nil {
		t.Fatal("expected validation error for missing URL scheme")
	}
}

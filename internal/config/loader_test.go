package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigPathPrefersExplicitFile(t *testing.T) {
	tempDir := t.TempDir()
	explicitPath := filepath.Join(tempDir, "custom.yaml")
	if err := os.WriteFile(explicitPath, []byte("server: {}\n"), 0o644); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}

	resolved, err := ResolveConfigPath(LoaderOptions{ConfigFile: explicitPath}, filepath.Join(tempDir, "missing.yaml"))
	if err != nil {
		t.Fatalf("ResolveConfigPath returned error: %v", err)
	}
	if resolved != explicitPath {
		t.Fatalf("expected explicit config path %q, got %q", explicitPath, resolved)
	}
}

func TestResolveConfigPathFallsBackToExampleConfig(t *testing.T) {
	tempDir := t.TempDir()
	defaultDir := filepath.Join(tempDir, "configs")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("mkdir default dir: %v", err)
	}

	examplePath := filepath.Join(defaultDir, "config.example.yaml")
	if err := os.WriteFile(examplePath, []byte("server: {}\n"), 0o644); err != nil {
		t.Fatalf("write example config: %v", err)
	}

	resolved, err := ResolveConfigPath(LoaderOptions{}, filepath.Join(defaultDir, "config.yaml"))
	if err != nil {
		t.Fatalf("ResolveConfigPath returned error: %v", err)
	}
	if resolved != examplePath {
		t.Fatalf("expected fallback config path %q, got %q", examplePath, resolved)
	}
}

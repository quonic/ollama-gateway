package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultConfigPath        = "/etc/ollama-gateway/config.yaml"
	defaultRepoConfigPath    = "configs/config.yaml"
	defaultRepoExampleConfig = "configs/config.example.yaml"
)

// LoaderOptions controls how LoadWithFlags behaves.
type LoaderOptions struct {
	ConfigFile       string
	SeedModelCatalog bool
}

// RegisterFlags registers command-line flags into the provided FlagSet
// and returns an options struct that captures their values.
func RegisterFlags(fs *flag.FlagSet, opts *LoaderOptions) {
	fs.StringVar(&opts.ConfigFile, "config", "", "path to YAML config file")
	fs.BoolVar(&opts.SeedModelCatalog, "seed-model-catalog", false, "seed DB model catalog once from YAML models if DB catalog is empty")
}

// ResolveConfigPath chooses the config path to use.
// It prefers an explicitly provided file, then a default config file if present,
// and finally falls back to the shipped example config when running from the repo.
func ResolveConfigPath(opts LoaderOptions, defaultPath string) (string, error) {
	if opts.ConfigFile != "" {
		if _, err := os.Stat(opts.ConfigFile); err == nil {
			return opts.ConfigFile, nil
		}
		if candidate, ok := resolveExampleConfig(filepath.Dir(opts.ConfigFile)); ok {
			return candidate, nil
		}
		return opts.ConfigFile, nil
	}
	if defaultPath != "" {
		if _, err := os.Stat(defaultPath); err == nil {
			return defaultPath, nil
		}
		if candidate, ok := resolveExampleConfig(filepath.Dir(defaultPath)); ok {
			return candidate, nil
		}
	}

	candidates := []string{
		defaultRepoConfigPath,
		defaultRepoExampleConfig,
		filepath.Join("..", "configs", "config.yaml"),
		filepath.Join("..", "configs", "config.example.yaml"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no config file found; provide --config or create configs/config.yaml")
}

func resolveExampleConfig(dir string) (string, bool) {
	if dir == "." || dir == "" {
		return "", false
	}
	for _, candidate := range []string{
		filepath.Join(dir, "config.example.yaml"),
		filepath.Join(dir, "config.yaml.example"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// LoadWithFlags loads configuration from a file path provided via flags or
// the given defaultPath fallback.
func LoadWithFlags(opts LoaderOptions, defaultPath string) (*Config, error) {
	path, err := ResolveConfigPath(opts, defaultPath)
	if err != nil {
		return nil, err
	}
	return Load(path)
}

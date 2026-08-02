package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level gateway configuration.
type Config struct {
	Server    ServerConfig          `yaml:"server"`
	Admin     AdminConfig           `yaml:"admin"`
	RateLimit RateLimitingConfig    `yaml:"rate_limiting,omitempty"`
	Backends  []Backend             `yaml:"backends"`
	Models    ModelCatalog          `yaml:"models"`
	Users     map[string]UserConfig `yaml:"users"`
	Pricing   PricingConfig         `yaml:"pricing"`
	Database  DatabaseConfig        `yaml:"database"`
}

// ServerConfig controls the HTTP server.
type ServerConfig struct {
	ListenAddr   string        `yaml:"listen_addr"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`
}

// AdminConfig holds the admin token hash for /admin/* routes.
type AdminConfig struct {
	TokenHash string `yaml:"token_hash"`
}

// RateLimitingConfig defines global defaults applied to API keys that do not
// specify their own per-key rate_limit override in Users config.
type RateLimitingConfig struct {
	DefaultRate  float64       `yaml:"default_rate"`  // tokens/sec refill (e.g. 10.0)
	DefaultBurst int           `yaml:"default_burst"` // max burst capacity (e.g. 50)
	TTL          time.Duration `yaml:"ttl,omitempty"` // bucket idle TTL before cleanup (default 1h)
}

// Backend represents a single Ollama backend server.
type Backend struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Weight  int               `yaml:"weight"`            // weight for round-robin (default 1)
	Headers map[string]string `yaml:"headers,omitempty"` // extra headers sent to backend
	Timeout time.Duration     `yaml:"timeout"`           // per-request timeout override
}

// ModelCatalog holds the global model definitions.
type ModelCatalog struct {
	// Models maps a canonical model name to its definition.
	Models map[string]ModelEntry `yaml:"models"`
}

// ModelEntry defines how a model is mapped to backends.
type ModelEntry struct {
	Name     string            `yaml:"name"`     // canonical display name (defaults to key)
	Backends []ModelBackendRef `yaml:"backends"` // weighted refs into the Backends list by Name or URL
}

// ModelBackendRef references a backend and its weight for this model.
type ModelBackendRef struct {
	Backend string `yaml:"backend"` // matches Backend.Name
	Weight  int    `yaml:"weight"`  // default 1 if not specified
}

// UserConfig defines per-user overrides.
type UserConfig struct {
	APIKeyHash string            `yaml:"api_key_hash"` // SHA-256 hex of the API key
	RateLimit  *RateLimitCfg     `yaml:"rate_limit,omitempty"`
	ModelAllow []string          `yaml:"model_allow,omitempty"` // allow-list (empty = all)
	ModelDeny  []string          `yaml:"model_deny,omitempty"`  // deny-list overrides allow-list
	Aliases    map[string]string `yaml:"aliases,omitempty"`     // alias -> canonical model name
}

// RateLimitCfg configures token-bucket rate limiting for a user.
type RateLimitCfg struct {
	Rate  float64       `yaml:"rate"`  // tokens per second refill rate
	Burst int           `yaml:"burst"` // max burst capacity
	TTL   time.Duration `yaml:"ttl"`   // bucket idle TTL before cleanup (default 1h)
}

// PricingConfig defines cost per 1M tokens for prompt/eval.
type PricingConfig struct {
	Models map[string]ModelPricing `yaml:"models"`
}

// ModelPricing holds per-model pricing in USD per 1M tokens.
type ModelPricing struct {
	PromptPer1M float64 `yaml:"prompt_per_1m"` // cost per 1M prompt tokens
	EvalPer1M   float64 `yaml:"eval_per_1m"`   // cost per 1M eval (output) tokens
}

// DatabaseConfig configures the SQLite database.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// Defaults and validation -----------------------------------------------------

const (
	defaultListenAddr    = "0.0.0.0:4080"
	defaultReadTimeout   = 30 * time.Second
	defaultWriteTimeout  = 120 * time.Second
	defaultIdleTimeout   = 120 * time.Second
	defaultBackendWeight = 1
	defaultBucketTTL     = 1 * time.Hour
	// Global rate limit defaults (applied when not specified in config).
	defaultRateLimitRate  float64 = 10.0 // tokens/sec refill
	defaultRateLimitBurst int     = 50   // max burst capacity
)

// Load reads the config from path, applies defaults and validates.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config YAML: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = defaultListenAddr
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = defaultReadTimeout
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = defaultWriteTimeout
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = defaultIdleTimeout
	}
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if b.Weight <= 0 {
			b.Weight = defaultBackendWeight
		}
		if b.Timeout == 0 {
			b.Timeout = defaultWriteTimeout
		}
	}
	for k, m := range cfg.Models.Models {
		m.Name = k // canonical name defaults to key; store back
		cfg.Models.Models[k] = m
		for i := range m.Backends {
			if m.Backends[i].Weight <= 0 {
				m.Backends[i].Weight = defaultBackendWeight
			}
		}
	}
	// Global rate limit defaults
	if cfg.RateLimit.DefaultRate <= 0 {
		cfg.RateLimit.DefaultRate = defaultRateLimitRate
	}
	if cfg.RateLimit.DefaultBurst <= 0 {
		cfg.RateLimit.DefaultBurst = defaultRateLimitBurst
	}
	if cfg.RateLimit.TTL == 0 {
		cfg.RateLimit.TTL = defaultBucketTTL
	}
	for userID, u := range cfg.Users {
		// Apply global TTL to per-key buckets that don't specify one.
		if u.RateLimit != nil && u.RateLimit.TTL == 0 {
			u.RateLimit.TTL = cfg.RateLimit.TTL
			cfg.Users[userID] = u // write back since map iteration is by value
		}
	}
}

func validate(cfg *Config) error {
	if len(cfg.Backends) == 0 {
		return fmt.Errorf("at least one backend must be configured")
	}
	for _, b := range cfg.Backends {
		if b.Name == "" {
			return fmt.Errorf("backend name is required")
		}
		if b.URL == "" {
			return fmt.Errorf("backend %q: url is required", b.Name)
		}
	}
	for k, m := range cfg.Models.Models {
		if len(m.Backends) == 0 {
			return fmt.Errorf("model %q: at least one backend ref is required", k)
		}
		for _, br := range m.Backends {
			found := false
			for _, b := range cfg.Backends {
				if b.Name == br.Backend {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("model %q: backend ref %q not found", k, br.Backend)
			}
		}
	}
	for userKey, u := range cfg.Users {
		if u.APIKeyHash == "" {
			return fmt.Errorf("user %q: api_key_hash is required", userKey)
		}
	}
	return nil
}

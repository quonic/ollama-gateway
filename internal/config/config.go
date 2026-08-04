package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level gateway configuration.
type Config struct {
	Server      ServerConfig          `yaml:"server"`
	Admin       AdminConfig           `yaml:"admin"`
	RateLimit   RateLimitingConfig    `yaml:"rate_limiting,omitempty"`
	Backends    []Backend             `yaml:"backends"`
	Models      ModelCatalog          `yaml:"models"`
	Users       map[string]UserConfig `yaml:"users"`
	Pricing     PricingConfig         `yaml:"pricing"`
	Database    DatabaseConfig        `yaml:"database"`
	HealthCheck HealthCheckConfig     `yaml:"health_check,omitempty"`
}

// ServerConfig controls the HTTP server.
type ServerConfig struct {
	ListenAddr           string        `yaml:"listen_addr"`
	ReadTimeout          time.Duration `yaml:"read_timeout"`
	WriteTimeout         time.Duration `yaml:"write_timeout"`
	IdleTimeout          time.Duration `yaml:"idle_timeout"`
	TLSCertPath          string        `yaml:"tls_cert_path,omitempty"`
	TLSKeyPath           string        `yaml:"tls_key_path,omitempty"`
	TLSCheckInterval     time.Duration `yaml:"tls_check_interval,omitempty"`
	TLSExpiryWarningDays int           `yaml:"tls_expiry_warning_days,omitempty"`
}

// AdminConfig holds the admin token hash for /admin/* routes.
type AdminConfig struct {
	TokenHash string `yaml:"token_hash"`
}

// RateLimitingConfig defines global defaults applied to API keys that do not
// specify their own per-key rate_limit override in Users config.
type RateLimitingConfig struct {
	DefaultRate          float64       `yaml:"default_rate"`      // tokens/sec refill (e.g. 10.0)
	DefaultBurst         int           `yaml:"default_burst"`     // max burst capacity (e.g. 50)
	TTL                  time.Duration `yaml:"ttl,omitempty"`     // bucket idle TTL before cleanup (default 1h)
	Backend              string        `yaml:"backend,omitempty"` // backend type: local or redis
	RedisAddr            string        `yaml:"redis_addr,omitempty"`
	RedisPassword        string        `yaml:"redis_password,omitempty"`
	RedisDB              int           `yaml:"redis_db,omitempty"`
	RedisKeyPrefix       string        `yaml:"redis_key_prefix,omitempty"`
	RedisTimeoutSec      int           `yaml:"redis_timeout_sec,omitempty"`
	RedisFallbackToLocal bool          `yaml:"redis_fallback_to_local,omitempty"`
}

// HealthCheckConfig defines periodic health check behavior for backends.
type HealthCheckConfig struct {
	IntervalSeconds    int `yaml:"interval_seconds"`    // how often to check each backend (default 10)
	TimeoutSeconds     int `yaml:"timeout_seconds"`     // per-check HTTP timeout (default 5)
	UnhealthyThreshold int `yaml:"unhealthy_threshold"` // consecutive failures before marking unhealthy (default 3)
}

// Backend represents a single Ollama backend server.
type Backend struct {
	Name            string            `yaml:"name"`
	URL             string            `yaml:"url"`
	Weight          int               `yaml:"weight"`                      // weight for round-robin (default 1)
	Tag             string            `yaml:"tag,omitempty"`               // optional backend tag for grouping/metadata
	Headers         map[string]string `yaml:"headers,omitempty"`           // extra headers sent to backend
	Timeout         time.Duration     `yaml:"timeout"`                     // per-request timeout override
	HealthCheckPath string            `yaml:"health_check_path,omitempty"` // path for health checks (default /api/version)
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

// PricingConfig defines cost per 1M tokens for prompt and output, with global defaults.
type PricingConfig struct {
	DefaultInputPer1M  float64                 `yaml:"default_input_per_1m_tokens"` // applies to any unpriced model
	DefaultOutputPer1M float64                 `yaml:"default_output_per_1m_tokens"`
	Models             map[string]ModelPricing `yaml:"models"`
}

// ModelPricing holds per-model pricing in USD per 1M tokens.
type ModelPricing struct {
	InputCostPer1M  float64 `yaml:"input_cost_per_1m_tokens"`  // cost per 1M prompt (input) tokens
	OutputCostPer1M float64 `yaml:"output_cost_per_1m_tokens"` // cost per 1M eval (output) tokens
}

// DatabaseConfig configures the SQLite database.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// Defaults and validation -----------------------------------------------------

const (
	defaultListenAddr           = "0.0.0.0:4080"
	defaultReadTimeout          = 30 * time.Second
	defaultWriteTimeout         = 120 * time.Second
	defaultIdleTimeout          = 120 * time.Second
	defaultTLSCheckInterval     = 24 * time.Hour
	defaultTLSExpiryWarningDays = 30
	defaultBackendWeight        = 1
	defaultBucketTTL            = 1 * time.Hour
	defaultRateLimitBackend     = "local"
	defaultRedisTimeoutSec      = 2
	defaultRedisFallbackToLocal = true
	// Global rate limit defaults (applied when not specified in config).
	defaultRateLimitRate  float64 = 10.0 // tokens/sec refill
	defaultRateLimitBurst int     = 50   // max burst capacity
	// Health check defaults.
	defaultHealthCheckInterval = 10 // seconds between checks
	defaultHealthCheckTimeout  = 5  // per-check HTTP timeout in seconds
	defaultUnhealthyThreshold  = 3  // consecutive failures before unhealthy
	defaultDatabasePath        = "/var/lib/ollama-gateway/gateway.db"
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
	if cfg.Server.TLSCheckInterval <= 0 {
		cfg.Server.TLSCheckInterval = defaultTLSCheckInterval
	}
	if cfg.Server.TLSExpiryWarningDays <= 0 {
		cfg.Server.TLSExpiryWarningDays = defaultTLSExpiryWarningDays
	}
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		if b.Weight <= 0 {
			b.Weight = defaultBackendWeight
		}
		if b.Timeout == 0 {
			b.Timeout = defaultWriteTimeout
		}
		if b.HealthCheckPath == "" {
			b.HealthCheckPath = "/api/version"
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
	if cfg.RateLimit.Backend == "" {
		cfg.RateLimit.Backend = defaultRateLimitBackend
	}
	if cfg.RateLimit.RedisTimeoutSec <= 0 {
		cfg.RateLimit.RedisTimeoutSec = defaultRedisTimeoutSec
	}
	if !cfg.RateLimit.RedisFallbackToLocal {
		cfg.RateLimit.RedisFallbackToLocal = defaultRedisFallbackToLocal
	}
	for userID, u := range cfg.Users {
		// Apply global TTL to per-key buckets that don't specify one.
		if u.RateLimit != nil && u.RateLimit.TTL == 0 {
			u.RateLimit.TTL = cfg.RateLimit.TTL
			cfg.Users[userID] = u // write back since map iteration is by value
		}
	}
	// Health check defaults
	if cfg.HealthCheck.IntervalSeconds <= 0 {
		cfg.HealthCheck.IntervalSeconds = defaultHealthCheckInterval
	}
	if cfg.HealthCheck.TimeoutSeconds <= 0 {
		cfg.HealthCheck.TimeoutSeconds = defaultHealthCheckTimeout
	}
	if cfg.HealthCheck.UnhealthyThreshold <= 0 {
		cfg.HealthCheck.UnhealthyThreshold = defaultUnhealthyThreshold
	}
	if strings.TrimSpace(cfg.Database.Path) == "" {
		cfg.Database.Path = defaultDatabasePath
	}
}

func validate(cfg *Config) error {
	certPath := strings.TrimSpace(cfg.Server.TLSCertPath)
	keyPath := strings.TrimSpace(cfg.Server.TLSKeyPath)
	if (certPath == "") != (keyPath == "") {
		return fmt.Errorf("server: tls_cert_path and tls_key_path must both be set or both be empty")
	}
	if certPath != "" {
		if err := validateReadableFile(certPath); err != nil {
			return fmt.Errorf("server: tls_cert_path %q: %w", certPath, err)
		}
		if err := validateReadableFile(keyPath); err != nil {
			return fmt.Errorf("server: tls_key_path %q: %w", keyPath, err)
		}
	}
	if cfg.Server.TLSCheckInterval <= 0 {
		return fmt.Errorf("server: tls_check_interval must be greater than zero")
	}
	if cfg.Server.TLSExpiryWarningDays <= 0 {
		return fmt.Errorf("server: tls_expiry_warning_days must be greater than zero")
	}

	if len(cfg.Backends) == 0 {
		return fmt.Errorf("at least one backend must be configured")
	}
	// Check for duplicate backend names.
	seenNames := make(map[string]bool)
	for _, b := range cfg.Backends {
		if b.Name == "" {
			return fmt.Errorf("backend name is required")
		}
		if seenNames[b.Name] {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seenNames[b.Name] = true
		if b.URL == "" {
			return fmt.Errorf("backend %q: url is required", b.Name)
		}
		// Validate URL has a scheme.
		u, err := url.Parse(b.URL)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("backend %q: url must include http:// or https:// scheme", b.Name)
		}
		if !strings.HasPrefix(u.Scheme, "http") {
			return fmt.Errorf("backend %q: unsupported url scheme %q (use http or https)", b.Name, u.Scheme)
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

func validateReadableFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}

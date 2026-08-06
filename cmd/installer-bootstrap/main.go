package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ollama-gateway/internal/auth"

	"gopkg.in/yaml.v3"
)

type options struct {
	BackendName   string
	BackendURL    string
	ConfigPath    string
	BootstrapPath string
	DatabasePath  string
	ModelName     string
	ListenAddr    string
}

type outputWriter interface {
	WriteString(s string) (n int, err error)
}

type generatedConfig struct {
	Server      serverConfig          `yaml:"server"`
	Admin       adminConfig           `yaml:"admin"`
	RateLimit   rateLimitConfig       `yaml:"rate_limiting"`
	Backends    []backendConfig       `yaml:"backends"`
	Models      modelsConfig          `yaml:"models"`
	Users       map[string]userConfig `yaml:"users"`
	Pricing     pricingConfig         `yaml:"pricing"`
	Database    databaseConfig        `yaml:"database"`
	HealthCheck healthCheckConfig     `yaml:"health_check"`
}

type serverConfig struct {
	ListenAddr   string `yaml:"listen_addr"`
	ReadTimeout  string `yaml:"read_timeout"`
	WriteTimeout string `yaml:"write_timeout"`
	IdleTimeout  string `yaml:"idle_timeout"`
}

type adminConfig struct {
	TokenHash string `yaml:"token_hash"`
}

type rateLimitConfig struct {
	DefaultRate  float64 `yaml:"default_rate"`
	DefaultBurst int     `yaml:"default_burst"`
	TTL          string  `yaml:"ttl"`
	Backend      string  `yaml:"backend"`
}

type backendConfig struct {
	Name            string `yaml:"name"`
	URL             string `yaml:"url"`
	Weight          int    `yaml:"weight"`
	Timeout         string `yaml:"timeout"`
	HealthCheckPath string `yaml:"health_check_path"`
}

type modelsConfig struct {
	Models map[string]modelEntry `yaml:"models"`
}

type modelEntry struct {
	Name     string            `yaml:"name"`
	Backends []modelBackendRef `yaml:"backends"`
}

type modelBackendRef struct {
	Backend string `yaml:"backend"`
	Weight  int    `yaml:"weight"`
}

type userConfig struct {
	APIKeyHash string `yaml:"api_key_hash"`
}

type pricingConfig struct {
	DefaultInputPer1M  float64                 `yaml:"default_input_per_1m_tokens"`
	DefaultOutputPer1M float64                 `yaml:"default_output_per_1m_tokens"`
	Models             map[string]modelPricing `yaml:"models"`
}

type modelPricing struct {
	InputCostPer1M  float64 `yaml:"input_cost_per_1m_tokens"`
	OutputCostPer1M float64 `yaml:"output_cost_per_1m_tokens"`
}

type databaseConfig struct {
	Path string `yaml:"path"`
}

type healthCheckConfig struct {
	IntervalSeconds    int `yaml:"interval_seconds"`
	TimeoutSeconds     int `yaml:"timeout_seconds"`
	UnhealthyThreshold int `yaml:"unhealthy_threshold"`
}

func main() {
	var opts options

	flag.StringVar(&opts.BackendName, "backend-name", "", "initial backend name")
	flag.StringVar(&opts.BackendURL, "backend-url", "", "initial backend URL")
	flag.StringVar(&opts.ConfigPath, "config-path", "", "output path for generated config.yaml")
	flag.StringVar(&opts.BootstrapPath, "bootstrap-path", "", "output path for bootstrap details text")
	flag.StringVar(&opts.DatabasePath, "database-path", "", "database path to write into generated config")
	flag.StringVar(&opts.ModelName, "model-name", "default-model", "initial model name")
	flag.StringVar(&opts.ListenAddr, "listen-addr", "0.0.0.0:4080", "listen address to write into generated config")
	flag.Parse()

	if err := run(opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "installer-bootstrap error: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options, out outputWriter) error {
	if err := validateOptions(opts); err != nil {
		return err
	}

	token, err := generateToken(32)
	if err != nil {
		return fmt.Errorf("generate admin token: %w", err)
	}
	hash := auth.HashAPIKey(token)

	cfg := buildConfig(opts, hash)
	yamlBytes, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config yaml: %w", err)
	}

	if err := writeFile(opts.ConfigPath, yamlBytes, 0o600); err != nil {
		return fmt.Errorf("write generated config: %w", err)
	}

	bootstrap := buildBootstrapText(opts, token)
	if err := writeFile(opts.BootstrapPath, []byte(bootstrap), 0o600); err != nil {
		return fmt.Errorf("write bootstrap details: %w", err)
	}

	_, _ = out.WriteString("CONFIG_PATH=" + opts.ConfigPath + "\n")
	_, _ = out.WriteString("BOOTSTRAP_PATH=" + opts.BootstrapPath + "\n")
	return nil
}

func validateOptions(opts options) error {
	if strings.TrimSpace(opts.BackendName) == "" {
		return errors.New("backend-name is required")
	}
	if strings.TrimSpace(opts.BackendURL) == "" {
		return errors.New("backend-url is required")
	}
	u, err := url.Parse(opts.BackendURL)
	if err != nil || u.Scheme == "" {
		return errors.New("backend-url must include http:// or https:// scheme")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("backend-url must use http or https scheme")
	}
	if strings.TrimSpace(opts.ConfigPath) == "" {
		return errors.New("config-path is required")
	}
	if strings.TrimSpace(opts.BootstrapPath) == "" {
		return errors.New("bootstrap-path is required")
	}
	if strings.TrimSpace(opts.DatabasePath) == "" {
		return errors.New("database-path is required")
	}
	if strings.TrimSpace(opts.ModelName) == "" {
		return errors.New("model-name is required")
	}
	if strings.TrimSpace(opts.ListenAddr) == "" {
		return errors.New("listen-addr is required")
	}
	return nil
}

func buildConfig(opts options, adminHash string) generatedConfig {
	modelName := strings.TrimSpace(opts.ModelName)
	backendName := strings.TrimSpace(opts.BackendName)

	return generatedConfig{
		Server: serverConfig{
			ListenAddr:   opts.ListenAddr,
			ReadTimeout:  "30s",
			WriteTimeout: "120s",
			IdleTimeout:  "120s",
		},
		Admin: adminConfig{
			TokenHash: adminHash,
		},
		RateLimit: rateLimitConfig{
			DefaultRate:  10.0,
			DefaultBurst: 50,
			TTL:          "1h",
			Backend:      "local",
		},
		Backends: []backendConfig{
			{
				Name:            backendName,
				URL:             strings.TrimSpace(opts.BackendURL),
				Weight:          1,
				Timeout:         "120s",
				HealthCheckPath: "/api/version",
			},
		},
		Models: modelsConfig{
			Models: map[string]modelEntry{
				modelName: {
					Name: modelName,
					Backends: []modelBackendRef{
						{Backend: backendName, Weight: 1},
					},
				},
			},
		},
		Users: map[string]userConfig{},
		Pricing: pricingConfig{
			DefaultInputPer1M:  0.0,
			DefaultOutputPer1M: 0.0,
			Models:             map[string]modelPricing{},
		},
		Database: databaseConfig{Path: opts.DatabasePath},
		HealthCheck: healthCheckConfig{
			IntervalSeconds:    10,
			TimeoutSeconds:     5,
			UnhealthyThreshold: 3,
		},
	}
}

func buildBootstrapText(opts options, token string) string {
	return strings.Join([]string{
		"Ollama Gateway bootstrap details",
		"",
		"Admin token (shown once):",
		token,
		"",
		"Config path:",
		opts.ConfigPath,
		"",
		"Backend:",
		"- name: " + opts.BackendName,
		"- url: " + opts.BackendURL,
		"",
		"Store this token securely. It is not persisted in plain text by the gateway.",
	}, "\n") + "\n"
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, mode)
}

func generateToken(byteLen int) (string, error) {
	if byteLen <= 0 {
		return "", errors.New("byteLen must be positive")
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

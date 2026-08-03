package config

import (
	"fmt"
	"strings"
)

// RuntimePolicy controls which YAML fields may change without process restart.
type RuntimePolicy struct {
	AllowAdminTokenHash bool
	AllowBackends       bool
	AllowTLSPaths       bool
}

// DefaultRuntimePolicy enables the currently supported hot-reload fields.
func DefaultRuntimePolicy() RuntimePolicy {
	return RuntimePolicy{
		AllowAdminTokenHash: true,
		AllowBackends:       true,
		AllowTLSPaths:       true,
	}
}

// ValidateRuntimeChange rejects runtime-unsafe config changes.
func (p RuntimePolicy) ValidateRuntimeChange(current, next *Config) error {
	if current == nil || next == nil {
		return fmt.Errorf("runtime policy requires current and next config")
	}

	if current.Server.ListenAddr != next.Server.ListenAddr {
		return fmt.Errorf("runtime reload rejected: server.listen_addr requires restart")
	}
	if current.Server.ReadTimeout != next.Server.ReadTimeout {
		return fmt.Errorf("runtime reload rejected: server.read_timeout requires restart")
	}
	if current.Server.WriteTimeout != next.Server.WriteTimeout {
		return fmt.Errorf("runtime reload rejected: server.write_timeout requires restart")
	}
	if current.Server.IdleTimeout != next.Server.IdleTimeout {
		return fmt.Errorf("runtime reload rejected: server.idle_timeout requires restart")
	}
	if current.Database.Path != next.Database.Path {
		return fmt.Errorf("runtime reload rejected: database.path requires restart")
	}
	if current.HealthCheck.IntervalSeconds != next.HealthCheck.IntervalSeconds {
		return fmt.Errorf("runtime reload rejected: health_check.interval_seconds requires restart")
	}
	if current.HealthCheck.TimeoutSeconds != next.HealthCheck.TimeoutSeconds {
		return fmt.Errorf("runtime reload rejected: health_check.timeout_seconds requires restart")
	}
	if current.HealthCheck.UnhealthyThreshold != next.HealthCheck.UnhealthyThreshold {
		return fmt.Errorf("runtime reload rejected: health_check.unhealthy_threshold requires restart")
	}

	currentTLSEnabled := tlsPathsEnabled(current.Server.TLSCertPath, current.Server.TLSKeyPath)
	nextTLSEnabled := tlsPathsEnabled(next.Server.TLSCertPath, next.Server.TLSKeyPath)
	if currentTLSEnabled != nextTLSEnabled {
		return fmt.Errorf("runtime reload rejected: enabling or disabling TLS requires restart")
	}

	if !p.AllowTLSPaths {
		if current.Server.TLSCertPath != next.Server.TLSCertPath || current.Server.TLSKeyPath != next.Server.TLSKeyPath {
			return fmt.Errorf("runtime reload rejected: TLS cert/key path changes are disabled")
		}
	}
	if !p.AllowAdminTokenHash && current.Admin.TokenHash != next.Admin.TokenHash {
		return fmt.Errorf("runtime reload rejected: admin.token_hash changes are disabled")
	}
	if !p.AllowBackends && !backendsEqual(current.Backends, next.Backends) {
		return fmt.Errorf("runtime reload rejected: backend changes are disabled")
	}

	return nil
}

func tlsPathsEnabled(certPath, keyPath string) bool {
	return strings.TrimSpace(certPath) != "" && strings.TrimSpace(keyPath) != ""
}

func backendsEqual(a, b []Backend) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
		if a[i].URL != b[i].URL {
			return false
		}
		if a[i].Weight != b[i].Weight {
			return false
		}
		if a[i].Tag != b[i].Tag {
			return false
		}
		if a[i].Timeout != b[i].Timeout {
			return false
		}
		if a[i].HealthCheckPath != b[i].HealthCheckPath {
			return false
		}
		if len(a[i].Headers) != len(b[i].Headers) {
			return false
		}
		for k, v := range a[i].Headers {
			if b[i].Headers[k] != v {
				return false
			}
		}
	}
	return true
}

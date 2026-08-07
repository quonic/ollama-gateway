package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Backend represents a single Ollama backend server with scheduling and health state.
type Backend struct {
	Name            string            // Unique identifier, matches config name
	URL             *url.URL          // Parsed base URL of the Ollama server
	Weight          int               // Configured weight for round-robin distribution
	Tag             string            // Optional admin-defined tag
	HealthCheckPath string            // HTTP path used for health checks (e.g. /api/version)
	Timeout         time.Duration     // Per-request timeout when proxying to this backend
	Headers         map[string]string // Extra headers sent to backend on every request

	// Scheduling fields — guarded by BackendPool.mu during selection.
	effectiveWeight int // Adjusted based on health; starts equal to Weight, drops to 0 when unhealthy
	currentWeight   int // Modified during smooth WRR selection

	// Health state — updated atomically by the health checker goroutine.
	healthy       bool      // Current health status
	lastCheckTime time.Time // When this backend was last checked
	failureCount  int       // Consecutive failure count (resets on success)

	// Runtime admin state — these are not persisted and can be toggled live.
	enabled bool // Whether this backend is currently allowed to receive traffic
}

// NewBackend creates a Backend from config values, initializing scheduling state.
func NewBackend(name string, rawURL string, weight int, tag string, healthCheckPath string, timeout time.Duration, headers map[string]string) (*Backend, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	b := &Backend{
		Name:            name,
		URL:             u,
		Weight:          weight,
		Tag:             tag,
		HealthCheckPath: healthCheckPath,
		Timeout:         timeout,
		Headers:         headers,

		effectiveWeight: weight,
		currentWeight:   0,
		healthy:         true, // starts optimistic; first health check will confirm or deny
		enabled:         true,
	}
	return b, nil
}

// ModelInfo captures a model entry returned by Ollama's tags or ps endpoints.
type ModelInfo struct {
	Name       string `json:"name"`
	Model      string `json:"model"`
	ModifiedAt string `json:"modified_at"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
}

// ModelShowDetails captures the nested "details" object from Ollama /api/show.
type ModelShowDetails struct {
	ParentModel       string
	Format            string
	Family            string
	Families          []string
	ParameterSize     string
	QuantizationLevel string
}

// ModelShowMetadata captures the fields the dashboard needs from /api/show.
type ModelShowMetadata struct {
	Capabilities []string
	Details      ModelShowDetails
}

type modelListResponse struct {
	Models []ModelInfo `json:"models"`
}

// PullProgress captures one streamed progress event from Ollama's /api/pull.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// PullModel downloads a model onto the backend.
func (b *Backend) PullModel(ctx context.Context, modelName string) error {
	return b.PullModelStream(ctx, modelName, nil)
}

// PullModelStream downloads a model onto the backend and reports streamed
// progress updates for each JSON event returned by Ollama's /api/pull.
func (b *Backend) PullModelStream(ctx context.Context, modelName string, onProgress func(PullProgress)) error {
	if ctx == nil {
		ctx = context.Background()
	}

	var bodyReader io.Reader
	data, err := json.Marshal(map[string]string{"name": modelName})
	if err != nil {
		return fmt.Errorf("marshal pull payload: %w", err)
	}
	bodyReader = bytes.NewReader(data)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.URL.String(), "/")+"/api/pull", bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range b.Headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call POST /api/pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read error response body: %w", readErr)
		}
		return fmt.Errorf("POST /api/pull returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	decoder := json.NewDecoder(resp.Body)
	for {
		var progress PullProgress
		if err := decoder.Decode(&progress); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("decode /api/pull response: %w", err)
		}
		if onProgress != nil {
			onProgress(progress)
		}
		if strings.TrimSpace(progress.Error) != "" {
			return fmt.Errorf("pull model %q: %s", modelName, strings.TrimSpace(progress.Error))
		}
	}

	return nil
}

// DeleteModel removes a model from the backend's local storage.
func (b *Backend) DeleteModel(ctx context.Context, modelName string) error {
	return b.doModelOperation(ctx, http.MethodDelete, "/api/delete", map[string]string{"name": modelName})
}

// EjectModel unloads a model from the backend's runtime memory by issuing a
// generate request with keep_alive set to 0.
func (b *Backend) EjectModel(ctx context.Context, modelName string) error {
	return b.doModelOperation(ctx, http.MethodPost, "/api/generate", map[string]any{"model": modelName, "keep_alive": 0})
}

// ListModels returns the models available on the backend.
func (b *Backend) ListModels(ctx context.Context) ([]ModelInfo, error) {
	payload, err := b.doJSONRequest(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	var response modelListResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("decode /api/tags response: %w", err)
	}
	return response.Models, nil
}

// ListInMemoryModels returns the models currently loaded in memory on the backend.
func (b *Backend) ListInMemoryModels(ctx context.Context) ([]ModelInfo, error) {
	payload, err := b.doJSONRequest(ctx, http.MethodGet, "/api/ps", nil)
	if err != nil {
		return nil, err
	}
	var response modelListResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("decode /api/ps response: %w", err)
	}
	return response.Models, nil
}

// ShowModel returns detailed information for a model from the backend.
func (b *Backend) ShowModel(ctx context.Context, modelName string) (map[string]any, error) {
	payload, err := b.doJSONRequest(ctx, http.MethodPost, "/api/show", map[string]string{"name": modelName})
	if err != nil {
		return nil, err
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("decode /api/show response: %w", err)
	}
	return response, nil
}

// ParseModelShowMetadata extracts capabilities and details from a raw /api/show payload.
// Missing fields or type mismatches are ignored and returned as zero values.
func ParseModelShowMetadata(payload map[string]any) *ModelShowMetadata {
	metadata := &ModelShowMetadata{
		Capabilities: []string{},
		Details: ModelShowDetails{
			Families: []string{},
		},
	}
	if payload == nil {
		return metadata
	}

	if details, ok := payload["details"].(map[string]any); ok {
		metadata.Details.ParentModel = stringField(details, "parent_model")
		metadata.Details.Format = stringField(details, "format")
		metadata.Details.Family = stringField(details, "family")
		metadata.Details.Families = stringSliceField(details, "families")
		metadata.Details.ParameterSize = stringField(details, "parameter_size")
		metadata.Details.QuantizationLevel = stringField(details, "quantization_level")
	}

	metadata.Capabilities = stringSliceField(payload, "capabilities")

	return metadata
}

func stringField(values map[string]any, key string) string {
	if raw, ok := values[key]; ok {
		if value, ok := raw.(string); ok {
			return value
		}
	}
	return ""
}

func stringSliceField(values map[string]any, key string) []string {
	raw, ok := values[key]
	if !ok {
		return []string{}
	}

	switch typed := raw.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, value := range typed {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			out = append(out, value)
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			out = append(out, value)
		}
		return out
	default:
		return []string{}
	}
}

func (b *Backend) doModelOperation(ctx context.Context, method, path string, payload any) error {
	_, err := b.doJSONRequest(ctx, method, path, payload)
	return err
}

func (b *Backend) doJSONRequest(ctx context.Context, method, path string, payload any) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(b.URL.String(), "/")+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range b.Headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{Timeout: b.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s returned status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return responseBody, nil
}

// IsHealthy returns whether this backend is currently considered healthy.
func (b *Backend) IsHealthy() bool {
	return b.healthy && b.enabled
}

// SetEnabled toggles whether this backend is allowed to receive traffic at runtime.
func (b *Backend) SetEnabled(enabled bool) {
	b.enabled = enabled
}

// IsEnabled returns whether this backend is currently enabled for routing.
func (b *Backend) IsEnabled() bool {
	return b.enabled
}

// SetHealth updates the health status and adjusts effectiveWeight accordingly.
// When becoming unhealthy, effectiveWeight drops to 0 so it's skipped in WRR selection.
// When recovering, effectiveWeight resets back to the configured Weight.
func (b *Backend) SetHealth(healthy bool) {
	b.healthy = healthy
	if healthy {
		b.effectiveWeight = b.Weight
	} else {
		b.effectiveWeight = 0
	}
}

// UpdateConfig mutates runtime backend fields used by routing and health checks.
func (b *Backend) UpdateConfig(rawURL string, weight int, tag string, timeout time.Duration, healthCheckPath string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	b.URL = u
	b.Weight = weight
	b.Tag = tag
	b.Timeout = timeout
	b.HealthCheckPath = healthCheckPath
	if b.healthy {
		b.effectiveWeight = weight
	}
	return nil
}

// MarkFailure increments the consecutive failure counter and updates last check time.
func (b *Backend) MarkFailure() {
	b.failureCount++
	b.lastCheckTime = time.Now()
}

// MarkSuccess resets the failure counter and updates last check time.
func (b *Backend) MarkSuccess() {
	b.failureCount = 0
	b.lastCheckTime = time.Now()
}

// FailureCount returns the number of consecutive health check failures.
func (b *Backend) FailureCount() int {
	return b.failureCount
}

// LastCheckTime returns when this backend was last checked.
func (b *Backend) LastCheckTime() time.Time {
	return b.lastCheckTime
}

// EffectiveWeight returns the current effective weight used in WRR selection.
func (b *Backend) EffectiveWeight() int {
	return b.effectiveWeight
}

// Name_ returns the backend's name (interface method to avoid collision with field).
func (b *Backend) Name_() string {
	return b.Name
}

// URL_ returns the backend's parsed URL.
func (b *Backend) URL_() *url.URL {
	return b.URL
}

// Weight_ returns the backend's configured weight.
func (b *Backend) Weight_() int {
	return b.Weight
}

var _ BackendLike = (*Backend)(nil)

// DefaultModelWeight is the default per-model backend weight when none is specified.
const DefaultModelWeight = 1

// BackendLike is an interface for backends, enabling mocking in tests.
type BackendLike interface {
	IsHealthy() bool
	SetHealth(healthy bool)
	Name_() string
	URL_() *url.URL
	Weight_() int
	EffectiveWeight() int
}

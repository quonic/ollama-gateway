package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
)

// DiscoveryStats captures startup discovery outcomes across backends.
type DiscoveryStats struct {
	SuccessfulBackends int
	FailedBackends     int
}

// DiscoverCatalogFromBackends calls each configured backend /api/tags endpoint and
// builds a merged model catalog suitable for resolver and DB sync.
func DiscoverCatalogFromBackends(ctx context.Context, cfg *config.Config) (map[string]config.ModelEntry, error) {
	catalog, _, err := DiscoverCatalogFromBackendsWithStats(ctx, cfg)
	return catalog, err
}

// DiscoverCatalogFromBackendsWithStats calls each configured backend /api/tags endpoint,
// returns the merged normalized catalog, discovery stats, and aggregated errors for failures.
func DiscoverCatalogFromBackendsWithStats(ctx context.Context, cfg *config.Config) (map[string]config.ModelEntry, DiscoveryStats, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	catalog := make(map[string]config.ModelEntry)
	stats := DiscoveryStats{}

	var errs []error

	for _, b := range cfg.Backends {
		models, err := discoverBackendModels(ctx, client, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("backend %q discovery failed: %w", b.Name, err))
			stats.FailedBackends++
			continue
		}
		stats.SuccessfulBackends++

		weight := b.Weight
		if weight <= 0 {
			weight = backends.DefaultModelWeight
		}

		for _, modelName := range models {
			normalized := NormalizeModelName(modelName)
			if normalized == "" {
				continue
			}

			entry, ok := catalog[normalized]
			if !ok {
				entry = config.ModelEntry{
					Name:     normalized,
					Backends: make([]config.ModelBackendRef, 0, 1),
				}
			}

			alreadyPresent := false
			for _, ref := range entry.Backends {
				if ref.Backend == b.Name {
					alreadyPresent = true
					break
				}
			}
			if !alreadyPresent {
				entry.Backends = append(entry.Backends, config.ModelBackendRef{
					Backend: b.Name,
					Weight:  weight,
				})
			}
			catalog[normalized] = entry
		}
	}

	if stats.SuccessfulBackends == 0 {
		if len(errs) == 0 {
			return catalog, stats, fmt.Errorf("model discovery failed: no backends configured")
		}
		return catalog, stats, fmt.Errorf("model discovery failed for all backends: %w", errors.Join(errs...))
	}

	if len(errs) > 0 {
		return catalog, stats, fmt.Errorf("model discovery partially failed: %w", errors.Join(errs...))
	}

	return catalog, stats, nil
}

type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func discoverBackendModels(ctx context.Context, client *http.Client, backendCfg config.Backend) ([]string, error) {
	tagsURL := strings.TrimRight(backendCfg.URL, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create discovery request: %w", err)
	}
	for k, v := range backendCfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call /api/tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("/api/tags returned status %d", resp.StatusCode)
	}

	var payload tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode /api/tags response: %w", err)
	}

	seen := make(map[string]struct{})
	out := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}

	return out, nil
}

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

// DiscoverCatalogFromBackends calls each configured backend /api/tags endpoint and
// builds a merged model catalog suitable for resolver and DB sync.
func DiscoverCatalogFromBackends(ctx context.Context, cfg *config.Config) (map[string]config.ModelEntry, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	catalog := make(map[string]config.ModelEntry)

	var errs []error
	successes := 0

	for _, b := range cfg.Backends {
		models, err := discoverBackendModels(ctx, client, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("backend %q discovery failed: %w", b.Name, err))
			continue
		}
		successes++

		weight := b.Weight
		if weight <= 0 {
			weight = backends.DefaultModelWeight
		}

		for _, modelName := range models {
			entry, ok := catalog[modelName]
			if !ok {
				entry = config.ModelEntry{
					Name:     modelName,
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
			catalog[modelName] = entry
		}
	}

	if successes == 0 {
		if len(errs) == 0 {
			return catalog, fmt.Errorf("model discovery failed: no backends configured")
		}
		return catalog, fmt.Errorf("model discovery failed for all backends: %w", errors.Join(errs...))
	}

	if len(errs) > 0 {
		return catalog, fmt.Errorf("model discovery partially failed: %w", errors.Join(errs...))
	}

	return catalog, nil
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

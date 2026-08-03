package models

import (
	"strings"

	"ollama-gateway/internal/config"
)

// NormalizeModelName converts user/discovery model names into a canonical key.
// Policy:
//   - trim spaces
//   - lower-case
//   - strip trailing ":latest"
func NormalizeModelName(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if strings.HasSuffix(n, ":latest") {
		n = strings.TrimSuffix(n, ":latest")
	}
	return n
}

// NormalizeCatalog returns a new catalog map keyed by normalized model names.
// If multiple entries normalize to the same key, backend refs are merged.
func NormalizeCatalog(in map[string]config.ModelEntry) map[string]config.ModelEntry {
	out := make(map[string]config.ModelEntry, len(in))
	for rawName, entry := range in {
		name := NormalizeModelName(rawName)
		if name == "" {
			continue
		}
		current, ok := out[name]
		if !ok {
			current = config.ModelEntry{
				Name:     name,
				Backends: make([]config.ModelBackendRef, 0, len(entry.Backends)),
			}
		}

		seen := make(map[string]struct{}, len(current.Backends))
		for _, existing := range current.Backends {
			seen[existing.Backend] = struct{}{}
		}
		for _, ref := range entry.Backends {
			if ref.Backend == "" {
				continue
			}
			if _, exists := seen[ref.Backend]; exists {
				continue
			}
			seen[ref.Backend] = struct{}{}
			current.Backends = append(current.Backends, ref)
		}
		out[name] = current
	}
	return out
}

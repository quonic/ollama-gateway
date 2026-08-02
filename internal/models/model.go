package models

// ModelEntry defines how a model is mapped to backends in the global catalog.
type ModelEntry struct {
	Name     string   // Canonical display name (defaults to map key)
	Backends []string // Backend names from config that serve this model
}

// ModelRegistry holds the global model catalog and provides lookup operations.
type ModelRegistry struct {
	models map[string]ModelEntry // keyed by canonical model name
}

// NewRegistry creates a registry from the given model entries. The map is copied
// so external mutations don't affect internal state.
func NewRegistry(entries map[string]ModelEntry) *ModelRegistry {
	m := &ModelRegistry{
		models: make(map[string]ModelEntry, len(entries)),
	}
	for k, v := range entries {
		if v.Name == "" {
			v.Name = k // canonical name defaults to key
		}
		m.models[k] = v
	}
	return m
}

// Get returns the catalog entry for a model name. Returns (entry, true) if found,
// or (zero value, false) otherwise. The caller should have already applied aliasing.
func (r *ModelRegistry) Get(name string) (ModelEntry, bool) {
	entry, ok := r.models[name]
	return entry, ok
}

// AllModels returns all registered model names in the catalog (sorted for deterministic output).
func (r *ModelRegistry) AllModels() []string {
	names := make([]string, 0, len(r.models))
	for name := range r.models {
		names = append(names, name)
	}
	return names
}

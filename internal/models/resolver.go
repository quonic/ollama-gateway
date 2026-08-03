package models

import (
	"ollama-gateway/internal/backends"
	"ollama-gateway/internal/config"
)

// UserOverrides holds per-user model access rules extracted from config.
type UserOverrides struct {
	AllowList []string          // Empty = inherit all global catalog models
	DenyList  []string          // Always applies if non-empty, overrides allow list
	Aliases   map[string]string // user-facing name → real canonical model name
}

// FromUserConfig converts a config.UserConfig into UserOverrides for the resolver.
func FromUserConfig(uc *config.UserConfig) UserOverrides {
	if uc == nil {
		return UserOverrides{}
	}
	return UserOverrides{
		AllowList: uc.ModelAllow,
		DenyList:  uc.ModelDeny,
		Aliases:   uc.Aliases,
	}
}

// Resolver ties together the model registry and backend manager to fully resolve
// a user's requested model name into an actual backend pool for proxying.
type Resolver struct {
	registry     *ModelRegistry
	manager      *backends.Manager
	modelWeights map[string][]config.ModelBackendRef // resolvedName → per-backend weight refs from config
}

// NewResolver creates a resolver from config, building both the model registry
// and backend manager from the same configuration source.
func NewResolver(cfg *config.Config) (*Resolver, error) {
	return NewResolverWithCatalog(cfg, CatalogFromConfig(cfg))
}

// NewResolverWithCatalog creates a resolver using a provided model catalog.
func NewResolverWithCatalog(cfg *config.Config, catalog map[string]config.ModelEntry) (*Resolver, error) {
	reg := buildRegistryFromCatalog(catalog)
	mgr, err := backends.NewManager(cfg)
	if err != nil {
		return nil, err
	}

	weights := make(map[string][]config.ModelBackendRef, len(catalog))
	for name, mc := range catalog {
		refs := make([]config.ModelBackendRef, 0, len(mc.Backends))
		for _, br := range mc.Backends {
			refs = append(refs, config.ModelBackendRef{
				Backend: br.Backend,
				Weight:  br.Weight,
			})
		}
		weights[name] = refs
	}

	return &Resolver{
		registry:     reg,
		manager:      mgr,
		modelWeights: weights,
	}, nil
}

// Resolve resolves a user's requested model name to a backend pool. The returned
// pool can be used for weighted round-robin selection via Select(). Returns a
// ResolutionError (with HTTP status code) on failure.
func (r *Resolver) Resolve(requestedModel string, overrides UserOverrides) (*backends.BackendPool, error) {
	resolvedName, backendNames, err := ResolveModel(requestedModel, r.registry, overrides)
	if err != nil {
		return nil, err
	}

	// Look up the model's per-backend weights from config and build refs.
	refs := make([]config.ModelBackendRef, 0, len(backendNames))
	for _, bn := range backendNames {
		w := backends.DefaultModelWeight // default if not explicitly set in catalog
		if weightRefs, ok := r.modelWeights[resolvedName]; ok {
			for _, wr := range weightRefs {
				if wr.Backend == bn && wr.Weight > 0 {
					w = wr.Weight
					break
				}
			}
		}
		refs = append(refs, config.ModelBackendRef{
			Backend: bn,
			Weight:  w,
		})
	}

	pool, err := r.manager.PoolForModel(refs)
	if err != nil {
		return nil, &ResolutionError{
			StatusCode: 500,
			Message:    "internal error: failed to build backend pool",
		}
	}

	_ = resolvedName // available for logging if needed downstream
	return pool, nil
}

// Registry returns the model registry (for /api/tags and dashboard use).
func (r *Resolver) Registry() *ModelRegistry {
	return r.registry
}

// Manager returns the backend manager (for health checks and admin operations).
func (r *Resolver) Manager() *backends.Manager {
	return r.manager
}

// CatalogFromConfig copies the model catalog from config for independent mutation.
func CatalogFromConfig(cfg *config.Config) map[string]config.ModelEntry {
	out := make(map[string]config.ModelEntry, len(cfg.Models.Models))
	for name, mc := range cfg.Models.Models {
		refs := make([]config.ModelBackendRef, 0, len(mc.Backends))
		for _, br := range mc.Backends {
			refs = append(refs, config.ModelBackendRef{Backend: br.Backend, Weight: br.Weight})
		}
		out[name] = config.ModelEntry{Name: mc.Name, Backends: refs}
	}
	return out
}

// buildRegistryFromCatalog converts config model entries into internal registry entries.
func buildRegistryFromCatalog(catalog map[string]config.ModelEntry) *ModelRegistry {
	entries := make(map[string]ModelEntry, len(catalog))
	for name, mc := range catalog {
		entry := ModelEntry{
			Name:     mc.Name,
			Backends: make([]string, 0, len(mc.Backends)),
		}
		if entry.Name == "" {
			entry.Name = name
		}
		for _, br := range mc.Backends {
			entry.Backends = append(entry.Backends, br.Backend)
		}
		entries[name] = entry
	}
	return NewRegistry(entries)
}

package models

import (
	"fmt"
)

// ResolutionError represents an error during model resolution, carrying the HTTP status code.
type ResolutionError struct {
	StatusCode int    // HTTP status to return to client
	Message    string // Human-readable error message for JSON response
}

func (e *ResolutionError) Error() string {
	return e.Message
}

// NewModelNotFoundError creates a 404 resolution error when the resolved model name doesn't exist.
func NewModelNotFoundError(requestedName string) *ResolutionError {
	return &ResolutionError{
		StatusCode: 404,
		Message:    fmt.Sprintf("model '%s' not found", requestedName),
	}
}

// NewModelDeniedError creates a 403 resolution error when the user's deny list blocks access.
func NewModelDeniedError(requestedName string) *ResolutionError {
	return &ResolutionError{
		StatusCode: 403,
		Message:    fmt.Sprintf("access to model '%s' is denied for your account", requestedName),
	}
}

// NewModelNotAllowedError creates a 403 resolution error when the allow list doesn't include the model.
func NewModelNotAllowedError(requestedName string, allowed []string) *ResolutionError {
	return &ResolutionError{
		StatusCode: 403,
		Message:    fmt.Sprintf("model '%s' is not available on your plan. Available: %v", requestedName, allowed),
	}
}

// ResolveModel implements the full model resolution flow per the spec:
//  1. Apply alias mapping (user-scoped)
//  2. Look up in global catalog → 404 if not found
//  3. Check deny list → 403 if denied
//  4. Check allow list → 403 if non-empty and model not included
//  5. Return resolved name + backend names from the catalog entry
func ResolveModel(requestedName string, registry *ModelRegistry, overrides UserOverrides) (resolvedName string, backends []string, err error) {
	// Step 1: Apply alias mapping if defined for this user.
	normalizedRequested := NormalizeModelName(requestedName)
	resolvedName = normalizedRequested
	if aliased, exists := overrides.Aliases[requestedName]; exists && aliased != "" {
		resolvedName = NormalizeModelName(aliased)
	} else if aliased, exists := overrides.Aliases[normalizedRequested]; exists && aliased != "" {
		resolvedName = NormalizeModelName(aliased)
	}

	// Step 2: Look up in global catalog (after aliasing).
	entry, found := registry.Get(resolvedName)
	if !found {
		return "", nil, NewModelNotFoundError(requestedName)
	}

	// Step 3: Check deny list — always applies even with no allow list.
	for _, denied := range overrides.DenyList {
		if NormalizeModelName(denied) == resolvedName {
			return "", nil, NewModelDeniedError(requestedName)
		}
	}

	// Step 4: Check allow list — if non-empty, model must be in it.
	if len(overrides.AllowList) > 0 {
		for _, allowed := range overrides.AllowList {
			if NormalizeModelName(allowed) == resolvedName {
				return resolvedName, entry.Backends, nil
			}
		}
		return "", nil, NewModelNotAllowedError(requestedName, overrides.AllowList)
	}

	// Step 5: No allow list restriction and not denied → return resolved model.
	return resolvedName, entry.Backends, nil
}

// VisibleModelsFor returns the set of models visible to a user after applying
// allow/deny/aliases. Used for /api/tags filtering and dashboard display.
func (r *ModelRegistry) VisibleModelsFor(overrides UserOverrides) []string {
	visible := make([]string, 0)
	denySet := make(map[string]struct{}, len(overrides.DenyList))
	for _, d := range overrides.DenyList {
		dn := NormalizeModelName(d)
		if dn != "" {
			denySet[dn] = struct{}{}
		}
	}
	allowSet := make(map[string]struct{}, len(overrides.AllowList))
	for _, a := range overrides.AllowList {
		an := NormalizeModelName(a)
		if an != "" {
			allowSet[an] = struct{}{}
		}
	}

	for name, entry := range r.models {
		// Check deny list — always applies.
		if _, denied := denySet[name]; denied {
			continue
		}

		// Check allow list — if non-empty, model must be in it.
		if len(allowSet) > 0 {
			if _, allowed := allowSet[name]; !allowed {
				continue
			}
		}

		visible = append(visible, entry.Name)
	}

	return visible
}

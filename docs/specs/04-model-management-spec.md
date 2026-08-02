# Model Management Specification

## 1. Overview

This document defines how models are registered, resolved, and routed within the Ollama Gateway.
It covers the global model catalog, per-user overrides (allow/deny lists and aliases), and the
resolution logic that maps a user-facing model name to backend servers.

---

## 2. Core Concepts

### Global Model Catalog

A list of all models available in the gateway's configuration. Each entry specifies which
backend(s) serve that model:

```yaml
models:
  global_catalog:
    - name: "llama3.2:latest"
      backends: ["ollama-a", "ollama-b"] # List of backend names from `backends:` section
      description: "Llama 3.2, latest version"

    - name: "gemma2:latest"
      backends: ["ollama-c"]
      description: "Google Gemma 2"
```

### Per-User Overrides

Each API key can have model access rules that override or refine the global catalog:

- **Allow list**: If specified, only these models are visible to this user. Models not in the
  allow list return HTTP 403 when requested (even if they exist globally).
- **Deny list**: Explicitly removes specific models from this user's view, regardless of whether
  an allow list is set.
- **Aliases**: Maps a user-facing name to a real model name that exists in the global catalog.

### Model Resolution Flow

When a request arrives for model `X` with API key belonging to user `U`:

1. Check if `X` matches any alias defined for user `U`. If so, resolve `X` → aliased model name `Y`.
2. Look up `Y` (or original `X`) in the global catalog:
   - If not found → return HTTP 404 `{"error": "model '...' not found"}`.
3. Check deny list for user `U`: if `Y` is denied → return HTTP 403.
4. Check allow list for user `U`: if an allow list exists and `Y` is not in it → return HTTP 403.
5. Return the resolved model name (the real one, after aliasing) along with its backend(s).

### Example Configuration

```yaml
models:
  global_catalog:
    - name: "llama3.2:latest"
      backends: ["ollama-a", "ollama-b"]
    - name: "gemma2:latest"
      backends: ["ollama-c"]
    - name: "codellama:latest"
      backends: ["ollama-d"]

api_keys:
  # User A can only use llama3.2 and gemma2; sees 'gpt-4' alias for llama3.2
  - id: "user-a"
    key_hash: "..."
    model_overrides:
      allow_list: ["llama3.2:latest", "gemma2:latest"]
      deny_list: []
      aliases:
        "gpt-4": "llama3.2:latest"

  # User B has access to all models except codellama; no aliases needed
  - id: "user-b"
    key_hash: "..."
    model_overrides:
      allow_list: [] # Empty = inherit global catalog (no restriction)
      deny_list: ["codellama:latest"] # But block this one

  # User C has full access, no restrictions
  - id: "user-c"
    key_hash: "..."
```

---

## 3. Detailed Resolution Logic

### Step-by-Step Algorithm

Given input `(requested_model_name, api_key)`:

```go
func ResolveModel(requestedName string, user UserContext) (resolvedName string, backends []Backend, err error) {
    // Step 1: Apply alias mapping if defined for this user
    resolvedName = requestedName
    if aliased, exists := user.Aliases[requestedName]; exists {
        resolvedName = aliased
    }

    // Step 2: Look up in global catalog
    entry, found := globalCatalog.Get(resolvedName)
    if !found {
        return "", nil, ModelNotFoundError{Model: requestedName}
    }

    // Step 3: Check deny list (applies even with no allow list)
    for _, denied := range user.DenyList {
        if denied == resolvedName {
            return "", nil, ModelDeniedError{Model: requestedName}
        }
    }

    // Step 4: Check allow list (if non-empty, model must be in it)
    if len(user.AllowList) > 0 {
        allowed := false
        for _, allowedModel := range user.AllowList {
            if allowedModel == resolvedName {
                allowed = true
                break
            }
        }
        if !allowed {
            return "", nil, ModelNotAllowedError{Model: requestedName}
        }
    }

    // Step 5: Return resolved model and its backends (from global catalog entry)
    return resolvedName, backendPool.GetBackends(entry.BackendNames), nil
}
```

### Error Responses

| Error Type        | HTTP Status | Response Body                                                               | When                                                                                                                             |
| ----------------- | ----------- | --------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Model Not Found   | 404         | `{"error": "model 'xyz' not found"}`                                        | Resolved name (after aliasing) doesn't exist in global catalog.                                                                  |
| Model Denied      | 403         | `{"error": "access to model 'xyz' is denied for your account"}`             | Model exists globally but user's deny list blocks it.                                                                            |
| Model Not Allowed | 403         | `{"error": "model 'xyz' is not available on your plan. Available: [list]"`} | Allow list is non-empty and doesn't include the requested model. Includes a helpful list of allowed models in the error message. |

### Alias Behavior Details

- Aliases are **user-scoped**: `gpt-4` can map to `llama3.2:latest` for user A, but have no
  meaning (or map elsewhere) for user B.
- The alias lookup happens **before** catalog lookup — the aliased name must exist in the global
  catalog; otherwise a "model not found" error is returned.
- Aliases do not affect what other users see: if user A aliases `gpt-4` → `llama3.2`, that alias
  only applies to requests authenticated with user A's key.

---

## 4. `/api/tags` Filtering Behavior

The Ollama `/api/tags` endpoint returns a list of all locally available models on the backend.
Since the gateway may route to multiple backends, and since users have different visibility:

### When User Has No Overrides (Inherits Global Catalog)

- The gateway fetches `/api/tags` from **all healthy backends** serving at least one model in the global catalog.
- It merges results, filters out models not present in any user's allow list or deny list.
  Wait — actually simpler: it returns only models that appear in the **global catalog**, since those are the ones we route to. Models on a backend but not registered in our config are hidden from all users.

### When User Has Allow List / Deny List / Aliases

- The gateway applies the same resolution logic as for inference requests, but at scale:
  - Start with global catalog models that exist on any healthy backend.
  - Remove denied models.
  - If allow list is non-empty, keep only those in it.
  - Apply aliases to rename returned model entries (so user sees `gpt-4` instead of `llama3.2:latest`).

### Implementation Approach for `/api/tags`

This endpoint requires special handling because it doesn't take a single model name — it returns
a list. The gateway will:

1. Determine the set of models visible to this user (apply allow/deny/aliases to global catalog).
2. For each visible model, fetch its details from one healthy backend that serves it (e.g., call `/api/show` on a representative backend).
3. Return an aggregated response in Ollama's format:

```json
{
  "models": [
    {
      "name": "gpt-4", // Aliased name if applicable
      "model": "llama3.2:latest", // Real backend model name
      "modified_at": "...",
      "size": 1234567890,
      "digest": "..."
    }
  ]
}
```

**Note**: Full implementation of `/api/tags` aggregation is a stretch goal for Phase 5. In the
initial proxy handler (Phase 5), if the model can't be resolved to a single backend via normal
routing logic, the gateway will forward the request to a default backend using weighted round-robin.

---

## 5. Model Registry Data Structures

### Global Catalog Entry

```go
type ModelEntry struct {
    Name        string   `yaml:"name" json:"name"`           // User-facing name (must match backend model)
    Backends    []string `yaml:"backends" json:"-"`          // Backend names from config
    Description string   `yaml:"description" json:"description,omitempty"`  // Optional, for dashboard display
}
```

### Per-User Model Overrides

```go
type ModelOverrides struct {
    AllowList []string          `yaml:"allow_list" json:"allow_list,omitempty"`  // Empty = inherit all
    DenyList  []string          `yaml:"deny_list" json:"deny_list,omitempty"`     // Always applies if non-empty
    Aliases   map[string]string `yaml:"aliases" json:"aliases,omitempty"`         // user-facing name -> real model name
}

func (mo *ModelOverrides) IsAllowed(modelName string, globalCatalog ModelRegistry) bool {
    // Deny list always takes precedence
    for _, denied := range mo.DenyList {
        if denied == modelName {
            return false
        }
    }

    // If allow list is non-empty, model must be in it
    if len(mo.AllowList) > 0 {
        for _, allowed := range mo.AllowList {
            if allowed == modelName {
                return true
            }
        }
        return false
    }

    return true  // No allow list restriction, not denied → allowed
}

func (mo *ModelOverrides) ResolveAlias(requested string) string {
    if aliased, exists := mo.Aliases[requested]; exists {
        return aliased
    }
    return requested
}
```

### Model Registry Interface

```go
type ModelRegistry interface {
    // Get returns the catalog entry for a model name (after aliasing applied by caller)
    Get(name string) (*ModelEntry, bool)

    // VisibleModelsFor returns the list of models visible to this user after applying
    // allow/deny/aliases. Used primarily for /api/tags filtering and dashboard display.
    VisibleModelsFor(overrides ModelOverrides) []string

    // AllModels returns all registered model names (for admin dashboard).
    AllModels() []string
}
```

---

## 6. Edge Cases & Validation Rules

| Rule                                                               | Behavior                                                                                                                                                        |
| ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Alias points to non-existent model                                 | Resolution fails with "model not found" error when user requests the alias. The alias itself is valid config but produces a runtime error.                      |
| Empty global catalog                                               | All API requests for models return 404 (except `/api/version`, `/api/ps` which don't require a specific model).                                                 |
| Model exists on backend but not in global catalog                  | Hidden from all users — never routed to, even if the user's allow list includes it. The gateway only routes models explicitly registered in config.             |
| Allow list and deny list both empty for a key                      | User inherits full global catalog access (no restrictions). This is the default behavior when `model_overrides` is omitted entirely from an API key definition. |
| Duplicate alias keys within one user's overrides                   | YAML parser will take the last value — this should be validated at config load time and produce a warning or error.                                             |
| Backend name in catalog entry doesn't exist in `backends:` section | Config validation fails at startup with an error message naming the missing backend.                                                                            |

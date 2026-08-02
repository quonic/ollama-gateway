package models

import (
	"testing"

	"ollama-gateway/internal/config"
)

func newTestRegistry() *ModelRegistry {
	return NewRegistry(map[string]ModelEntry{
		"llama3":    {Name: "llama3", Backends: []string{"backend-a"}},
		"gemma2":    {Name: "gemma2", Backends: []string{"backend-b"}},
		"codellama": {Name: "codellama", Backends: []string{"backend-c"}},
	})
}

func TestResolveModel_NoOverrides(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{} // empty = inherit all

	name, backends, err := ResolveModel("llama3", reg, overrides)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "llama3" {
		t.Errorf("expected resolved name 'llama3', got %q", name)
	}
	if len(backends) != 1 || backends[0] != "backend-a" {
		t.Errorf("unexpected backends: %v", backends)
	}
}

func TestResolveModel_ModelNotFound(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{}

	name, _, err := ResolveModel("nonexistent-model", reg, overrides)
	if name != "" || err == nil {
		t.Fatal("expected error for nonexistent model")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 404 {
		t.Errorf("expected status 404 for not found, got %d", re.StatusCode)
	}
}

func TestResolveModel_DenyList(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		DenyList: []string{"gemma2"},
	}

	name, _, err := ResolveModel("gemma2", reg, overrides)
	if name != "" || err == nil {
		t.Fatal("expected error when model is denied")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 403 {
		t.Errorf("expected status 403 for denied model, got %d", re.StatusCode)
	}
}

func TestResolveModel_AllowList_Permits(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		AllowList: []string{"llama3", "gemma2"},
	}

	name, backends, err := ResolveModel("llama3", reg, overrides)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "llama3" {
		t.Errorf("expected resolved name 'llama3', got %q", name)
	}
	if len(backends) != 1 || backends[0] != "backend-a" {
		t.Errorf("unexpected backends: %v", backends)
	}
}

func TestResolveModel_AllowList_Denies(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		AllowList: []string{"llama3"}, // gemma2 not in allow list
	}

	name, _, err := ResolveModel("gemma2", reg, overrides)
	if name != "" || err == nil {
		t.Fatal("expected error when model not in allow list")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 403 {
		t.Errorf("expected status 403 for not allowed model, got %d", re.StatusCode)
	}
}

func TestResolveModel_Alias(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		Aliases: map[string]string{
			"gpt-4": "llama3", // alias maps to a real model in the catalog
		},
	}

	name, backends, err := ResolveModel("gpt-4", reg, overrides)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "llama3" {
		t.Errorf("expected resolved alias to 'llama3', got %q", name)
	}
	if len(backends) != 1 || backends[0] != "backend-a" {
		t.Errorf("unexpected backends for aliased model: %v", backends)
	}
}

func TestResolveModel_AliasToNonExistent(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		Aliases: map[string]string{
			"fake-alias": "nonexistent-model", // alias points to model not in catalog
		},
	}

	name, _, err := ResolveModel("fake-alias", reg, overrides)
	if name != "" || err == nil {
		t.Fatal("expected error when alias resolves to nonexistent model")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 404 {
		t.Errorf("expected status 404 for alias to nonexistent model, got %d", re.StatusCode)
	}
}

func TestResolveModel_DenyListOverridesAllowList(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		// gemma2 is in both allow and deny list; deny should take precedence.
		AllowList: []string{"llama3", "gemma2"},
		DenyList:  []string{"gemma2"},
	}

	name, _, err := ResolveModel("gemma2", reg, overrides)
	if name != "" || err == nil {
		t.Fatal("expected error when model is both allowed and denied (deny should win)")
	}
	re, ok := err.(*ResolutionError)
	if !ok {
		t.Fatalf("expected *ResolutionError, got %T: %v", err, err)
	}
	if re.StatusCode != 403 {
		t.Errorf("expected status 403 for denied model (deny overrides allow), got %d", re.StatusCode)
	}
}

func TestVisibleModelsFor_NoOverrides(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{}

	visible := reg.VisibleModelsFor(overrides)
	if len(visible) != 3 {
		t.Errorf("expected all 3 models visible with no overrides, got %d: %v", len(visible), visible)
	}
}

func TestVisibleModelsFor_WithAllowList(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		AllowList: []string{"llama3"},
	}

	visible := reg.VisibleModelsFor(overrides)
	if len(visible) != 1 || visible[0] != "llama3" {
		t.Errorf("expected only 'llama3' visible, got %v", visible)
	}
}

func TestVisibleModelsFor_WithDenyList(t *testing.T) {
	reg := newTestRegistry()
	overrides := UserOverrides{
		DenyList: []string{"codellama"},
	}

	visible := reg.VisibleModelsFor(overrides)
	if len(visible) != 2 {
		t.Errorf("expected 2 models visible after deny, got %d: %v", len(visible), visible)
	}
	for _, v := range visible {
		if v == "codellama" {
			t.Error("denied model 'codellama' should not be in visible list")
		}
	}
}

func TestAllModels(t *testing.T) {
	reg := newTestRegistry()
	all := reg.AllModels()
	if len(all) != 3 {
		t.Errorf("expected 3 models, got %d", len(all))
	}
}

func TestFromUserConfig_Nil(t *testing.T) {
	overrides := FromUserConfig(nil)
	if overrides.AllowList != nil || overrides.DenyList != nil || overrides.Aliases != nil {
		t.Error("expected empty UserOverrides from nil config")
	}
}

func TestFromUserConfig_Populated(t *testing.T) {
	uc := &config.UserConfig{
		ModelAllow: []string{"llama3"},
		ModelDeny:  []string{"gemma2"},
		Aliases:    map[string]string{"alias": "real"},
	}
	overrides := FromUserConfig(uc)
	if len(overrides.AllowList) != 1 || overrides.AllowList[0] != "llama3" {
		t.Errorf("unexpected AllowList: %v", overrides.AllowList)
	}
	if len(overrides.DenyList) != 1 || overrides.DenyList[0] != "gemma2" {
		t.Errorf("unexpected DenyList: %v", overrides.DenyList)
	}
	if overrides.Aliases["alias"] != "real" {
		t.Errorf("unexpected Aliases: %v", overrides.Aliases)
	}
}

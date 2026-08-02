package usage

import (
	"testing"
)

func TestCalculateCost_ModelSpecificPricing(t *testing.T) {
	pc := &PricingConfig{
		ModelPricing: map[string]ModelPricing{
			"llama3.2:latest": {InputCostPer1M: 0.5, OutputCostPer1M: 1.5},
		},
	}

	// Prompt: 500 tokens @ $0.5/1M = $0.00025; Completion: 200 tokens @ $1.5/1M = $0.0003
	cost := pc.CalculateCost("llama3.2:latest", 500, 200)
	expected := 0.00055
	if cost != expected {
		t.Errorf("expected cost %.8f, got %.8f", expected, cost)
	}
}

func TestCalculateCost_DefaultPricingFallback(t *testing.T) {
	pc := &PricingConfig{
		DefaultInputPer1M:  0.3,
		DefaultOutputPer1M: 0.9,
		ModelPricing:       map[string]ModelPricing{}, // no model-specific entry
	}

	cost := pc.CalculateCost("unknown-model", 1_000_000, 500_000)
	// (1M / 1M) * 0.3 + (500K / 1M) * 0.9 = 0.3 + 0.45 = 0.75
	expected := 0.75
	if cost != expected {
		t.Errorf("expected cost %.8f, got %.8f", expected, cost)
	}
}

func TestCalculateCost_ZeroTokens(t *testing.T) {
	pc := &PricingConfig{
		DefaultInputPer1M:  0.5,
		DefaultOutputPer1M: 1.5,
	}

	cost := pc.CalculateCost("any-model", 0, 0)
	if cost != 0 {
		t.Errorf("expected zero cost for no tokens, got %.8f", cost)
	}
}

func TestCalculateCost_NoPricingConfigured(t *testing.T) {
	pc := &PricingConfig{} // all defaults are zero
	cost := pc.CalculateCost("unpriced-model", 1000, 500)
	if cost != 0.0 {
		t.Errorf("expected $0 for unpriced model with no defaults, got %.8f", cost)
	}
}

func TestCalculateCost_PartialModelPricingFallsBackToDefault(t *testing.T) {
	pc := &PricingConfig{
		DefaultInputPer1M:  0.2,
		DefaultOutputPer1M: 0.8,
		ModelPricing: map[string]ModelPricing{
			"partial-model": {InputCostPer1M: 0.5}, // only input set; output should use default
		},
	}

	cost := pc.CalculateCost("partial-model", 1_000_000, 1_000_000)
	// Input uses model-specific (0.5), output falls back to default (0.8) → total 1.3
	expected := 1.3
	if cost != expected {
		t.Errorf("expected cost %.8f, got %.8f", expected, cost)
	}
}

func TestCalculateCost_RoundingPrecision(t *testing.T) {
	pc := &PricingConfig{
		ModelPricing: map[string]ModelPricing{
			"m": {InputCostPer1M: 0.003, OutputCostPer1M: 0.002},
		},
	}

	cost := pc.CalculateCost("m", 7, 3) // very small values that could cause float noise
	if cost == 0 {
		t.Error("expected non-zero cost for positive token counts with positive rates")
	}
}

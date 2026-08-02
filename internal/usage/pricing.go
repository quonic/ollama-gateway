package usage

import (
	"math"
)

// PricingConfig defines cost per 1M tokens for prompt and eval output.
type PricingConfig struct {
	DefaultInputPer1M  float64                 `yaml:"default_input_per_1m_tokens"`
	DefaultOutputPer1M float64                 `yaml:"default_output_per_1m_tokens"`
	ModelPricing       map[string]ModelPricing `yaml:"models"`
}

// ModelPricing holds per-model pricing in USD per 1M tokens.
type ModelPricing struct {
	InputCostPer1M  float64 `yaml:"input_cost_per_1m_tokens"`
	OutputCostPer1M float64 `yaml:"output_cost_per_1m_tokens"`
}

// CalculateCost computes the cost for a single request using model-specific or default pricing.
// Formula: (promptTokens / 1e6) * inputRate + (completionTokens / 1e6) * outputRate
func (pc *PricingConfig) CalculateCost(model string, promptTokens, completionTokens int) float64 {
	pricing := pc.ModelPricing[model] // zero-value if not found

	inputRate := pricing.InputCostPer1M
	outputRate := pricing.OutputCostPer1M

	if inputRate == 0 {
		inputRate = pc.DefaultInputPer1M
	}
	if outputRate == 0 {
		outputRate = pc.DefaultOutputPer1M
	}

	cost := float64(promptTokens)/1_000_000.0*inputRate +
		float64(completionTokens)/1_000_000.0*outputRate

	return math.Round(cost*1e8) / 1e8 // round to nearest $0.00000001 (avoids floating-point noise in sums)
}

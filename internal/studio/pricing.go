package studio

import "strings"

// ModelPricing is the per-million-token cost in USD for a given model.
// Cache-read pricing is typically 75–90% off the input rate; cache-write is
// usually 25% above the input rate.
//
// These rates are *approximate* and reflect public pricing as of July 2026.
// Treat the displayed cost as a guideline, not a billing source of truth —
// the user's actual bill depends on each provider's billing rounding rules,
// promotional credits, and tier discounts. The frontend labels the figure
// with "≈" so users don't mistake it for an authoritative invoice.
type ModelPricing struct {
	InputPerMTok      float64 // $ per 1,000,000 input tokens (uncached)
	OutputPerMTok     float64 // $ per 1,000,000 output tokens
	CacheReadPerMTok  float64 // $ per 1,000,000 tokens read from prompt cache
	CacheWritePerMTok float64 // $ per 1,000,000 tokens written into prompt cache
}

// modelPricing maps a normalized lowercase model name to its pricing tier.
// Lookup tries (1) exact match, (2) any registered prefix match — so a
// model like "glm-5.1-experimental" still resolves to the GLM-5.1 tier.
//
// Sources (approximate, periodically drifts):
//   - GLM: docs.z.ai/guides/overview/pricing
//   - Kimi K3: kimi.com K3 API pricing; Kimi Code subscription use is shown as
//     an API-equivalent estimate because membership credits are not denominated
//     in dollars per token.
//
// All values are USD per million tokens.
var modelPricing = map[string]ModelPricing{
	// GLM family — official Z.AI prices. Cache creation is conservatively
	// charged at the uncached-input rate; cache storage is currently free.
	"glm-5.2":     {InputPerMTok: 1.40, OutputPerMTok: 4.40, CacheReadPerMTok: 0.26, CacheWritePerMTok: 1.40},
	"glm-5.1":     {InputPerMTok: 1.40, OutputPerMTok: 4.40, CacheReadPerMTok: 0.26, CacheWritePerMTok: 1.40},
	"glm-5":       {InputPerMTok: 1.00, OutputPerMTok: 3.20, CacheReadPerMTok: 0.20, CacheWritePerMTok: 1.00},
	"glm-5-turbo": {InputPerMTok: 1.20, OutputPerMTok: 4.00, CacheReadPerMTok: 0.24, CacheWritePerMTok: 1.20},
	"glm-4.7":     {InputPerMTok: 0.60, OutputPerMTok: 2.20, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.60},

	// Kimi K3 Open Platform equivalent: $3 cache miss, $0.30 cache hit,
	// $15 output per million tokens. K3-256k is the same model at a smaller
	// membership context/quota tier.
	"k3":                        {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.00},
	"k3-256k":                   {InputPerMTok: 3.00, OutputPerMTok: 15.00, CacheReadPerMTok: 0.30, CacheWritePerMTok: 3.00},
	"kimi-for-coding":           {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},
	"kimi-for-coding-highspeed": {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},
}

// LookupPricing returns the pricing tier for a GLM or Kimi model. Any other
// provider is deliberately rejected at this shared layer as well as at the
// Wails boundary so legacy callers cannot resurrect removed providers.
func LookupPricing(provider, model string) ModelPricing {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if providerDefinition(provider) == nil {
		return ModelPricing{}
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModelPricing{}
	}
	switch provider {
	case "glm":
		if !strings.HasPrefix(m, "glm-") {
			return ModelPricing{}
		}
	case "kimi":
		if m != "k3" && m != "k3-256k" && !strings.HasPrefix(m, "kimi-for-coding") {
			return ModelPricing{}
		}
	}
	if p, ok := modelPricing[m]; ok {
		return p
	}
	// Prefix match: "glm-5.1-some-experimental-suffix" → glm-5.1 tier.
	// Pick the LONGEST matching prefix so we don't accidentally match
	// "glm-4" when "glm-4.6" was intended.
	bestLen := 0
	var best ModelPricing
	for prefix, p := range modelPricing {
		if strings.HasPrefix(m, prefix) && len(prefix) > bestLen {
			best = p
			bestLen = len(prefix)
		}
	}
	return best
}

// EstimateCost computes a USD figure for one turn given the token counts
// reported by the provider. Returns 0 for unsupported/unknown models or zero
// usage so the caller can suppress the display.
func EstimateCost(provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	p := LookupPricing(provider, model)
	if p == (ModelPricing{}) {
		return 0
	}
	const million = 1_000_000.0
	cost := 0.0
	// Cache-read tokens are billed at the discounted cache rate (when known)
	// and shouldn't also be counted as input. Providers report `inputTokens`
	// inclusive of cache reads in some cases; the heuristic here treats the
	// reported numbers at face value (provider-side accounting handles the
	// dedup) which matches what the chat:usage event already shows.
	cost += float64(inputTokens) * p.InputPerMTok / million
	cost += float64(outputTokens) * p.OutputPerMTok / million
	if p.CacheReadPerMTok > 0 {
		cost += float64(cacheReadTokens) * p.CacheReadPerMTok / million
	}
	if p.CacheWritePerMTok > 0 {
		cost += float64(cacheWriteTokens) * p.CacheWritePerMTok / million
	}
	return cost
}

// EstimateCost is the Wails-bound entry point for the frontend so it can
// query the cost of a hypothetical (provider, model, tokens...) combo
// without duplicating the pricing table in TypeScript. The price table
// drifts; keeping a single source of truth in Go means a release that
// updates rates ships once.
func (s *Studio) EstimateCost(provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) float64 {
	if s.validateAvailableStudioProviderModel(provider, model) != nil {
		return 0
	}
	return EstimateCost(provider, model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

// GetModelPricing exposes the full pricing struct (per-million-token rates)
// for a given (provider, model) pair. iter 1050+ uses this to compute a
// LIVE cost preview in the chat input footer as the user types: backend
// is called once when the active project's model changes; the frontend
// then multiplies cached rates by the running token estimate locally so
// every keystroke doesn't cross the Wails bridge.
//
// Returns a zero ModelPricing struct (all fields 0) for unsupported models.
// The frontend treats all-zero as "no cost
// preview" and hides the chip — which matches the desired behavior
// (showing "≈$0.0000" on every keystroke is just noise).
func (s *Studio) GetModelPricing(provider, model string) ModelPricing {
	if s.validateAvailableStudioProviderModel(provider, model) != nil {
		return ModelPricing{}
	}
	return LookupPricing(provider, model)
}

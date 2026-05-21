package studio

import "strings"

// ModelPricing is the per-million-token cost in USD for a given model.
// Cache-read pricing is typically 75–90% off the input rate; cache-write is
// usually 25% above the input rate. Ollama (local) is free across the board.
//
// These rates are *approximate* and reflect public pricing as of late 2025.
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
// - GLM (Zhipu): bigmodel.cn pricing page
// - MiniMax (abab models): minimax.io pricing page
// - Kimi (kimi.com/coding): coding-platform pricing
// - Ollama: 0 (runs locally)
//
// All values are USD per million tokens.
var modelPricing = map[string]ModelPricing{
	// GLM family — bigmodel.cn published rates, USD-equivalent.
	// glm-5.1 / glm-5 are GLM's flagship coding/reasoning model.
	"glm-5.1":     {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},
	"glm-5":       {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},
	"glm-5-turbo": {InputPerMTok: 0.30, OutputPerMTok: 1.20, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.38},
	"glm-4.7":     {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},
	"glm-4.6":     {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},
	"glm-4.5":     {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},
	"glm-4.5-air": {InputPerMTok: 0.20, OutputPerMTok: 1.10, CacheReadPerMTok: 0.04, CacheWritePerMTok: 0.25},
	"glm-4":       {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69},

	// MiniMax abab series — minimax.io pricing.
	"abab7-chat-pro":  {InputPerMTok: 1.00, OutputPerMTok: 2.00},
	"abab6.5-chat":    {InputPerMTok: 0.85, OutputPerMTok: 1.70},
	"abab6.5s-chat":   {InputPerMTok: 0.30, OutputPerMTok: 0.60},
	"abab6-chat":      {InputPerMTok: 0.50, OutputPerMTok: 1.00},
	"minimax-m1":      {InputPerMTok: 1.00, OutputPerMTok: 2.00},
	"minimax-text-01": {InputPerMTok: 0.85, OutputPerMTok: 1.70},

	// Kimi (Moonshot) — kimi.com coding-platform pricing for the k2.6 model.
	"kimi-for-coding": {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},
	"kimi-k2.6":       {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},
	"kimi-k2.5":       {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},
	"kimi-k2":         {InputPerMTok: 0.60, OutputPerMTok: 2.50, CacheReadPerMTok: 0.06, CacheWritePerMTok: 0.75},

	// DeepSeek V4 — both V4 variants share cache pricing; only base in/out
	// rates differ. Pro is the flagship with thinking; flash is the
	// economical non-thinking variant. Cache pricing is the DeepSeek
	// "cache hit" rate published with the V4 release.
	"deepseek-v4-pro":   {InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.07, CacheWritePerMTok: 0.55},
	"deepseek-v4-flash": {InputPerMTok: 0.27, OutputPerMTok: 1.10, CacheReadPerMTok: 0.03, CacheWritePerMTok: 0.27},
}

// LookupPricing returns the pricing tier for a model. Falls back to a
// zero-cost tier for Ollama (local) and any unknown model — better to show
// "≈$0.00" than a wrong number. The frontend can also use the zero return
// to suppress the cost chip entirely.
func LookupPricing(provider, model string) ModelPricing {
	if provider == "ollama" {
		return ModelPricing{} // local inference — no per-token cost
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModelPricing{}
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
// reported by the provider. Returns 0 for unknown models / Ollama / zero
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
	return EstimateCost(provider, model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

// GetModelPricing exposes the full pricing struct (per-million-token rates)
// for a given (provider, model) pair. iter 1050+ uses this to compute a
// LIVE cost preview in the chat input footer as the user types: backend
// is called once when the active project's model changes; the frontend
// then multiplies cached rates by the running token estimate locally so
// every keystroke doesn't cross the Wails bridge.
//
// Returns a zero ModelPricing struct (all fields 0) for unknown models or
// local providers like Ollama. The frontend treats all-zero as "no cost
// preview" and hides the chip — which matches the desired behavior
// (showing "≈$0.0000" on every keystroke is just noise).
func (s *Studio) GetModelPricing(provider, model string) ModelPricing {
	return LookupPricing(provider, model)
}

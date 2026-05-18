package studio

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestLookupPricing_OllamaIsZero(t *testing.T) {
	p := LookupPricing("ollama", "llama3.1")
	if p != (ModelPricing{}) {
		t.Errorf("ollama got non-zero pricing: %+v", p)
	}
}

func TestLookupPricing_UnknownIsZero(t *testing.T) {
	p := LookupPricing("zzz", "no-such-model")
	if p != (ModelPricing{}) {
		t.Errorf("unknown got non-zero pricing: %+v", p)
	}
}

func TestLookupPricing_ExactMatch(t *testing.T) {
	p := LookupPricing("glm", "glm-5.1")
	if p.InputPerMTok != 0.55 || p.OutputPerMTok != 2.19 {
		t.Errorf("glm-5.1 pricing wrong: %+v", p)
	}
}

func TestLookupPricing_LongestPrefixWins(t *testing.T) {
	// "glm-4.6" should resolve to glm-4.6 tier even though "glm-4" and "glm"
	// are both registered prefixes — the longest match must win.
	p := LookupPricing("glm", "glm-4.6")
	if p.InputPerMTok != 0.55 {
		t.Errorf("glm-4.6 should match glm-4.6 tier (input 0.55), got %+v", p)
	}
	// "glm-5.1-experimental" → glm-5.1 (longest matching prefix), not glm-5.
	p2 := LookupPricing("glm", "glm-5.1-experimental")
	if p2 != modelPricing["glm-5.1"] {
		t.Errorf("glm-5.1-experimental should fall through to glm-5.1 tier, got %+v", p2)
	}
}

func TestLookupPricing_CaseInsensitive(t *testing.T) {
	a := LookupPricing("glm", "GLM-5.1")
	b := LookupPricing("glm", "glm-5.1")
	if a != b {
		t.Errorf("case-insensitive lookup failed: %+v vs %+v", a, b)
	}
}

func TestLookupPricing_EmptyModelIsZero(t *testing.T) {
	p := LookupPricing("glm", "")
	if p != (ModelPricing{}) {
		t.Errorf("empty model got non-zero pricing: %+v", p)
	}
	// Whitespace-only is also empty after TrimSpace.
	p2 := LookupPricing("glm", "   ")
	if p2 != (ModelPricing{}) {
		t.Errorf("whitespace model got non-zero pricing: %+v", p2)
	}
}

func TestEstimateCost_Ollama(t *testing.T) {
	// Local inference is free regardless of token counts.
	got := EstimateCost("ollama", "llama3.1", 1_000_000, 500_000, 0, 0)
	if got != 0 {
		t.Errorf("ollama cost = $%f, want 0", got)
	}
}

func TestEstimateCost_GLM5_1(t *testing.T) {
	// 1M input + 1M output at GLM-5.1 rates: 0.55 + 2.19 = 2.74.
	got := EstimateCost("glm", "glm-5.1", 1_000_000, 1_000_000, 0, 0)
	want := 0.55 + 2.19
	if !approxEqual(got, want) {
		t.Errorf("glm-5.1 cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_WithCache(t *testing.T) {
	// 100K input + 50K output + 1M cache read at GLM-5.1 rates:
	// 100k * 0.55/M + 50k * 2.19/M + 1M * 0.11/M = 0.055 + 0.1095 + 0.11 = 0.2745
	got := EstimateCost("glm", "glm-5.1", 100_000, 50_000, 1_000_000, 0)
	want := 100_000*0.55/1e6 + 50_000*2.19/1e6 + 1_000_000*0.11/1e6
	if !approxEqual(got, want) {
		t.Errorf("cache-read cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_CacheWrite(t *testing.T) {
	got := EstimateCost("glm", "glm-5.1", 0, 0, 0, 1_000_000)
	want := 0.69 // 1M * 0.69/M
	if !approxEqual(got, want) {
		t.Errorf("cache-write cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_CacheZeroForModelsWithoutCache(t *testing.T) {
	// MiniMax abab models don't have cache pricing — the cache-token amounts
	// should contribute nothing rather than pricing them at $0/M_in
	// (which would be wrong for true cache reads, although here it's the
	// same since their cache prices are unset → 0).
	got := EstimateCost("minimax", "abab7-chat-pro", 1_000_000, 0, 1_000_000, 1_000_000)
	want := 1.0 // Only the 1M input tokens at $1/M; cache fields don't apply.
	if !approxEqual(got, want) {
		t.Errorf("minimax cost = %f, want %f (cache-* should be ignored)", got, want)
	}
}

func TestEstimateCost_UnknownModelZero(t *testing.T) {
	got := EstimateCost("nope", "no-such", 1_000_000, 1_000_000, 0, 0)
	if got != 0 {
		t.Errorf("unknown model cost = %f, want 0", got)
	}
}

func TestEstimateCost_Kimi(t *testing.T) {
	// Kimi for coding: 0.6 in / 2.5 out per million.
	// 100K in + 200K out = 0.06 + 0.5 = 0.56
	got := EstimateCost("kimi", "kimi-for-coding", 100_000, 200_000, 0, 0)
	want := 0.06 + 0.5
	if !approxEqual(got, want) {
		t.Errorf("kimi cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_ZeroTokensIsZero(t *testing.T) {
	got := EstimateCost("glm", "glm-5.1", 0, 0, 0, 0)
	if got != 0 {
		t.Errorf("zero-token cost = %f, want 0", got)
	}
}

// TestStudioEstimateCost_WailsBindingPassthrough verifies the Studio method
// is a thin passthrough to the package-level EstimateCost. The frontend
// calls `Studio.EstimateCost(...)` via Wails, so this is the entry point
// exposed to TS.
func TestStudioEstimateCost_WailsBindingPassthrough(t *testing.T) {
	s := &Studio{}
	a := s.EstimateCost("glm", "glm-5.1", 1_000_000, 1_000_000, 0, 0)
	b := EstimateCost("glm", "glm-5.1", 1_000_000, 1_000_000, 0, 0)
	if !approxEqual(a, b) {
		t.Errorf("studio binding diverged from package fn: %f vs %f", a, b)
	}
}

// TestEstimateCost_GLM4_5Air verifies the cheaper GLM tier resolves
// correctly — important so users on the cost-optimized tier see lower
// numbers, not the flagship price.
func TestEstimateCost_GLM4_5Air(t *testing.T) {
	got := EstimateCost("glm", "glm-4.5-air", 1_000_000, 1_000_000, 0, 0)
	want := 0.20 + 1.10
	if !approxEqual(got, want) {
		t.Errorf("glm-4.5-air cost = %f, want %f", got, want)
	}
}

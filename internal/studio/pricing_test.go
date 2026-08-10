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
	if p.InputPerMTok != 1.40 || p.OutputPerMTok != 4.40 {
		t.Errorf("glm-5.1 pricing wrong: %+v", p)
	}
}

func TestLookupPricing_LongestPrefixWins(t *testing.T) {
	// "glm-5-turbo-preview" must resolve to the longer turbo tier rather than
	// the flagship "glm-5" prefix.
	p := LookupPricing("glm", "glm-5-turbo-preview")
	if p.InputPerMTok != 1.20 {
		t.Errorf("glm-5-turbo-preview should match turbo tier (input 1.20), got %+v", p)
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
	// 1M input + 1M output at GLM-5.1 rates.
	got := EstimateCost("glm", "glm-5.1", 1_000_000, 1_000_000, 0, 0)
	want := 1.40 + 4.40
	if !approxEqual(got, want) {
		t.Errorf("glm-5.1 cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_WithCache(t *testing.T) {
	// 100K input + 50K output + 1M cache read at GLM-5.1 rates.
	got := EstimateCost("glm", "glm-5.1", 100_000, 50_000, 1_000_000, 0)
	want := 100_000*1.40/1e6 + 50_000*4.40/1e6 + 1_000_000*0.26/1e6
	if !approxEqual(got, want) {
		t.Errorf("cache-read cost = %f, want %f", got, want)
	}
}

func TestEstimateCost_CacheWrite(t *testing.T) {
	got := EstimateCost("glm", "glm-5.1", 0, 0, 0, 1_000_000)
	want := 1.40
	if !approxEqual(got, want) {
		t.Errorf("cache-write cost = %f, want %f", got, want)
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

// TestEstimateCost_GLM5_2 verifies the new flagship default resolves to the
// current official tier rather than falling through to $0.
func TestEstimateCost_GLM5_2(t *testing.T) {
	got := EstimateCost("glm", "glm-5.2", 1_000_000, 1_000_000, 0, 0)
	want := 1.40 + 4.40
	if !approxEqual(got, want) {
		t.Errorf("glm-5.2 cost = %f, want %f", got, want)
	}
	// Prefix match: a future glm-5.1-* variant still resolves to the same tier.
	if EstimateCost("glm", "glm-5.1-preview", 1_000_000, 0, 0, 0) != 1.40 {
		t.Errorf("glm-5.1-preview should resolve to the glm-5.1 tier (1.40 input)")
	}
}

func TestEstimateCost_KimiK3CurrentRates(t *testing.T) {
	got := EstimateCost("kimi", "k3", 1_000_000, 1_000_000, 1_000_000, 0)
	want := 3.00 + 15.00 + 0.30
	if !approxEqual(got, want) {
		t.Errorf("Kimi K3 cost = %f, want %f", got, want)
	}
	if got256 := LookupPricing("kimi", "k3-256k"); got256 != modelPricing["k3"] {
		t.Errorf("k3-256k pricing = %+v, want K3 tier %+v", got256, modelPricing["k3"])
	}
}

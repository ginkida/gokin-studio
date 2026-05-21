package studio

import "testing"

// TestStudioGetModelPricing_KnownModel: known GLM-5.1 model returns the
// full pricing struct with non-zero rates. The frontend uses this to
// compute a live cost preview in the chat input footer (iter 1050+).
func TestStudioGetModelPricing_KnownModel(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("glm", "glm-5.1")
	want := ModelPricing{InputPerMTok: 0.55, OutputPerMTok: 2.19, CacheReadPerMTok: 0.11, CacheWritePerMTok: 0.69}
	if got != want {
		t.Errorf("GetModelPricing glm/glm-5.1 = %+v, want %+v", got, want)
	}
}

// TestStudioGetModelPricing_OllamaIsZero: local provider Ollama returns
// all-zero pricing. The frontend treats all-zero as "no cost preview"
// and hides the chip — matching the iter 290+ behavior for completed
// turns.
func TestStudioGetModelPricing_OllamaIsZero(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("ollama", "qwen3")
	if got != (ModelPricing{}) {
		t.Errorf("Ollama pricing should be all zero, got %+v", got)
	}
}

// TestStudioGetModelPricing_UnknownIsZero: an unknown model returns
// zero pricing rather than erroring. The frontend silently hides the
// cost chip when the active model isn't priced; better UX than a
// chip showing "$NaN".
func TestStudioGetModelPricing_UnknownIsZero(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("acme", "made-up-model")
	if got != (ModelPricing{}) {
		t.Errorf("unknown model pricing should be all zero, got %+v", got)
	}
}

// TestStudioGetModelPricing_PrefixMatchKeepsFlagshipRates: an unknown
// variant of GLM-5.1 (e.g. "glm-5.1-experimental") resolves to the
// flagship pricing — verifies the longest-prefix-wins lookup in
// LookupPricing still applies through the Wails binding.
func TestStudioGetModelPricing_PrefixMatchKeepsFlagshipRates(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("glm", "glm-5.1-experimental")
	if got.InputPerMTok != 0.55 {
		t.Errorf("expected glm-5.1 input rate via prefix match, got %f", got.InputPerMTok)
	}
}

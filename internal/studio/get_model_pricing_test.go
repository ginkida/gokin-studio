package studio

import "testing"

// TestStudioGetModelPricing_KnownModel: known GLM-5.1 model returns the
// full pricing struct with non-zero rates. The frontend uses this to
// compute a live cost preview in the chat input footer (iter 1050+).
func TestStudioGetModelPricing_KnownModel(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("glm", "glm-5.1")
	want := ModelPricing{InputPerMTok: 1.40, OutputPerMTok: 4.40, CacheReadPerMTok: 0.26, CacheWritePerMTok: 1.40}
	if got != want {
		t.Errorf("GetModelPricing glm/glm-5.1 = %+v, want %+v", got, want)
	}
}

func TestStudioGetModelPricing_RemovedProviderIsZero(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("ollama", "qwen3")
	if got != (ModelPricing{}) {
		t.Errorf("removed provider pricing should be all zero, got %+v", got)
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

func TestStudioGetModelPricing_RejectsUnlistedVariant(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("glm", "glm-5.1-experimental")
	if got != (ModelPricing{}) {
		t.Errorf("unlisted GLM variant should be rejected at Wails boundary, got %+v", got)
	}
}

func TestStudioGetModelPricing_KimiK3(t *testing.T) {
	s := &Studio{}
	got := s.GetModelPricing("kimi", "k3")
	want := ModelPricing{InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3}
	if got != want {
		t.Errorf("GetModelPricing kimi/k3 = %+v, want %+v", got, want)
	}
}

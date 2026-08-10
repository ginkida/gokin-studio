package studio

import (
	"slices"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// TestGetProviders_PublicBoundaryIsGLMAndKimi locks the desktop product
// boundary. Generic engine clients may exist internally, but no third provider
// or undeclared model metadata may leak into any frontend picker.
func TestGetProviders_PublicBoundaryIsGLMAndKimi(t *testing.T) {
	s := NewStudio()
	providers := s.GetProviders()
	if len(providers) != 2 {
		t.Fatalf("public providers = %d, want exactly GLM and Kimi: %+v", len(providers), providers)
	}
	if providers[0].ID != "glm" || providers[1].ID != "kimi" {
		t.Fatalf("public provider order = [%s, %s], want [glm, kimi]", providers[0].ID, providers[1].ID)
	}
	for _, provider := range providers {
		detailIDs := make([]string, 0, len(provider.ModelDetails))
		for _, detail := range provider.ModelDetails {
			detailIDs = append(detailIDs, detail.ID)
		}
		if !slices.Equal(provider.Models, detailIDs) {
			t.Errorf("%s model list and capability details differ: models=%v details=%v",
				provider.ID, provider.Models, detailIDs)
		}
	}
}

// TestGetProviders_LineupMatchesEngine guards against the GetProviders drift
// that previously listed dead models. Every exposed
// model offered in the picker must exist in client.AvailableModels so the
// engine can actually construct it.
func TestGetProviders_LineupMatchesEngine(t *testing.T) {
	withTempConfigDir(t)
	s := NewStudio()
	avail := map[string]bool{}
	for _, m := range client.AvailableModels {
		avail[m.ID] = true
	}
	for _, p := range s.GetProviders() {
		for _, m := range p.Models {
			if !avail[m] {
				t.Errorf("provider %q offers model %q not in client.AvailableModels (drift)", p.ID, m)
			}
		}
	}
}

// TestGetProviders_GLMDefaultIsFirst verifies glm-5.2 leads the GLM list (the
// flagship default) and the retired glm-4-plus tier is gone.
func TestGetProviders_GLMDefaultIsFirst(t *testing.T) {
	withTempConfigDir(t)
	s := NewStudio()
	for _, p := range s.GetProviders() {
		if p.ID != "glm" {
			continue
		}
		if len(p.Models) == 0 || p.Models[0] != "glm-5.2" {
			t.Fatalf("GLM models should lead with glm-5.2, got %v", p.Models)
		}
		for _, m := range p.Models {
			if m == "glm-4-plus" || m == "glm-4-air" {
				t.Errorf("retired model %q still offered in GLM lineup", m)
			}
		}
		return
	}
	t.Fatal("glm provider not found")
}

func TestGetProviders_KimiK3IsFirst(t *testing.T) {
	withTempConfigDir(t)
	s := NewStudio()
	for _, p := range s.GetProviders() {
		if p.ID != "kimi" {
			continue
		}
		if len(p.Models) == 0 || p.Models[0] != "k3" {
			t.Fatalf("Kimi models should lead with k3, got %v", p.Models)
		}
		return
	}
	t.Fatal("kimi provider not found")
}

func TestGetProviders_CurrentFlagshipCapabilities(t *testing.T) {
	s := NewStudio()
	got := map[string]ProviderModelInfo{}
	for _, provider := range s.GetProviders() {
		for _, model := range provider.ModelDetails {
			got[provider.ID+"/"+model.ID] = model
		}
	}

	glm := got["glm/glm-5.2"]
	if !glm.Latest || !glm.Recommended || glm.ContextWindow != 1_000_000 ||
		glm.DefaultMaxOutputTokens != 131_072 || glm.ReasoningControl != "high / max" {
		t.Fatalf("GLM-5.2 capabilities drifted: %+v", glm)
	}
	kimi := got["kimi/k3"]
	if !kimi.Latest || !kimi.Recommended || kimi.ContextWindow != 1_048_576 ||
		kimi.DefaultMaxOutputTokens != 131_072 || kimi.ReasoningControl != "low / high / max" {
		t.Fatalf("Kimi K3 capabilities drifted: %+v", kimi)
	}
	if len(kimi.InputModalities) != 2 || kimi.InputModalities[1] != "image" {
		t.Fatalf("Kimi K3 modalities drifted: %v", kimi.InputModalities)
	}
}

// TestDefaultConfig_ModelIsGLM52 locks the current default model.
func TestDefaultConfig_ModelIsGLM52(t *testing.T) {
	c := defaultConfig()
	if c.Settings.DefaultModel != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2", c.Settings.DefaultModel)
	}
	if c.Settings.DefaultProvider != "glm" {
		t.Errorf("default provider = %q, want glm", c.Settings.DefaultProvider)
	}
}

package studio

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// TestGetProviders_LineupMatchesEngine guards against the GetProviders drift
// that previously listed dead models (glm-4-plus, MiniMax-M1). Every cloud
// model offered in the picker must exist in client.AvailableModels so the
// engine can actually construct it.
func TestGetProviders_LineupMatchesEngine(t *testing.T) {
	withTempConfigDir(t)
	s := NewStudio()
	avail := map[string]bool{}
	for _, m := range client.AvailableModels {
		avail[m.ID] = true
	}
	cloud := map[string]bool{"glm": true, "minimax": true, "kimi": true, "deepseek": true}
	for _, p := range s.GetProviders() {
		if !cloud[p.ID] {
			continue
		}
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

// TestDefaultConfig_ModelIsGLM52 locks the new default model.
func TestDefaultConfig_ModelIsGLM52(t *testing.T) {
	c := defaultConfig()
	if c.Settings.DefaultModel != "glm-5.2" {
		t.Errorf("default model = %q, want glm-5.2", c.Settings.DefaultModel)
	}
	if c.Settings.DefaultProvider != "glm" {
		t.Errorf("default provider = %q, want glm", c.Settings.DefaultProvider)
	}
}

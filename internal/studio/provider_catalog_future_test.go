package studio

import (
	"strings"
	"testing"
	"time"
)

func TestFutureStudioModelIDsStayInsideProviderBoundary(t *testing.T) {
	accepted := []struct{ provider, model string }{
		{"glm", "glm-5.3"},
		{"glm", "glm-6-turbo"},
		{"kimi", "k4"},
		{"kimi", "k4-256k"},
		{"kimi", "kimi-k4.1"},
	}
	for _, tc := range accepted {
		if err := validateStudioProviderModelRuntime(tc.provider, tc.model); err != nil {
			t.Errorf("future %s/%s rejected: %v", tc.provider, tc.model, err)
		}
		if err := validateStudioProviderModel(tc.provider, tc.model); err == nil {
			t.Errorf("unadvertised future %s/%s passed the static boundary", tc.provider, tc.model)
		}
	}

	rejected := []struct{ provider, model string }{
		{"glm", "claude-opus-4-6"},
		{"kimi", "gpt-5.4"},
		{"glm", "glm-5.3/../../secret"},
		{"kimi", "K4"},
		{"openai", "glm-5.3"},
		{"glm", "glm-5.1-experimental"},
		{"kimi", "kimi-k2.7"},
	}
	for _, tc := range rejected {
		if err := validateStudioProviderModelRuntime(tc.provider, tc.model); err == nil {
			t.Errorf("unsafe/out-of-boundary %s/%s accepted", tc.provider, tc.model)
		}
	}
}

func TestFutureModelDefinitionInheritsFlagshipCapabilities(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{{"glm", "glm-5.3"}, {"kimi", "k4"}} {
		definition := modelDefinition(tc.provider, tc.model)
		if definition == nil || definition.ContextWindow < 1_000_000 || definition.MaxOutputTokens != 131_072 {
			t.Errorf("%s/%s inferred capabilities = %+v", tc.provider, tc.model, definition)
			continue
		}
		if !definition.Latest || !definition.Recommended || !strings.Contains(definition.Description, "Account-advertised") {
			t.Errorf("%s/%s inferred flags = %+v", tc.provider, tc.model, definition)
		}
	}
}

func TestPopulateAvailableStudioModelsIncludesFutureFamilyOnly(t *testing.T) {
	info := &ProviderHealthInfo{Provider: "glm"}
	populateAvailableStudioModels(info, []byte(`{"data":[{"id":"glm-5.2"},{"id":"claude-opus-4-6"},{"id":"glm-5.3[1m]"}]}`))
	want := "glm-5.3,glm-5.2"
	if got := strings.Join(info.AvailableModels, ","); got != want {
		t.Fatalf("available models = %q, want %q", got, want)
	}
	if info.RecommendedModel != "glm-5.3" {
		t.Fatalf("recommended model = %q, want glm-5.3", info.RecommendedModel)
	}
}

func TestFutureModelRequiresAccountDiscoveryForPublicMutation(t *testing.T) {
	s := &Studio{}
	if err := s.validateAvailableStudioProviderModel("glm", "glm-5.3"); err == nil {
		t.Fatal("unadvertised future model unexpectedly passed public mutation boundary")
	}
	s.rememberAvailableStudioModels(&ProviderHealthInfo{
		Provider: "glm", OK: true, AvailableModels: []string{"glm-5.3"},
	})
	if err := s.validateAvailableStudioProviderModel("glm", "glm-5.3"); err != nil {
		t.Fatalf("account-advertised future model rejected: %v", err)
	}
	if err := s.validateAvailableStudioProviderModel("glm", "glm-6-unknown"); err == nil {
		t.Fatal("different unadvertised future model unexpectedly passed")
	}
	providers := s.GetProviders()
	for _, provider := range providers {
		if provider.ID != "glm" {
			continue
		}
		if len(provider.Models) == 0 || provider.Models[0] != "glm-5.3" {
			t.Fatalf("discovered future model not promoted in provider catalog: %v", provider.Models)
		}
		for _, detail := range provider.ModelDetails {
			if detail.ID == "glm-5.3" && (!detail.Latest || !detail.Recommended) {
				t.Fatalf("future model detail not promoted: %+v", detail)
			}
		}
		s.mu.Lock()
		s.discoveredModelsAt["glm"] = time.Now().Add(-studioModelDiscoveryTTL - time.Second)
		s.mu.Unlock()
		if err := s.validateAvailableStudioProviderModel("glm", "glm-5.3"); err == nil {
			t.Fatal("expired account discovery unexpectedly remained valid")
		}
		return
	}
	t.Fatal("GLM provider missing")
}

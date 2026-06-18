package client

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/config"
)

// TestSupportsKimiThinking pins the model-prefix match used to auto-enable
// Extended Thinking for Kimi Coding Plan. Drift here silently turns off
// thinking for every Kimi user, so the cases are explicit. (Ported from gokin.)
func TestSupportsKimiThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"kimi-for-coding", true},
		{"Kimi-for-Coding", true},  // case-insensitive
		{"kimi-k2.6", true},        // future K2.x variant
		{"kimi-k2-thinking", true}, // future K2 variant
		{"glm-5.1", false},
		{"glm-4.7", false},
		{"llama3.2", false},
		{"", false},
		{"random-nonsense", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := SupportsKimiThinking(c.model); got != c.want {
				t.Errorf("SupportsKimiThinking(%q) = %v, want %v", c.model, got, c.want)
			}
		})
	}
}

// TestSupportsDeepSeekThinking pins which DeepSeek models get Extended
// Thinking auto-enabled. V4 (pro + flash) and legacy reasoner: yes.
// deepseek-chat: no — it's a pure chat model and the API rejects
// thinking blocks on that route. (Ported from gokin; confirmed April 2026.)
func TestSupportsDeepSeekThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"deepseek-v4-pro", true},
		{"deepseek-v4-flash", true},
		{"DeepSeek-V4-Pro", true},   // case-insensitive
		{"deepseek-reasoner", true}, // legacy reasoner
		{"deepseek-chat", false},    // plain chat — endpoint rejects thinking
		{"deepseek", true},          // bare family name
		{"glm-5.1", false},
		{"kimi-for-coding", false},
		{"", false},
		{"random", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := SupportsDeepSeekThinking(c.model); got != c.want {
				t.Errorf("SupportsDeepSeekThinking(%q) = %v, want %v", c.model, got, c.want)
			}
		})
	}
}

// TestAnthropicClient_SupportsPromptCachingForDeepSeek pins that the prompt
// caching gate recognises the DeepSeek base URL. DeepSeek V4 honours
// cache_control markers and reports cache_read_input_tokens on repeat prefixes
// (1.2K-token system prompt → ~95% input-token reduction on the second call,
// confirmed April 2026 on the Anthropic-compat endpoint). (Ported from gokin.)
func TestAnthropicClient_SupportsPromptCachingForDeepSeek(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.deepseek.com/anthropic", true},
		{"https://api.deepseek.com/anthropic/v1/messages", true},
		{"https://api.kimi.com/coding", true},
		{"https://api.moonshot.ai/anthropic", true},
		{"https://api.minimax.io/anthropic", true},
		{DefaultAnthropicBaseURL, true},
		{"", true}, // empty defaults to Anthropic
		// GLM is known not to honour cache_control.
		{"https://api.z.ai/api/anthropic", false},
		// Unknown provider — conservative off.
		{"https://random.example.com/api", false},
	}
	for _, c := range cases {
		t.Run(c.baseURL, func(t *testing.T) {
			ac := &AnthropicClient{config: AnthropicConfig{BaseURL: c.baseURL}}
			if got := ac.supportsPromptCaching(); got != c.want {
				t.Errorf("supportsPromptCaching(BaseURL=%q) = %v, want %v",
					c.baseURL, got, c.want)
			}
		})
	}
}

// TestAvailableModels_IncludeProviderDefaults verifies that every non-optional
// provider's DefaultModel is registered in AvailableModels under the correct
// provider name. A drift here means the factory can build a client for a model
// the engine doesn't know about. (Ported from gokin model_registry_test.go.)
func TestAvailableModels_IncludeProviderDefaults(t *testing.T) {
	for _, provider := range config.Providers {
		if provider.KeyOptional {
			continue
		}
		if provider.DefaultModel == "" {
			t.Fatalf("provider %s has empty DefaultModel", provider.Name)
		}
		info, ok := GetModelInfo(provider.DefaultModel)
		if !ok {
			t.Fatalf("provider %s default model %q is not in AvailableModels", provider.Name, provider.DefaultModel)
		}
		if info.Provider != provider.Name {
			t.Fatalf("provider %s default model %q is registered under provider %q",
				provider.Name, provider.DefaultModel, info.Provider)
		}
	}
}

// TestAvailableModels_IncludePresets verifies that every model in
// config.ModelPresets (excluding Ollama, which uses dynamic model names)
// is registered in AvailableModels under the correct provider.
// (Ported from gokin model_registry_test.go.)
func TestAvailableModels_IncludePresets(t *testing.T) {
	for preset, model := range config.ModelPresets {
		if model.Provider == "ollama" {
			continue
		}
		info, ok := GetModelInfo(model.Name)
		if !ok {
			t.Fatalf("preset %s model %q is not in AvailableModels", preset, model.Name)
		}
		if info.Provider != model.Provider {
			t.Fatalf("preset %s model %q registered under %q, want %q",
				preset, model.Name, info.Provider, model.Provider)
		}
	}
}

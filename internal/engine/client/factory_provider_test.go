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

// newKimiTestConfig returns a minimal config that passes key validation so
// newKimiClient can construct a client without touching the network.
func newKimiTestConfig() *config.Config {
	return &config.Config{
		API: config.APIConfig{
			KimiKey: "sk-kimi-test-key-for-unit-test-12345",
		},
		Model: config.ModelConfig{
			Name:            "kimi-for-coding",
			MaxOutputTokens: 4096,
		},
	}
}

// newDeepSeekTestConfig mirrors newKimiTestConfig for the DeepSeek factory
// path — a minimal config that lets newDeepSeekClient build without network calls.
func newDeepSeekTestConfig() *config.Config {
	return &config.Config{
		API: config.APIConfig{
			DeepSeekKey: "sk-deepseek-test-key-for-unit-test-12345",
		},
		Model: config.ModelConfig{
			Name:            "deepseek-v4-pro",
			MaxOutputTokens: 4096,
		},
	}
}

// TestNewKimiClient_AutoEnablesThinking confirms the factory flips thinking on
// for kimi-for-coding when the user hasn't configured it. Without this guard a
// future config-refactor could silently drop the auto-enable and every new Kimi
// install would lose the reasoning stream. (Ported from gokin factory_test.go.)
func TestNewKimiClient_AutoEnablesThinking(t *testing.T) {
	cfg := newKimiTestConfig()
	c, err := newKimiClient(cfg, "kimi-for-coding")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac, ok := c.(*AnthropicClient)
	if !ok {
		t.Fatalf("expected *AnthropicClient, got %T", c)
	}
	if !ac.config.EnableThinking {
		t.Error("EnableThinking should auto-flip to true for kimi-for-coding when user hasn't configured it")
	}
	if ac.config.ThinkingBudget != defaultKimiThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d (auto-default)", ac.config.ThinkingBudget, defaultKimiThinkingBudget)
	}
}

// TestNewKimiClient_RespectsExplicitEnable verifies the auto-enable does not
// override a user who explicitly set EnableThinking=true with their own budget.
func TestNewKimiClient_RespectsExplicitEnable(t *testing.T) {
	cfg := newKimiTestConfig()
	cfg.Model.EnableThinking = true
	cfg.Model.ThinkingBudget = 2048
	c, err := newKimiClient(cfg, "kimi-for-coding")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if !ac.config.EnableThinking {
		t.Error("explicit EnableThinking=true should be preserved")
	}
	if ac.config.ThinkingBudget != 2048 {
		t.Errorf("explicit ThinkingBudget=2048 should be preserved, got %d", ac.config.ThinkingBudget)
	}
}

// TestNewKimiClient_DoesNotEnableForUnsupportedModel guards the prefix check
// — a non-Kimi model name accidentally routed to newKimiClient must not get
// thinking wired on.
func TestNewKimiClient_DoesNotEnableForUnsupportedModel(t *testing.T) {
	cfg := newKimiTestConfig()
	c, err := newKimiClient(cfg, "some-other-model")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.EnableThinking {
		t.Error("thinking should NOT auto-enable for non-Kimi model names")
	}
	if ac.config.ThinkingBudget != 0 {
		t.Errorf("ThinkingBudget should stay 0 for non-Kimi model, got %d", ac.config.ThinkingBudget)
	}
}

// TestNewKimiClient_RepairsOutOfRangeBudget: a hand-edited config.yaml with an
// out-of-range thinking_budget must not propagate to the provider (it would 400
// with a cryptic "budget_tokens out of range"). Factory normalizes to default.
func TestNewKimiClient_RepairsOutOfRangeBudget(t *testing.T) {
	cases := []struct {
		name     string
		existing int32
		want     int32
	}{
		{"way_below_min", 100, defaultKimiThinkingBudget},
		{"at_min_ok", 1024, 1024},
		{"in_range_preserved", 4096, 4096},
		{"at_max_ok", 65536, 65536},
		{"way_above_max", 1_000_000, defaultKimiThinkingBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newKimiTestConfig()
			cfg.Model.EnableThinking = true
			cfg.Model.ThinkingBudget = tc.existing
			c, err := newKimiClient(cfg, "kimi-for-coding")
			if err != nil {
				t.Fatalf("newKimiClient: %v", err)
			}
			ac := c.(*AnthropicClient)
			if ac.config.ThinkingBudget != tc.want {
				t.Errorf("budget = %d, want %d (configured was %d)",
					ac.config.ThinkingBudget, tc.want, tc.existing)
			}
		})
	}
}

// TestNewDeepSeekClient_AutoEnablesThinking confirms that a user who did not
// configure thinking still gets it on for V4 Pro. (Ported from gokin
// factory_deepseek_test.go.)
func TestNewDeepSeekClient_AutoEnablesThinking(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	c, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newDeepSeekClient: %v", err)
	}
	ac, ok := c.(*AnthropicClient)
	if !ok {
		t.Fatalf("expected *AnthropicClient, got %T", c)
	}
	if !ac.config.EnableThinking {
		t.Error("EnableThinking should auto-flip to true for deepseek-v4-pro")
	}
	if ac.config.ThinkingBudget != defaultDeepSeekThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d (auto-default)", ac.config.ThinkingBudget, defaultDeepSeekThinkingBudget)
	}
}

// TestNewDeepSeekClient_NoThinkingForChat confirms deepseek-chat is correctly
// left without a thinking budget — otherwise API requests to that route 400.
func TestNewDeepSeekClient_NoThinkingForChat(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	cfg.Model.Name = "deepseek-chat"
	c, err := newDeepSeekClient(cfg, "deepseek-chat")
	if err != nil {
		t.Fatalf("newDeepSeekClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.EnableThinking {
		t.Error("thinking must NOT auto-enable for deepseek-chat")
	}
	if ac.config.ThinkingBudget != 0 {
		t.Errorf("ThinkingBudget should stay 0 for deepseek-chat, got %d", ac.config.ThinkingBudget)
	}
}

// TestNewDeepSeekClient_MissingKey confirms the factory returns a user-friendly
// error when no API key is configured.
func TestNewDeepSeekClient_MissingKey(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{Name: "deepseek-v4-pro"},
	}
	_, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
	if err == nil {
		t.Fatal("expected error for missing DeepSeek key")
	}
}

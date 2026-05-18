package studio

import "testing"

// TestContextWindowForProvider verifies per-provider token-budget values.
// These are exposed in ProjectInfo.ContextWindow and drive the context gauge
// and overflow-warning thresholds on the frontend.
func TestContextWindowForProvider(t *testing.T) {
	cases := []struct {
		provider, model string
		want            int
	}{
		// Kimi-for-coding (Kimi-k2.6) has a 262K context window.
		{"kimi", "kimi-for-coding", 262144},
		// GLM-5.x always returns 128K regardless of sub-model name.
		{"glm", "glm-5.1", 128000},
		{"glm", "glm-5-flash", 128000},
		{"glm", "glm-4", 128000}, // non-5.x prefix also falls through to 128K
		// MiniMax (default case) → 128K.
		{"minimax", "minimax-text", 128000},
		// Unknown provider → 128K (same default).
		{"", "", 128000},
		{"anthropic", "claude-3", 128000},
		// Ollama: known model uses profile's context window.
		{"ollama", "llama3.1", 128000}, // llama3.1 profile = 128K
		{"ollama", "qwen2.5-coder", 32768},
		// Ollama: unknown model falls through to GetModelProfile's "unknown" 4096.
		{"ollama", "xyzzy-totally-unknown-model-v99", 4096},
	}

	for _, tc := range cases {
		got := contextWindowForProvider(tc.provider, tc.model)
		if got != tc.want {
			t.Errorf("contextWindowForProvider(%q, %q) = %d, want %d",
				tc.provider, tc.model, got, tc.want)
		}
	}
}

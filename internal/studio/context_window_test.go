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
		// GLM-5.2 ships a 1M context window; other GLM-5.x → 200K; GLM-4.x → 128K.
		{"glm", "glm-5.2", 1000000},
		{"glm", "glm-5.2-experimental", 1000000}, // prefix match still 1M
		{"glm", "glm-5.1", 200000},
		{"glm", "glm-5-flash", 200000},
		{"glm", "glm-4", 128000}, // non-5.x prefix falls through to 128K
		// MiniMax (default case) → 200K (M2.x).
		{"minimax", "MiniMax-M2.7", 204800},
		// Unknown provider → MiniMax default (204800).
		{"", "", 204800},
		{"anthropic", "claude-3", 204800},
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

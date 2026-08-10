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
		// Kimi Code standard/HighSpeed/K3-256K use 262K; K3 supports 1M.
		{"kimi", "kimi-for-coding", 262144},
		{"kimi", "kimi-for-coding-highspeed", 262144},
		{"kimi", "k3-256k", 262144},
		{"kimi", "k3", 1048576},
		// GLM-5.2 → 1M; GLM-5.x and GLM-4.7 → 200K.
		{"glm", "glm-5.2", 1000000},
		{"glm", "glm-5.1", 200000},
		{"glm", "glm-5-turbo", 200000},
		{"glm", "glm-4.7", 200000},
		{"glm", "glm-4", 0}, // unlisted models are outside the product contract
		// Providers outside the product contract have no usable context.
		{"", "", 0},
		{"deepseek", "deepseek-v4-pro", 0},
		{"ollama", "llama3.1", 0},
	}

	for _, tc := range cases {
		got := contextWindowForProvider(tc.provider, tc.model)
		if got != tc.want {
			t.Errorf("contextWindowForProvider(%q, %q) = %d, want %d",
				tc.provider, tc.model, got, tc.want)
		}
	}
}

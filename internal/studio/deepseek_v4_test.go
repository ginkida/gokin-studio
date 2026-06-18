package studio

import (
	"testing"
)

// TestLookupPricing_DeepSeekV4 verifies iter 940+ pricing entries for
// the V4 lineup. Pro is the flagship with thinking; flash is the
// economical non-thinking variant. Both share cache pricing tier.
func TestLookupPricing_DeepSeekV4(t *testing.T) {
	cases := []struct {
		model          string
		wantInput      float64
		wantOutput     float64
		wantCacheRead  float64
		wantCacheWrite float64
	}{
		{"deepseek-v4-pro", 0.55, 2.19, 0.07, 0.55},
		{"deepseek-v4-flash", 0.27, 1.10, 0.03, 0.27},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			p := LookupPricing("deepseek", c.model)
			if p.InputPerMTok != c.wantInput {
				t.Errorf("InputPerMTok=%v, want %v", p.InputPerMTok, c.wantInput)
			}
			if p.OutputPerMTok != c.wantOutput {
				t.Errorf("OutputPerMTok=%v, want %v", p.OutputPerMTok, c.wantOutput)
			}
			if p.CacheReadPerMTok != c.wantCacheRead {
				t.Errorf("CacheReadPerMTok=%v, want %v", p.CacheReadPerMTok, c.wantCacheRead)
			}
			if p.CacheWritePerMTok != c.wantCacheWrite {
				t.Errorf("CacheWritePerMTok=%v, want %v", p.CacheWritePerMTok, c.wantCacheWrite)
			}
		})
	}
}

// TestResolveProviderKey_DeepSeek verifies the iter 780+ env-var fallback
// chain works for DeepSeek (added in iter 940+).
func TestResolveProviderKey_DeepSeek(t *testing.T) {
	clearEnv(t, "DEEPSEEK_API_KEY")

	// Setting wins.
	withEnv(t, "DEEPSEEK_API_KEY", "from-env")
	key, src := ResolveProviderKey("deepseek", Settings{DeepSeekKey: "from-setting"})
	if key != "from-setting" || src != KeySourceSetting {
		t.Errorf("setting-set should win: key=%q src=%q", key, src)
	}

	// Env fallback when setting empty.
	key, src = ResolveProviderKey("deepseek", Settings{})
	if key != "from-env" || src != KeySourceEnv {
		t.Errorf("env fallback: key=%q src=%q", key, src)
	}

	// Both empty.
	clearEnv(t, "DEEPSEEK_API_KEY")
	key, src = ResolveProviderKey("deepseek", Settings{})
	if key != "" || src != KeySourceNone {
		t.Errorf("both empty: key=%q src=%q, want empty/none", key, src)
	}
}

func TestEnvVarForProvider_DeepSeek(t *testing.T) {
	if got := envVarForProvider("deepseek"); got != "DEEPSEEK_API_KEY" {
		t.Errorf("envVarForProvider(deepseek)=%q, want DEEPSEEK_API_KEY", got)
	}
}

// TestContextWindow_DeepSeek verifies DeepSeek V4 Pro/Flash expose the 1M context window.
func TestContextWindow_DeepSeek(t *testing.T) {
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		if got := contextWindowForProvider("deepseek", model); got != 1000000 {
			t.Errorf("contextWindowForProvider(deepseek, %q)=%d, want 1000000", model, got)
		}
	}
}

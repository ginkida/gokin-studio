package studio

import (
	"testing"
)

func TestLookupPricing_DeepSeekV4IsUnsupported(t *testing.T) {
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		if got := LookupPricing("deepseek", model); got != (ModelPricing{}) {
			t.Errorf("unsupported DeepSeek pricing for %q = %+v", model, got)
		}
	}
}

func TestResolveProviderKey_DeepSeekIsUnsupported(t *testing.T) {
	withEnv(t, "DEEPSEEK_API_KEY", "from-env")
	key, src := ResolveProviderKey("deepseek", Settings{})
	if key != "" || src != KeySourceNone {
		t.Errorf("unsupported provider resolved credentials: key=%q src=%q", key, src)
	}
}

func TestEnvVarForProvider_DeepSeekIsUnsupported(t *testing.T) {
	if got := envVarForProvider("deepseek"); got != "" {
		t.Errorf("envVarForProvider(deepseek)=%q, want empty", got)
	}
}

func TestContextWindow_DeepSeekIsUnsupported(t *testing.T) {
	for _, model := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		if got := contextWindowForProvider("deepseek", model); got != 0 {
			t.Errorf("contextWindowForProvider(deepseek, %q)=%d, want 0", model, got)
		}
	}
}

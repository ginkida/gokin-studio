package studio

import (
	"strings"
	"testing"
)

// Legacy provider IDs must not regain a runtime path through the diagnostics
// RPC after the desktop product was narrowed to GLM and Kimi.
func TestCheckProviderHealth_RejectsUnsupportedProvider(t *testing.T) {
	withTempConfigDir(t)
	s := NewStudio()
	for _, provider := range []string{"deepseek", "minimax", "ollama", "anthropic", "gemini"} {
		info := s.CheckProviderHealth(provider)
		if info.OK {
			t.Errorf("%s: expected OK=false", provider)
		}
		if !strings.Contains(strings.ToLower(info.Error), "unknown provider") {
			t.Errorf("%s: error = %q, want unknown provider", provider, info.Error)
		}
	}
}

package client

import "testing"

// TestNormalizeThinkingBudget guards the iter-1201 port from upstream gokin.
// The function repairs hand-edited config.yaml typos before an API call so
// providers don't reject the request with a cryptic 400.
func TestNormalizeThinkingBudget(t *testing.T) {
	const def int32 = 8192
	cases := []struct {
		name   string
		budget int32
		want   int32
	}{
		{"zero → auto-default", 0, def},
		{"below min (100) → auto-default", 100, def},
		{"exactly min (1024)", 1024, 1024},
		{"mid-range (4096)", 4096, 4096},
		{"default (8192)", 8192, 8192},
		{"exactly max (65536)", 65536, 65536},
		{"above max (70000) → auto-default", 70000, def},
		{"negative → auto-default", -1, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeThinkingBudget(tc.budget, def)
			if got != tc.want {
				t.Errorf("normalizeThinkingBudget(%d, %d) = %d, want %d", tc.budget, def, got, tc.want)
			}
		})
	}
}

func TestThinkingBudgetConstants(t *testing.T) {
	// Guard the actual constant values — a drift here would silently change
	// default reasoning quality for all providers that support thinking.
	if defaultGLMThinkingBudget != 8192 {
		t.Errorf("defaultGLMThinkingBudget = %d, want 8192", defaultGLMThinkingBudget)
	}
	if defaultKimiThinkingBudget != 8192 {
		t.Errorf("defaultKimiThinkingBudget = %d, want 8192", defaultKimiThinkingBudget)
	}
	if defaultDeepSeekThinkingBudget != 8192 {
		t.Errorf("defaultDeepSeekThinkingBudget = %d, want 8192", defaultDeepSeekThinkingBudget)
	}
	if defaultMiniMaxThinkingBudget != 8192 {
		t.Errorf("defaultMiniMaxThinkingBudget = %d, want 8192", defaultMiniMaxThinkingBudget)
	}
	if thinkingBudgetMin != 1024 {
		t.Errorf("thinkingBudgetMin = %d, want 1024", thinkingBudgetMin)
	}
	if thinkingBudgetMax != 65536 {
		t.Errorf("thinkingBudgetMax = %d, want 65536", thinkingBudgetMax)
	}
}

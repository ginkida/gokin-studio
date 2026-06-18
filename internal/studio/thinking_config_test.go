package studio

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// TestResolveThinkingConfig pins the ThinkingMode → (enable, budget) policy that
// initClient feeds the client factory. The standout case is GLM "disabled": it
// must resolve to the explicit-disable sentinel so the GLM factory auto-enable
// fallback can't silently turn thinking back on for the default provider.
func TestResolveThinkingConfig(t *testing.T) {
	glmDefault := client.DefaultThinkingBudget("glm") // 8192
	const otherDefault int32 = 4096

	cases := []struct {
		name       string
		mode       string
		provider   string
		model      string
		userBudget int32
		wantEnable bool
		wantBudget int32
	}{
		// disabled → sentinel for every auto-enabling provider (the audit's top fix)
		{"glm disabled", "disabled", "glm", "glm-5.2", 0, false, client.ThinkingDisabledSentinel},
		{"kimi disabled", "disabled", "kimi", "kimi-for-coding", 0, false, client.ThinkingDisabledSentinel},
		{"deepseek disabled", "disabled", "deepseek", "deepseek-v4-pro", 0, false, client.ThinkingDisabledSentinel},

		// enabled with no user budget → provider default (GLM 8192, not 4096)
		{"glm enabled no budget → 8192", "enabled", "glm", "glm-5.2", 0, true, glmDefault},
		{"kimi enabled no budget → 4096", "enabled", "kimi", "kimi-for-coding", 0, true, otherDefault},
		{"deepseek enabled no budget → 4096", "enabled", "deepseek", "deepseek-v4-pro", 0, true, otherDefault},

		// enabled with explicit user budget → preserved
		{"glm enabled budget 2048", "enabled", "glm", "glm-5.2", 2048, true, 2048},
		{"glm enabled budget 16384", "enabled", "glm", "glm-5.2", 16384, true, 16384},

		// auto: supported providers enable at their tuned default
		{"glm-5.2 auto → 8192", "", "glm", "glm-5.2", 0, true, glmDefault},
		{"glm-4.7 auto → 8192", "", "glm", "glm-4.7", 0, true, glmDefault},
		{"kimi auto → 4096", "", "kimi", "kimi-for-coding", 0, true, otherDefault},
		{"deepseek-v4-pro auto → 4096", "", "deepseek", "deepseek-v4-pro", 0, true, otherDefault},

		// auto: unsupported models / providers stay off
		{"glm-4.5 auto stays off", "", "glm", "glm-4.5", 0, false, 0},
		{"deepseek-chat auto stays off", "", "deepseek", "deepseek-chat", 0, false, 0},
		{"minimax auto stays off", "", "minimax", "MiniMax-M2.7", 0, false, 0},
		{"ollama auto stays off", "", "ollama", "llama3", 0, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enable, budget := resolveThinkingConfig(tc.mode, tc.provider, tc.model, tc.userBudget)
			if enable != tc.wantEnable {
				t.Errorf("enable = %v, want %v", enable, tc.wantEnable)
			}
			if budget != tc.wantBudget {
				t.Errorf("budget = %d, want %d", budget, tc.wantBudget)
			}
		})
	}
}

// TestResolveThinkingConfig_GLMAutoVsEnabledConsistent is the focused regression
// for the audit finding: toggling a GLM project between auto and explicitly-
// enabled must NOT change the thinking budget (was 8192 auto vs 4096 enabled).
func TestResolveThinkingConfig_GLMAutoVsEnabledConsistent(t *testing.T) {
	_, autoBudget := resolveThinkingConfig("", "glm", "glm-5.2", 0)
	enEnable, enabledBudget := resolveThinkingConfig("enabled", "glm", "glm-5.2", 0)
	if !enEnable {
		t.Fatal("enabled mode should enable thinking")
	}
	if autoBudget != enabledBudget {
		t.Errorf("GLM budget differs between auto (%d) and enabled (%d) — toggling silently changes it",
			autoBudget, enabledBudget)
	}
}

// TestProjectInfo_ThinkingActiveResolved verifies Info() exposes the RESOLVED
// thinking state (ThinkingActive / ThinkingBudgetEffective) so the UI badge
// reflects reality without re-deriving the per-provider auto-enable rules in
// TypeScript. GLM (the default) and DeepSeek V4 auto-enable in auto mode, not
// just Kimi — the previous frontend logic only recognized Kimi.
func TestProjectInfo_ThinkingActiveResolved(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		model      string
		mode       string
		budget     int32
		wantActive bool
		wantBudget int32
	}{
		{"glm-5.2 auto → active 8192", "glm", "glm-5.2", "", 0, true, 8192},
		{"glm disabled → off, budget 0", "glm", "glm-5.2", "disabled", 0, false, 0},
		{"glm enabled budget 2048", "glm", "glm-5.2", "enabled", 2048, true, 2048},
		{"glm enabled no budget → 8192", "glm", "glm-5.2", "enabled", 0, true, 8192},
		{"deepseek-v4-pro auto → active 4096", "deepseek", "deepseek-v4-pro", "", 0, true, 4096},
		{"kimi auto → active 4096", "kimi", "kimi-for-coding", "", 0, true, 4096},
		{"glm-4.5 auto → off (unsupported)", "glm", "glm-4.5", "", 0, false, 0},
		{"minimax auto → off", "minimax", "MiniMax-M2.7", "", 0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Project{
				ID:             "tp",
				Name:           "T",
				Directory:      t.TempDir(),
				Provider:       tc.provider,
				Model:          tc.model,
				ThinkingMode:   tc.mode,
				ThinkingBudget: tc.budget,
				sessions:       map[string]*ChatSession{"default": NewChatSession("c")},
			}
			info := p.Info()
			if info.ThinkingActive != tc.wantActive {
				t.Errorf("ThinkingActive = %v, want %v", info.ThinkingActive, tc.wantActive)
			}
			if info.ThinkingBudgetEffective != tc.wantBudget {
				t.Errorf("ThinkingBudgetEffective = %d, want %d", info.ThinkingBudgetEffective, tc.wantBudget)
			}
		})
	}
}

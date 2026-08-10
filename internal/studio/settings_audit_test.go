package studio

import (
	"strings"
	"testing"
)

func TestDiffSettings_NoChangesReturnsEmpty(t *testing.T) {
	s := Settings{
		Theme:                 "dark",
		DefaultProvider:       "glm",
		DefaultModel:          "glm-5.1",
		GLMKey:                "sk-x",
		DefaultThinkingMode:   "enabled",
		DefaultThinkingBudget: 4096,
		DefaultBudgetUSD:      5.0,
	}
	diff := diffSettings(s, s)
	if len(diff) != 0 {
		t.Errorf("expected no diffs for identical settings; got %d entries: %+v", len(diff), diff)
	}
}

func TestDiffSettings_AllNonSecretFields(t *testing.T) {
	oldS := Settings{}
	newS := Settings{
		Theme:                   "light",
		DefaultProvider:         "kimi",
		DefaultModel:            "kimi-for-coding",
		DefaultThinkingMode:     "enabled",
		DefaultThinkingBudget:   8192,
		DefaultBudgetUSD:        10.5,
		AutoCleanupDisabled:     true,
		KeepAwakeEnabled:        true,
		AutoUpdateCheckDisabled: true,
		AutoArchivePRAfterClose: true,
	}
	diff := diffSettings(oldS, newS)
	// Each field above should produce its own entry.
	expectedFields := []string{
		"theme", "defaultProvider", "defaultModel",
		"defaultThinkingMode", "defaultThinkingBudget", "defaultBudgetUSD",
		"autoCleanupDisabled", "keepAwakeEnabled", "autoUpdateCheckDisabled", "autoArchivePRAfterClose",
	}
	if len(diff) != len(expectedFields) {
		t.Fatalf("got %d diff entries, want %d: %+v", len(diff), len(expectedFields), diff)
	}
	for i, want := range expectedFields {
		if diff[i].Field != want {
			t.Errorf("entry %d: Field=%q, want %q", i, diff[i].Field, want)
		}
	}
}

func TestDiffSettings_APIKey_SetReportsNoValue(t *testing.T) {
	oldS := Settings{}
	newS := Settings{GLMKey: "sk-secret-do-not-log"}
	diff := diffSettings(oldS, newS)
	if len(diff) != 1 {
		t.Fatalf("expected 1 diff entry, got %d", len(diff))
	}
	if !strings.Contains(diff[0].Message, "set") {
		t.Errorf("message=%q, want to contain 'set'", diff[0].Message)
	}
	if strings.Contains(diff[0].Message, "sk-secret-do-not-log") {
		t.Errorf("MESSAGE LEAKS API KEY: %q — this is a SECURITY violation", diff[0].Message)
	}
}

func TestDiffSettings_APIKey_ClearedReportsClear(t *testing.T) {
	oldS := Settings{GLMKey: "sk-old"}
	newS := Settings{GLMKey: ""}
	diff := diffSettings(oldS, newS)
	if len(diff) != 1 {
		t.Fatalf("expected 1 diff entry, got %d", len(diff))
	}
	if !strings.Contains(diff[0].Message, "cleared") {
		t.Errorf("message=%q, want to contain 'cleared'", diff[0].Message)
	}
	if strings.Contains(diff[0].Message, "sk-old") {
		t.Errorf("MESSAGE LEAKS OLD API KEY: %q", diff[0].Message)
	}
}

func TestDiffSettings_APIKey_RotatedReportsUpdated(t *testing.T) {
	oldS := Settings{KimiKey: "sk-kimi-A"}
	newS := Settings{KimiKey: "sk-kimi-B"}
	diff := diffSettings(oldS, newS)
	if len(diff) != 1 {
		t.Fatalf("expected 1 diff entry, got %d", len(diff))
	}
	if !strings.Contains(diff[0].Message, "updated") {
		t.Errorf("message=%q, want to contain 'updated'", diff[0].Message)
	}
	if strings.Contains(diff[0].Message, "sk-kimi") {
		t.Errorf("MESSAGE LEAKS API KEY: %q", diff[0].Message)
	}
}

func TestDiffSettings_APIKey_BothEmptyNotLogged(t *testing.T) {
	oldS := Settings{}
	newS := Settings{}
	diff := diffSettings(oldS, newS)
	if len(diff) != 0 {
		t.Errorf("expected no entries when both keys empty; got %+v", diff)
	}
}

func TestDiffSettings_APIKey_UnchangedNonEmptyNotLogged(t *testing.T) {
	oldS := Settings{GLMKey: "sk-same"}
	newS := Settings{GLMKey: "sk-same"}
	diff := diffSettings(oldS, newS)
	if len(diff) != 0 {
		t.Errorf("expected no entries when key unchanged; got %+v", diff)
	}
}

func TestDiffSettings_GlobalInstructionsNeverLogsContent(t *testing.T) {
	const secret = "private-client-codename"
	diff := diffSettings(Settings{}, Settings{GlobalInstructions: secret})
	if len(diff) != 1 || diff[0].Field != "globalInstructions" {
		t.Fatalf("unexpected diff: %+v", diff)
	}
	if strings.Contains(diff[0].Message, secret) || !strings.Contains(diff[0].Message, "content not logged") {
		t.Fatalf("global instruction content leaked: %q", diff[0].Message)
	}
}

func TestDiffSettings_NumericFields(t *testing.T) {
	cases := []struct {
		name       string
		oldS, newS Settings
		wantMsg    string
	}{
		{"thinking budget", Settings{DefaultThinkingBudget: 1024}, Settings{DefaultThinkingBudget: 4096}, "1024 → 4096"},
		{"budget USD", Settings{DefaultBudgetUSD: 1.0}, Settings{DefaultBudgetUSD: 5.50}, "$1.00 → $5.50"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diff := diffSettings(c.oldS, c.newS)
			if len(diff) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(diff))
			}
			if !strings.Contains(diff[0].Message, c.wantMsg) {
				t.Errorf("Message=%q, want to contain %q", diff[0].Message, c.wantMsg)
			}
		})
	}
}

func TestDiffSettings_AutoCleanupToggleHasReadableState(t *testing.T) {
	diffOn := diffSettings(Settings{AutoCleanupDisabled: false}, Settings{AutoCleanupDisabled: true})
	if len(diffOn) != 1 || !strings.Contains(diffOn[0].Message, "disabled") {
		t.Errorf("enabling AutoCleanupDisabled should log 'disabled'; got %+v", diffOn)
	}
	diffOff := diffSettings(Settings{AutoCleanupDisabled: true}, Settings{AutoCleanupDisabled: false})
	if len(diffOff) != 1 || !strings.Contains(diffOff[0].Message, "enabled") {
		t.Errorf("disabling AutoCleanupDisabled should log 'enabled'; got %+v", diffOff)
	}
}

func TestDiffSettings_AutomaticUpdateToggleHasReadableState(t *testing.T) {
	diffOff := diffSettings(Settings{}, Settings{AutoUpdateCheckDisabled: true})
	if len(diffOff) != 1 || diffOff[0].Field != "autoUpdateCheckDisabled" || !strings.Contains(diffOff[0].Message, "disabled") {
		t.Errorf("opting out should log disabled; got %+v", diffOff)
	}
	diffOn := diffSettings(Settings{AutoUpdateCheckDisabled: true}, Settings{})
	if len(diffOn) != 1 || !strings.Contains(diffOn[0].Message, "enabled") {
		t.Errorf("opting back in should log enabled; got %+v", diffOn)
	}
}

func TestUpdateSettings_LogsToEventLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	// Apply a change.
	newCfg := StudioConfig{
		Settings: Settings{
			Theme:                 "light",
			DefaultProvider:       "kimi",
			DefaultModel:          "kimi-for-coding",
			DefaultThinkingMode:   "",
			DefaultThinkingBudget: 0,
			DefaultBudgetUSD:      0,
		},
	}
	// Match the existing defaults except for theme + provider/model.
	newCfg.Settings.DefaultProvider = "kimi"
	newCfg.Settings.DefaultModel = "kimi-for-coding"
	if err := s.UpdateSettings(newCfg); err != nil {
		t.Fatal(err)
	}
	logs := s.GetRecentLogs()
	foundTheme := false
	foundProv := false
	for _, l := range logs {
		if l.Source != "settings" {
			continue
		}
		if strings.Contains(l.Message, "theme") {
			foundTheme = true
		}
		if strings.Contains(l.Message, "default provider") {
			foundProv = true
		}
	}
	if !foundTheme {
		t.Errorf("expected settings log entry for theme change; got %+v", logs)
	}
	if !foundProv {
		t.Errorf("expected settings log entry for provider change; got %+v", logs)
	}
}

func TestUpdateSettings_NoChangesNoLogs(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	// Save same settings → no logs.
	if err := s.UpdateSettings(StudioConfig{Settings: s.config.Settings}); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.GetRecentLogs() {
		if l.Source == "settings" {
			t.Errorf("no-op save should not log; got %+v", l)
		}
	}
}

func TestUpdateSettings_APIKeyValueNeverInLogs(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	const secret = "sk-do-not-log-me-12345"
	cfg := StudioConfig{Settings: s.config.Settings}
	cfg.Settings.GLMKey = secret
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.GetRecentLogs() {
		if strings.Contains(l.Message, secret) {
			t.Errorf("API KEY LEAK in event log: %q — message=%q", secret, l.Message)
		}
	}
}

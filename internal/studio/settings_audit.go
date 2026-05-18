package studio

import (
	"fmt"
)

// settingsAuditEntry is one row in the diff between old and new settings.
// Field is the human-readable name; Message is the formatted change line
// ready to drop into the event log.
type settingsAuditEntry struct {
	Field   string
	Message string
}

// diffSettings produces an audit-log row for each field that changed
// between old and new Settings. API key VALUES are deliberately NOT
// logged — only "set"/"cleared" status — so the event log (which iter 760+
// persists to disk and iter 750+ bundles into backups) never contains
// secrets. Stable field iteration order keeps tests deterministic.
func diffSettings(oldS, newS Settings) []settingsAuditEntry {
	out := []settingsAuditEntry{}

	if oldS.Theme != newS.Theme {
		out = append(out, settingsAuditEntry{
			Field:   "theme",
			Message: fmt.Sprintf("theme: %q → %q", oldS.Theme, newS.Theme),
		})
	}
	if oldS.DefaultProvider != newS.DefaultProvider {
		out = append(out, settingsAuditEntry{
			Field:   "defaultProvider",
			Message: fmt.Sprintf("default provider: %q → %q", oldS.DefaultProvider, newS.DefaultProvider),
		})
	}
	if oldS.DefaultModel != newS.DefaultModel {
		out = append(out, settingsAuditEntry{
			Field:   "defaultModel",
			Message: fmt.Sprintf("default model: %q → %q", oldS.DefaultModel, newS.DefaultModel),
		})
	}
	// API keys: never log the value itself. Just "set"/"cleared".
	for _, k := range []struct {
		name     string
		oldValue string
		newValue string
	}{
		{"GLM API key", oldS.GLMKey, newS.GLMKey},
		{"MiniMax API key", oldS.MiniMaxKey, newS.MiniMaxKey},
		{"Kimi API key", oldS.KimiKey, newS.KimiKey},
		{"DeepSeek API key", oldS.DeepSeekKey, newS.DeepSeekKey},
	} {
		// Compare presence, not value — replacing a key with another key is
		// reported as "updated" (we don't reveal whether they're identical).
		oldHas := k.oldValue != ""
		newHas := k.newValue != ""
		if oldHas == newHas {
			if !oldHas {
				continue // both empty → nothing changed
			}
			// Both non-empty: report only if the actual value changed.
			// Important: we still don't log the values themselves.
			if k.oldValue != k.newValue {
				out = append(out, settingsAuditEntry{
					Field:   k.name,
					Message: fmt.Sprintf("%s: updated (value not logged)", k.name),
				})
			}
			continue
		}
		if !oldHas && newHas {
			out = append(out, settingsAuditEntry{
				Field:   k.name,
				Message: fmt.Sprintf("%s: set (value not logged)", k.name),
			})
		} else {
			out = append(out, settingsAuditEntry{
				Field:   k.name,
				Message: fmt.Sprintf("%s: cleared", k.name),
			})
		}
	}
	if oldS.OllamaURL != newS.OllamaURL {
		// Ollama URL is not a secret — it's a localhost/network endpoint
		// the user controls. Log the value for debugging.
		out = append(out, settingsAuditEntry{
			Field:   "ollamaUrl",
			Message: fmt.Sprintf("Ollama URL: %q → %q", oldS.OllamaURL, newS.OllamaURL),
		})
	}
	if oldS.DefaultThinkingMode != newS.DefaultThinkingMode {
		out = append(out, settingsAuditEntry{
			Field:   "defaultThinkingMode",
			Message: fmt.Sprintf("default thinking mode: %q → %q", oldS.DefaultThinkingMode, newS.DefaultThinkingMode),
		})
	}
	if oldS.DefaultThinkingBudget != newS.DefaultThinkingBudget {
		out = append(out, settingsAuditEntry{
			Field:   "defaultThinkingBudget",
			Message: fmt.Sprintf("default thinking budget: %d → %d", oldS.DefaultThinkingBudget, newS.DefaultThinkingBudget),
		})
	}
	if oldS.DefaultBudgetUSD != newS.DefaultBudgetUSD {
		out = append(out, settingsAuditEntry{
			Field:   "defaultBudgetUSD",
			Message: fmt.Sprintf("default budget: $%.2f → $%.2f", oldS.DefaultBudgetUSD, newS.DefaultBudgetUSD),
		})
	}
	if oldS.AutoCleanupDisabled != newS.AutoCleanupDisabled {
		state := "enabled"
		if newS.AutoCleanupDisabled {
			state = "disabled"
		}
		out = append(out, settingsAuditEntry{
			Field:   "autoCleanupDisabled",
			Message: fmt.Sprintf("auto-cleanup on startup: %s", state),
		})
	}
	return out
}

// logSettingsChanges writes one event-log line per changed setting at info
// level, source="settings". Called from UpdateSettings under the studio
// write lock so the log entries reflect the new state.
func (s *Studio) logSettingsChanges(oldS, newS Settings) {
	for _, entry := range diffSettings(oldS, newS) {
		s.logf("info", "settings", "%s", entry.Message)
	}
}

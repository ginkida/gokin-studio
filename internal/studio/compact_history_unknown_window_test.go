package studio

import (
	"fmt"
	"testing"

	"google.golang.org/genai"
)

func exchangeHistory(exchanges int) []*genai.Content {
	history := make([]*genai.Content, 0, exchanges*2)
	for i := 0; i < exchanges; i++ {
		history = append(history,
			&genai.Content{Role: "user", Parts: []*genai.Part{{Text: fmt.Sprintf("question %d", i)}}},
			&genai.Content{Role: "model", Parts: []*genai.Part{{Text: fmt.Sprintf("answer %d", i)}}},
		)
	}
	return history
}

// A non-positive context window means "unknown", not "zero budget". Read as a
// budget it makes `total <= charBudget` unsatisfiable, so the trim loop runs to
// its floor and drops every middle exchange on every turn — silently deleting
// the user's conversation while the request still goes out.
//
// emergencyCompactHistory, its sibling twenty lines away, already returns the
// history untouched for maxTokens <= 0. compactHistory must agree: an
// over-long request surfaces as a provider error the emergency path can
// recover from, whereas silently discarded history is unrecoverable.
//
// Reachability today: every provider/model pair that yields a zero window is
// refused by validateStudioProviderModelRuntime before a turn starts, so this
// is a guard against a future call site or validation change, not a live
// user-facing failure.
func TestCompactHistoryTreatsNonPositiveWindowAsUnknown(t *testing.T) {
	history := exchangeHistory(10)
	for _, maxTokens := range []int{0, -1, -1024} {
		t.Run(fmt.Sprintf("maxTokens=%d", maxTokens), func(t *testing.T) {
			got := compactHistory(history, maxTokens)
			if len(got) != len(history) {
				t.Errorf("compactHistory dropped %d of %d entries for an unknown window",
					len(history)-len(got), len(history))
			}
		})
	}
}

// The two compaction entry points must not disagree about the same input.
func TestCompactionSiblingsAgreeOnUnknownWindow(t *testing.T) {
	history := exchangeHistory(10)
	normal := compactHistory(history, 0)
	emergency, dropped, _ := emergencyCompactHistory(history, 0)
	if len(normal) != len(history) || len(emergency) != len(history) || dropped != 0 {
		t.Errorf("compactHistory kept %d, emergencyCompactHistory kept %d (dropped %d), want both to keep all %d",
			len(normal), len(emergency), dropped, len(history))
	}
}

// A real window still compacts: the guard must not disable trimming outright.
func TestCompactHistoryStillTrimsWithATinyRealWindow(t *testing.T) {
	history := exchangeHistory(20)
	got := compactHistory(history, 8)
	if len(got) >= len(history) {
		t.Errorf("a tiny window must still trim: kept %d of %d", len(got), len(history))
	}
	if len(got) == 0 {
		t.Error("compaction must never empty the history")
	}
}

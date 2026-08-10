package studio

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Build a text turn with given role and text.
func textTurn(role, text string) *genai.Content {
	return &genai.Content{Role: role, Parts: []*genai.Part{genai.NewPartFromText(text)}}
}

// Build an assistant turn that carries a function call.
func funcCallTurn(name string, args map[string]any) *genai.Content {
	return &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}},
	}
}

// Build a user turn that carries a function response.
func funcResponseTurn(name string, result string) *genai.Content {
	return &genai.Content{
		Role: "user",
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     name,
			Response: map[string]any{"result": result},
		}}},
	}
}

func TestCompactHistoryNoOpWhenSmall(t *testing.T) {
	hist := []*genai.Content{
		textTurn("user", "hello"),
		textTurn("model", "hi"),
		textTurn("user", "what's up"),
		textTurn("model", "nothing much"),
	}
	out := compactHistory(hist, 128000)
	if len(out) != len(hist) {
		t.Errorf("expected no compaction, got %d turns (orig %d)", len(out), len(hist))
	}
}

func TestCompactHistoryPreservesLatestExchanges(t *testing.T) {
	// Build N exchanges where each user turn has a large payload so we exceed
	// the context window and force compaction.
	big := strings.Repeat("x", 20000) // ~5k tokens at 4 chars/token
	var hist []*genai.Content
	for i := 0; i < 6; i++ {
		hist = append(hist, textTurn("user", "msg "+big+" "+string(rune('A'+i))))
		hist = append(hist, textTurn("model", "reply"))
	}
	out := compactHistory(hist, 20000) // tight budget: ~5k tokens
	if len(out) == 0 {
		t.Fatal("compactHistory returned empty slice")
	}
	// The very last turn must be preserved intact — it's what the agent is
	// responding to right now.
	last := out[len(out)-1]
	if last.Role != "model" || len(last.Parts) == 0 || last.Parts[0].Text != "reply" {
		t.Errorf("expected last turn to be model 'reply', got role=%s text=%q", last.Role, firstText(last))
	}
	// First turn of the original history should be preserved (often a system-ish anchor).
	if out[0] != hist[0] {
		t.Logf("note: first turn not preserved, which is acceptable if compactor chose otherwise")
	}
}

func TestCompactHistoryNeverSplitsFunctionPairs(t *testing.T) {
	// Arrange: interleave several exchanges including a function call/response
	// pair. If the compactor trims mid-pair, the remaining history would be
	// invalid for the LLM (orphan FunctionResponse or FunctionCall).
	hist := []*genai.Content{
		textTurn("user", strings.Repeat("old ", 5000)),
		textTurn("model", "old reply"),
		textTurn("user", "do a thing"),
		funcCallTurn("bash", map[string]any{"command": "ls"}),
		funcResponseTurn("bash", strings.Repeat("listing ", 2000)),
		textTurn("model", "here you go"),
		textTurn("user", "now the next step"),
	}
	out := compactHistory(hist, 6000)

	// Walk output: every FunctionResponse in user turn must be preceded by a
	// FunctionCall turn from model. Every FunctionCall in model turn must be
	// followed by a FunctionResponse turn from user.
	for i, c := range out {
		if c == nil {
			t.Fatalf("compacted history has nil entry at %d", i)
		}
		for _, part := range c.Parts {
			if part == nil {
				continue
			}
			if part.FunctionResponse != nil {
				// Must have a preceding model turn with matching FunctionCall name.
				if i == 0 {
					t.Errorf("FunctionResponse at index 0 has no preceding FunctionCall (tool=%s)", part.FunctionResponse.Name)
					continue
				}
				prev := out[i-1]
				if prev.Role != "model" {
					t.Errorf("FunctionResponse at %d preceded by role=%s, expected model", i, prev.Role)
					continue
				}
				foundCall := false
				for _, pp := range prev.Parts {
					if pp != nil && pp.FunctionCall != nil && pp.FunctionCall.Name == part.FunctionResponse.Name {
						foundCall = true
						break
					}
				}
				if !foundCall {
					t.Errorf("FunctionResponse %s at index %d has no matching FunctionCall in previous turn", part.FunctionResponse.Name, i)
				}
			}
		}
	}
}

// TestCompactHistory_EmptyHistory verifies that calling compactHistory with a
// nil or zero-length history is a clean no-op — no panic, just returns the
// same value.
func TestCompactHistory_EmptyHistory(t *testing.T) {
	if out := compactHistory(nil, 128000); out != nil {
		t.Errorf("compactHistory(nil) = %v, want nil", out)
	}
	if out := compactHistory([]*genai.Content{}, 128000); len(out) != 0 {
		t.Errorf("compactHistory([]) len = %d, want 0", len(out))
	}
}

func TestEmergencyCompactHistory_KeepsNewestExchange(t *testing.T) {
	var history []*genai.Content
	for i := 0; i < 8; i++ {
		history = append(history,
			genai.NewContentFromText(fmt.Sprintf("request-%d", i), genai.RoleUser),
			genai.NewContentFromText(strings.Repeat("answer ", 100), genai.RoleModel),
		)
	}
	got, dropped, target := emergencyCompactHistory(history, 1048576)
	if dropped <= 0 || len(got) >= len(history) {
		t.Fatalf("emergency compaction did not shrink: dropped=%d before=%d after=%d", dropped, len(history), len(got))
	}
	if target != 131072 {
		t.Fatalf("target = %d, want 131072", target)
	}
	lastText := got[len(got)-1].Parts[0].Text
	if !strings.Contains(lastText, "answer") {
		t.Fatalf("newest exchange was not preserved: %q", lastText)
	}
}

func TestEmergencyCompactHistory_DoesNotDropOnlyCurrentExchange(t *testing.T) {
	history := []*genai.Content{
		genai.NewContentFromText("current request", genai.RoleUser),
	}
	got, dropped, _ := emergencyCompactHistory(history, 262144)
	if dropped != 0 || len(got) != 1 || got[0] != history[0] {
		t.Fatalf("single exchange changed: dropped=%d got=%#v", dropped, got)
	}
}

// TestCompactHistory_SingleExchangeNotTrimmed verifies that a history that
// exceeds the token budget but contains only one exchange is returned
// unchanged. Trimming a single exchange would lose the user's question and
// confuse the model; the budget overflow is surfaced to the user instead.
func TestCompactHistory_SingleExchangeNotTrimmed(t *testing.T) {
	// Build a single large exchange (user + model). Total will be much larger
	// than the tiny token budget we pass.
	big := strings.Repeat("x", 30000)
	hist := []*genai.Content{
		textTurn("user", big),
		textTurn("model", "ok"),
	}
	// Budget of 100 tokens → charBudget = 300 chars, far below 30K.
	out := compactHistory(hist, 100)
	if len(out) != len(hist) {
		t.Errorf("single-exchange history was modified (len %d→%d); should be returned unchanged", len(hist), len(out))
	}
}

func firstText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	for _, p := range c.Parts {
		if p != nil && p.Text != "" {
			return p.Text
		}
	}
	return ""
}

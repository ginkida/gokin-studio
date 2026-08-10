package studio

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestPublicRPCTextLimitsRejectBeforeSideEffects(t *testing.T) {
	s := newStudioForTest(t)
	a := addTestProject(t, s, "A")
	b := addTestProject(t, s, "B")
	tests := []struct {
		name string
		call func() error
	}{
		{"send", func() error { return s.SendMessage(a.ID, strings.Repeat("x", ChatMessageMaxBytes+1), "default") }},
		{"edit", func() error { return s.EditUserMessage(a.ID, "default", 0, strings.Repeat("x", ChatMessageMaxBytes+1)) }},
		{"dispatch", func() error { return s.Dispatch(a.ID, b.ID, "default", strings.Repeat("x", DispatchTaskMaxBytes+1)) }},
		{"commit", func() error {
			_, err := s.CommitChanges(a.ID, strings.Repeat("x", CommitMessageMaxBytes+1))
			return err
		}},
		{"search", func() error {
			_, err := s.SearchProjectHistory(a.ID, strings.Repeat("x", HistoryQueryMaxBytes+1))
			return err
		}},
		{"memory edit", func() error {
			_, err := s.UpdateMemoryEntry(a.ID, "entry", strings.Repeat("x", MemoryContentMaxBytes+1))
			return err
		}},
		{"answer", func() error { return s.AnswerQuestion("question", strings.Repeat("x", QuestionAnswerMaxBytes+1)) }},
		{"terminal", func() error { return s.WriteTerminal("terminal", strings.Repeat("x", TerminalWriteMaxBytes+1)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("oversized RPC error = %v, want limit error", err)
			}
		})
	}
}

func TestPublicRPCTextRejectsInvalidUTF8AndNUL(t *testing.T) {
	s := newStudioForTest(t)
	invalid := string([]byte{0xff})
	for name, message := range map[string]string{"invalid UTF-8": invalid, "NUL": "hello\x00world", "blank": " \n\t "} {
		t.Run(name, func(t *testing.T) {
			if err := s.SendMessage("missing", message, "default"); err == nil {
				t.Fatal("unsafe message accepted")
			}
		})
	}
	if err := s.RenameProject("missing", invalid); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid project name error = %v", err)
	}
	if err := s.AnswerQuestion(strings.Repeat("q", QuestionIDMaxBytes+1), "answer"); err == nil {
		t.Fatal("oversized question ID accepted")
	}
}

func TestValidateRPCTextAcceptsExactByteBoundary(t *testing.T) {
	value := strings.Repeat("x", HistoryQueryMaxBytes)
	if err := validateRPCText("query", value, HistoryQueryMaxBytes, true); err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
}

func TestResizeTerminalRejectsInvalidGeometry(t *testing.T) {
	s := newStudioForTest(t)
	for _, dims := range [][2]int{{0, 24}, {80, 0}, {-1, 24}, {TerminalDimensionMax + 1, 24}, {80, TerminalDimensionMax + 1}} {
		if err := s.ResizeTerminal("missing", dims[0], dims[1]); err == nil || !strings.Contains(err.Error(), "dimensions") {
			t.Fatalf("ResizeTerminal(%d,%d) error = %v", dims[0], dims[1], err)
		}
	}
}

type validationProbeTool struct {
	executed atomic.Bool
}

func (*validationProbeTool) Name() string        { return "probe" }
func (*validationProbeTool) Description() string { return "probe" }
func (*validationProbeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "probe"}
}
func (*validationProbeTool) Validate(map[string]any) error {
	return tools.NewValidationError("value", "rejected")
}
func (t *validationProbeTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.executed.Store(true)
	return tools.NewSuccessResult("unexpected"), nil
}

func TestSafeToolExecuteEnforcesValidation(t *testing.T) {
	tool := &validationProbeTool{}
	result, err := safeToolExecute(context.Background(), tool, map[string]any{"value": "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "validation error") || tool.executed.Load() {
		t.Fatalf("validation bypassed: result=%+v executed=%v", result, tool.executed.Load())
	}
}

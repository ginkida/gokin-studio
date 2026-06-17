// stubs.go — Stub types for features not extracted from gokin-last.
// These allow executor.go and factory.go to compile without the full
// implementations (validators, checkpoint, context enrichment, etc.).
package tools

import (
	"context"
	"sync"

	"google.golang.org/genai"
)

// CheckpointJournal is a stub for checkpoint journaling.
type CheckpointJournal struct {
	mu      sync.Mutex
	entries []interface{}
}

// NewCheckpointJournal creates an empty checkpoint journal.
func NewCheckpointJournal() *CheckpointJournal {
	return &CheckpointJournal{}
}

// Clear is a no-op stub.
func (c *CheckpointJournal) Clear() {}

// Lookup returns cached result if available (stub — always returns not-found).
func (c *CheckpointJournal) Lookup(call *genai.FunctionCall) (*ToolResult, string, bool) {
	return nil, "", false
}

// Record saves a tool result for checkpoint recovery (stub — no-op).
func (c *CheckpointJournal) Record(call *genai.FunctionCall, result ToolResult) {}

// --- Stub constructors for tools not extracted ---

func NewUndoPlanTool(deps ...interface{}) Tool  { return nil }
func NewRedoPlanTool(deps ...interface{}) Tool  { return nil }
func NewRefactorTool(deps ...interface{}) Tool  { return nil }
func NewCodeGraphTool(deps ...interface{}) Tool { return nil }

// NewRunTestsTool is implemented in run_tests.go
func NewSSHTool(deps ...interface{}) Tool             { return nil }
func GetAllDeclarations() []genai.FunctionDeclaration { return nil }
func FormatWarnings(w interface{}) string             { return "" }

// SemanticValidatorRegistry is a stub for semantic validators.
type SemanticValidatorRegistry struct{}

// ImpactGateResult is a stub for impact gate evaluation.
type ImpactGateResult struct {
	Summary string
	Allowed bool
	Reason  string
}

// CloneToolForWorkDir creates a clone of a tool with a different working directory.
// Stub: returns the original tool unchanged.
func CloneToolForWorkDir(t Tool, workDir string) Tool { return t }

// CloneRegistryForWorkDir creates a clone of a registry with tools re-targeted to a different working directory.
// Stub: returns a new registry with the same tools (no re-targeting).
func CloneRegistryForWorkDir(reg ToolRegistry, workDir string) ToolRegistry {
	r := NewRegistry()
	for _, t := range reg.List() {
		_ = r.Register(t)
	}
	return r
}

// --- Stub functions for impact-gate ---

func (e *Executor) evaluateImpactGate(_ context.Context, _ *genai.FunctionCall) *ImpactGateResult {
	return nil
}

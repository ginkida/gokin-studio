// stubs.go — Stub constructors for tools that are not yet ported.
package tools

import (
	"context"

	"google.golang.org/genai"
)

// --- Stub constructors for tools not extracted ---

func NewUndoPlanTool(deps ...interface{}) Tool  { return nil }
func NewRedoPlanTool(deps ...interface{}) Tool  { return nil }
func NewRefactorTool(deps ...interface{}) Tool  { return nil }
func NewCodeGraphTool(deps ...interface{}) Tool { return nil }

func NewSSHTool(deps ...interface{}) Tool             { return nil }
func GetAllDeclarations() []genai.FunctionDeclaration { return nil }

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

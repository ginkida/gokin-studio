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

func NewSSHTool(deps ...interface{}) Tool { return nil }

// GetAllDeclarations returns a map of tool name → declaration for every tool
// in the default registry. In studio, declarations live inline in each tool's
// Declaration() method (no separate declarations.go file), so this function
// builds the map dynamically. This makes drift-detection tests (TestEveryRegisteredToolHasDeclaration,
// TestNoDeadDeclarations) verify that every registered tool returns a non-nil Declaration().
func GetAllDeclarations() map[string]*genai.FunctionDeclaration {
	r := DefaultRegistry("")
	m := make(map[string]*genai.FunctionDeclaration, len(r.tools))
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, tool := range r.tools {
		if d := tool.Declaration(); d != nil {
			m[name] = d
		}
	}
	return m
}

// ImpactGateResult is a stub for impact gate evaluation.
type ImpactGateResult struct {
	Summary string
	Allowed bool
	Reason  string
}

// --- Stub functions for impact-gate ---

func (e *Executor) evaluateImpactGate(_ context.Context, _ *genai.FunctionCall) *ImpactGateResult {
	return nil
}

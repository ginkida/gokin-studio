package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func sendArgs(projectID string) map[string]any {
	return map[string]any{"action": "send", "project_id": projectID, "session_id": "s", "message": "hi"}
}

// A human-initiated turn is depth 0 and may delegate.
func TestDelegationHopAllowedAtDepthZero(t *testing.T) {
	if errType, msg := delegationHopAllowed(nil, "A", delegationHop{
		Applies: true, TargetProject: "B", CrossProject: true,
	}); errType != "" {
		t.Fatalf("first hop refused: %s / %s", errType, msg)
	}
}

func TestDelegationRefusedAtMaxDepth(t *testing.T) {
	parent := &delegationStamp{ChainID: "c", Depth: maxDelegationDepth, Chain: []string{"A", "B"}}
	errType, msg := delegationHopAllowed(parent, "B", delegationHop{
		Applies: true, TargetProject: "C", CrossProject: true,
	})
	if errType != delegationErrorDepth {
		t.Fatalf("error_type = %q, want %q", errType, delegationErrorDepth)
	}
	if !strings.Contains(msg, "hops deep") {
		t.Fatalf("message should explain the limit: %q", msg)
	}
}

// A -> B -> A is the loop the guard exists for. It is only catchable because
// the chain is seeded with the origin project.
func TestDelegationRefusesCycleBackToOrigin(t *testing.T) {
	first := nextDelegationStamp(nil, "chain-1", "A", "B")
	if len(first.Chain) != 2 || first.Chain[0] != "A" || first.Chain[1] != "B" {
		t.Fatalf("chain = %v, want [A B] so the origin is visible downstream", first.Chain)
	}
	errType, _ := delegationHopAllowed(first, "B", delegationHop{
		Applies: true, TargetProject: "A", CrossProject: true,
	})
	if errType != delegationErrorCycle {
		t.Fatalf("A->B->A error_type = %q, want %q", errType, delegationErrorCycle)
	}
}

func TestDelegationRefusesSelfTarget(t *testing.T) {
	errType, _ := delegationHopAllowed(nil, "A", delegationHop{
		Applies: true, TargetProject: "A", CrossProject: true,
	})
	if errType != delegationErrorCycle {
		t.Fatalf("self-target error_type = %q, want %q", errType, delegationErrorCycle)
	}
}

// Handing work to a sibling chat inside one project is ordinary coordination.
// It must count toward depth but must never read as a cycle.
func TestSameProjectSendIsAHopButNotACycle(t *testing.T) {
	hop := delegationHopFor("session_agent", sendArgs(""), "A")
	if !hop.Applies || hop.CrossProject {
		t.Fatalf("same-project send = %+v, want Applies without CrossProject", hop)
	}
	if errType, _ := delegationHopAllowed(nil, "A", hop); errType != "" {
		t.Fatalf("same-project send refused: %s", errType)
	}
	deep := &delegationStamp{ChainID: "c", Depth: maxDelegationDepth}
	if errType, _ := delegationHopAllowed(deep, "A", hop); errType != delegationErrorDepth {
		t.Fatalf("same-project send at max depth = %q, want depth_limit", errType)
	}
}

func TestReadOnlySessionAgentActionsAreUnconstrained(t *testing.T) {
	for _, action := range []string{"list", "read", "suggest", "search"} {
		hop := delegationHopFor("session_agent", map[string]any{"action": action}, "A")
		if hop.Applies {
			t.Fatalf("%s classified as a delegation hop", action)
		}
	}
}

func TestDelegationChainIsBounded(t *testing.T) {
	stamp := nextDelegationStamp(nil, "c", "A", "B")
	for i := 0; i < 10; i++ {
		stamp = nextDelegationStamp(stamp, "c", "B", "P")
	}
	if len(stamp.Chain) > maxDelegationDepth+1 {
		t.Fatalf("chain grew to %d entries; it is persisted and rendered and must stay bounded", len(stamp.Chain))
	}
}

func TestDelegationStampRoundTripsThroughToolContext(t *testing.T) {
	original := nextDelegationStamp(nil, "chain-9", "A", "B")
	restored := stampFromToolContext(original.toolContext())
	if restored == nil || restored.ChainID != "chain-9" || restored.Depth != 1 {
		t.Fatalf("round-trip lost the stamp: %+v", restored)
	}
	if len(restored.Chain) != 2 {
		t.Fatalf("round-trip chain = %v", restored.Chain)
	}
	// A human turn carries no stamp and must decode as depth 0, not as an error.
	if stampFromToolContext(tools.DelegationContext{}) != nil {
		t.Fatal("an absent stamp must decode to nil, i.e. depth 0")
	}
}

// The guard is evaluated before the permission switch, so the most permissive
// mode cannot wave a structurally refused hop through.
func TestHopGuardRefusesRegardlessOfPermissionMode(t *testing.T) {
	deep := &delegationStamp{ChainID: "c", Depth: maxDelegationDepth, Chain: []string{"A", "B"}}
	refusal := delegationHopGuard("session_agent", sendArgs("C"), deep, "B")
	if refusal == "" {
		t.Fatal("hop guard did not refuse a max-depth cross-project send")
	}
	for _, mode := range []string{"skip", "auto", "manual", "accept_edits"} {
		if got := permissionForTool(mode, "session_agent", sendArgs("C")); got == permissionDeny {
			continue // plan-style denial is fine too
		}
		// Whatever the mode decides, the guard's refusal is computed first and
		// independently; this asserts the guard does not consult the mode.
		if delegationHopGuard("session_agent", sendArgs("C"), deep, "B") != refusal {
			t.Fatalf("hop guard result changed with permission mode %q", mode)
		}
	}
}

func TestHopGuardIgnoresUnrelatedTools(t *testing.T) {
	for _, name := range []string{"read", "write", "bash", "git_status"} {
		if got := delegationHopGuard(name, map[string]any{"path": "x"}, nil, "A"); got != "" {
			t.Fatalf("%s was treated as a delegation hop: %q", name, got)
		}
	}
}

// Both of these are read by the delegation monitor and rendered in the caller's
// UI. Before they were written anywhere, MutatedBeforeStop was always false and
// DeniedTools always empty, so two warnings the UI promised could never fire.
func TestRecordDeniedToolIsDedupedAndBounded(t *testing.T) {
	session := NewChatSession("s")
	recordDeniedTool(session, "bash")
	recordDeniedTool(session, "bash")
	recordDeniedTool(session, "")
	recordDeniedTool(nil, "write")
	for i := 0; i < maxRecordedDeniedTools+5; i++ {
		recordDeniedTool(session, "tool-"+string(rune('a'+i)))
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if len(session.deniedTools) > maxRecordedDeniedTools {
		t.Fatalf("denied tools = %d, cap is %d", len(session.deniedTools), maxRecordedDeniedTools)
	}
	seen := map[string]int{}
	for _, name := range session.deniedTools {
		seen[name]++
		if seen[name] > 1 {
			t.Fatalf("duplicate denial recorded: %q", name)
		}
		if name == "" {
			t.Fatal("empty tool name recorded")
		}
	}
	if seen["bash"] != 1 {
		t.Fatalf("first denial lost: %+v", session.deniedTools)
	}
}

func TestToolMutatesWorkspaceCoversTheWriteSet(t *testing.T) {
	for _, name := range []string{"write", "edit", "delete", "bash", "git_commit", "document_create"} {
		if !toolMutatesWorkspace(name) {
			t.Fatalf("%q is not treated as a mutation; a cancel after it would claim nothing changed", name)
		}
	}
	for _, name := range []string{"read", "grep", "glob", "git_status", "delegate"} {
		if toolMutatesWorkspace(name) {
			t.Fatalf("%q is a read; flagging it would warn about changes that never happened", name)
		}
	}
}

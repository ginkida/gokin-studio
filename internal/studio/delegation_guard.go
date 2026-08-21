package studio

import (
	"fmt"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// A cross-agent call can chain, and a chain can loop. Project A asks B, B asks
// C, C asks A — each hop looks locally reasonable, each hop costs money, and
// each hop can run tools in a real working tree. Per-hop human approval is the
// only thing bounding this today, which means permission mode "skip" bounds it
// not at all.
//
// The guard ships before the delegation primitive on purpose: it is the brake,
// and the brake goes on before the accelerant.

const (
	// maxDelegationDepth is how many cross-agent hops a single human action may
	// cause. 2 allows the useful shape (I ask A, A consults B) and refuses the
	// shape nobody asked for (an autonomous relay across the whole workspace).
	maxDelegationDepth = 2

	delegationErrorDepth = "depth_limit"
	delegationErrorCycle = "cycle"
)

// delegationStamp is the studio-side chain record. It is attached to the turn
// that a delegation starts, so the tools running inside that turn can see how
// deep they already are.
type delegationStamp struct {
	ChainID string
	Depth   int
	Chain   []string // project IDs, oldest first, bounded by maxDelegationDepth+1
}

func (d *delegationStamp) depth() int {
	if d == nil {
		return 0
	}
	return d.Depth
}

func (d *delegationStamp) chainID() string {
	if d == nil {
		return ""
	}
	return d.ChainID
}

// toolContext projects the stamp into the engine-side type carried on the tool
// call context.
func (d *delegationStamp) toolContext() tools.DelegationContext {
	if d == nil {
		return tools.DelegationContext{}
	}
	return tools.DelegationContext{
		ChainID: d.ChainID,
		Depth:   d.Depth,
		Chain:   append([]string(nil), d.Chain...),
	}
}

// stampFromToolContext rebuilds the studio-side stamp from a tool context.
func stampFromToolContext(value tools.DelegationContext) *delegationStamp {
	if value.ChainID == "" && value.Depth == 0 && len(value.Chain) == 0 {
		return nil
	}
	return &delegationStamp{
		ChainID: value.ChainID,
		Depth:   value.Depth,
		Chain:   append([]string(nil), value.Chain...),
	}
}

// nextDelegationStamp derives the stamp for a turn about to start in
// targetProjectID. Call delegationHopAllowed first; this function assumes the
// hop has already been judged legal.
//
// The first stamp seeds the chain with the ORIGIN as well as the target.
// Without the origin, A -> B -> A looks legal from B's side: B's chain would
// read [B] and A would not appear to have been visited.
func nextDelegationStamp(parent *delegationStamp, chainID, originProjectID, targetProjectID string) *delegationStamp {
	next := &delegationStamp{ChainID: chainID, Depth: 1}
	if parent != nil {
		next.ChainID = parent.ChainID
		next.Depth = parent.Depth + 1
		next.Chain = append(next.Chain, parent.Chain...)
	} else if originProjectID != "" {
		next.Chain = append(next.Chain, originProjectID)
	}
	if next.ChainID == "" {
		next.ChainID = chainID
	}
	if targetProjectID != "" && (len(next.Chain) == 0 || next.Chain[len(next.Chain)-1] != targetProjectID) {
		next.Chain = append(next.Chain, targetProjectID)
	}
	// Defensive bound: the depth guard should already have refused anything
	// longer, but the chain is persisted and rendered, so cap it regardless.
	if limit := maxDelegationDepth + 1; len(next.Chain) > limit {
		next.Chain = next.Chain[len(next.Chain)-limit:]
	}
	return next
}

// delegationHop describes what a single tool call would reach.
type delegationHop struct {
	Applies       bool   // false = the call stays inside this turn's own work
	TargetProject string // project the turn would start in
	CrossProject  bool   // true = a different project than the caller's
}

// delegationHopAllowed reports whether one more hop may happen. It returns a
// closed error_type plus a message meant for the model, or "" when legal.
//
// Depth applies to every hop; the project-cycle rule applies only to
// cross-project hops. Handing work to a sibling chat inside one project is
// ordinary coordination, not a loop.
func delegationHopAllowed(parent *delegationStamp, originProjectID string, hop delegationHop) (string, string) {
	if !hop.Applies {
		return "", ""
	}
	if parent.depth() >= maxDelegationDepth {
		return delegationErrorDepth, fmt.Sprintf(
			"Delegation refused: this turn is already %d hops deep and the limit is %d. "+
				"Report back to whoever asked you instead of delegating further.",
			parent.depth(), maxDelegationDepth)
	}
	if !hop.CrossProject {
		return "", ""
	}
	if originProjectID != "" && originProjectID == hop.TargetProject {
		return delegationErrorCycle, "Delegation refused: a project cannot delegate to itself."
	}
	// The chain is seeded with the origin, so this catches A -> B -> A as well
	// as any longer loop.
	for _, visited := range parent.chain() {
		if visited == hop.TargetProject {
			return delegationErrorCycle, "Delegation refused: that project is already part of this delegation chain, " +
				"so sending work back to it would loop."
		}
	}
	return "", ""
}

func (d *delegationStamp) chain() []string {
	if d == nil {
		return nil
	}
	return d.Chain
}

// delegationHopGuard is the agent-loop hook. It runs before any approval so a
// structurally refused call never becomes a question the user has to answer,
// and so permission mode "skip" cannot bypass it.
//
// It returns a denial message, or "" when the call is unconstrained.
func delegationHopGuard(toolName string, args map[string]any, parent *delegationStamp, originProjectID string) string {
	hop := delegationHopFor(toolName, args, originProjectID)
	_, message := delegationHopAllowed(parent, originProjectID, hop)
	return message
}

// delegationHopFor classifies what a tool call would reach.
func delegationHopFor(toolName string, args map[string]any, originProjectID string) delegationHop {
	switch toolName {
	case "session_agent":
		// Only "send" starts a turn elsewhere; the read-only actions are free.
		if !strings.EqualFold(strings.TrimSpace(stringArg(args, "action")), "send") {
			return delegationHop{}
		}
		target := strings.TrimSpace(stringArg(args, "project_id"))
		if target == "" || target == originProjectID {
			// A sibling chat in this project. Counts toward depth, because a
			// relay of same-project sends is still a relay, but it is not a
			// project cycle.
			return delegationHop{Applies: true, TargetProject: originProjectID}
		}
		return delegationHop{Applies: true, TargetProject: target, CrossProject: true}
	case "delegate":
		switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
		case "ask", "run", "batch":
			target := strings.TrimSpace(stringArg(args, "project_id"))
			return delegationHop{
				Applies:       true,
				TargetProject: target,
				CrossProject:  target != "" && target != originProjectID,
			}
		}
		return delegationHop{}
	}
	return delegationHop{}
}

// maxRecordedDeniedTools bounds what a single turn can accumulate; the list is
// persisted in the delegation record and rendered in the caller's UI.
const maxRecordedDeniedTools = 10

// recordDeniedTool notes a blocked tool call on the session so a delegation
// monitor can report "finished, but N calls were blocked".
func recordDeniedTool(session *ChatSession, name string) {
	if session == nil || name == "" {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for _, existing := range session.deniedTools {
		if existing == name {
			return
		}
	}
	if len(session.deniedTools) >= maxRecordedDeniedTools {
		return
	}
	session.deniedTools = append(session.deniedTools, truncateUTF8(name, 100))
}

// toolMutatesWorkspace reports whether a successful call changed durable state
// in the project. Used only to warn that a cancel did not roll anything back.
func toolMutatesWorkspace(name string) bool {
	switch name {
	case "write", "edit", "atomicwrite", "delete", "move", "copy", "mkdir",
		"refactor", "document_create", "batch", "bash", "git_add", "git_commit",
		"git_branch", "git_pr", "run_tests":
		return true
	}
	return false
}

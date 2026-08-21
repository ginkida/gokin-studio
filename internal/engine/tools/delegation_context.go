package tools

// Cross-agent calls can chain: project A asks B, B asks C, C asks A. Nothing in
// the tool layer can see that chain on its own, so the studio layer stamps it
// onto the tool context and the cross-agent tools read it back out.
//
// This type lives in `tools` rather than `studio` only because `tools` cannot
// import `studio`. It carries no behaviour — the depth and cycle rules are
// enforced studio-side, where the project registry lives.

// DelegationDepthCtxKey addresses the DelegationContext value on a tool call
// context. Absent means "this turn was started by a human", i.e. depth 0.
type DelegationDepthCtxKey struct{}

// DelegationContext describes the delegation chain that produced the current
// turn. Chain holds project IDs oldest-first and is bounded by the studio-side
// depth limit, so it is always safe to render or persist.
type DelegationContext struct {
	ChainID string
	Depth   int
	Chain   []string
}

// DelegationFromContext reads the stamp off a tool context. The zero value is
// the correct answer for a human-initiated turn, so callers do not need to
// distinguish "absent" from "depth 0".
func DelegationFromContext(values interface{ Value(any) any }) DelegationContext {
	if values == nil {
		return DelegationContext{}
	}
	stamp, _ := values.Value(DelegationDepthCtxKey{}).(DelegationContext)
	return stamp
}

package tools

import "time"

// defaultToolExecTimeout is the fallback per-tool execution timeout.
// Two minutes is long enough for most file/git operations while keeping
// a stuck tool from parking the agent indefinitely.
const defaultToolExecTimeout = 2 * time.Minute

// adaptiveToolTimeout returns an execution timeout for a tool call.
//
// If historical p95 data is available, the timeout stretches to 5×p95
// (so 95% of past runs complete with headroom) but is capped at 2×base
// so one unusually slow run can't balloon the budget indefinitely.
//
// A non-positive base is replaced by defaultToolExecTimeout so this
// helper can never return a zero/negative duration.
func adaptiveToolTimeout(base, p95 time.Duration, ok bool) time.Duration {
	if base <= 0 {
		base = defaultToolExecTimeout
	}
	if !ok || p95 <= 0 {
		return base
	}
	timeout := base
	if adaptive := p95 * 5; adaptive > timeout {
		timeout = adaptive
	}
	if ceiling := base * 2; timeout > ceiling {
		timeout = ceiling
	}
	return timeout
}

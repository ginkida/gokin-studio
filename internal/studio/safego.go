package studio

import (
	"fmt"
	"os"
	"runtime/debug"
)

// safeGoFn runs fn in a new goroutine with a panic barrier. Any panic is
// captured, written to stderr (so devs running from a terminal see it), and
// — if a logger is supplied — appended to the event log so users can review
// it later in Settings → Diagnostics → View Logs.
//
// This is the single chokepoint for "background work that must not crash the
// process". Every long-running goroutine in studio should funnel through it
// (or call recoverPanic in its own defer if it can't, e.g. the PTY read loop
// which needs to control its own lifetime).
//
// `label` identifies the call site in panic output ("auto-cleanup",
// "dispatch", "messenger-ask-agent"). `logFn` is optional — pass nil if the
// caller can't access an event log (e.g. during very early Startup before
// EventLog is initialised).
func safeGoFn(label string, logFn func(level, source, message string), fn func()) {
	go func() {
		defer recoverPanic(label, logFn)
		fn()
	}()
}

// recoverPanic is the standalone version for callers that need to manage
// their own goroutine (e.g. terminal PTY read loops that hold a `*Terminal`
// closure). Use as `defer recoverPanic("ctx-label", logger)` at the top of
// the goroutine body.
//
// Logs both to stderr (visible when launched from a shell, with full stack
// trace) and via logFn (visible in the in-app Logs viewer for users running
// from a desktop launcher where stderr is invisible). Never panics itself.
func recoverPanic(label string, logFn func(level, source, message string)) {
	r := recover()
	if r == nil {
		return
	}
	// Always print to stderr first — even if logFn is nil, devs running from
	// a terminal need to see this. Include the stack so the failure is
	// debuggable beyond "something panicked".
	stack := debug.Stack()
	fmt.Fprintf(os.Stderr, "gokin-studio: %s panic recovered: %v\n%s\n", label, r, stack)
	// Best-effort log to the event log. Guarded so a logger panic can't
	// recurse and bring us back to the same spot.
	if logFn != nil {
		func() {
			defer func() { _ = recover() }()
			// Truncate stack to first 5 frames so a typical panic fits in the
			// 2 KB event-log message cap (iter 870+ enforces this).
			head := summarizeStack(stack, 5)
			logFn("error", "panic", fmt.Sprintf("[%s] %v\n%s", label, r, head))
		}()
	}
}

// summarizeStack truncates a stack trace to the first maxFrames goroutine
// frames so it fits inside the event log's 2 KB message cap.
func summarizeStack(stack []byte, maxFrames int) string {
	if len(stack) == 0 {
		return ""
	}
	s := string(stack)
	// Each frame is two lines (function + file:line); the first line is the
	// "goroutine N [running]:" header. Keep header + maxFrames * 2 lines.
	keep := 1 + maxFrames*2
	lines := 0
	for i, c := range s {
		if c == '\n' {
			lines++
			if lines >= keep {
				return s[:i]
			}
		}
	}
	return s
}

package studio

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRecoverPanic_DoesNotCrash verifies the helper swallows a synthetic
// panic — if it didn't, the test binary would crash and the suite would
// report a failure not a test-failure.
func TestRecoverPanic_DoesNotCrash(t *testing.T) {
	t.Parallel()
	func() {
		defer recoverPanic("test-no-crash", nil)
		panic("synthetic")
	}()
	// If we got here, the recover ran.
}

// TestRecoverPanic_LogsViaCallback verifies the captured panic message and
// label are forwarded to the logFn. The user's main way to learn about a
// background crash post-iter-970+ is the in-app Logs viewer; if the wire
// from recoverPanic to LogEvent breaks, that signal goes silent.
func TestRecoverPanic_LogsViaCallback(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		entries []string
	)
	logFn := func(level, source, message string) {
		mu.Lock()
		defer mu.Unlock()
		entries = append(entries, level+"|"+source+"|"+message)
	}
	func() {
		defer recoverPanic("widget-loop", logFn)
		panic(errors.New("boom"))
	}()
	mu.Lock()
	defer mu.Unlock()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d (%v)", len(entries), entries)
	}
	e := entries[0]
	if !strings.HasPrefix(e, "error|panic|") {
		t.Errorf("expected level=error source=panic, got prefix in %q", e)
	}
	if !strings.Contains(e, "[widget-loop]") {
		t.Errorf("expected label in log, got %q", e)
	}
	if !strings.Contains(e, "boom") {
		t.Errorf("expected panic value 'boom' in log, got %q", e)
	}
}

// TestRecoverPanic_NoLogIfNoPanic — verifies the helper is cheap when no
// panic occurred (the common case). Without this guarantee, every safeGoFn
// call would write a log entry even when the goroutine ran successfully.
func TestRecoverPanic_NoLogIfNoPanic(t *testing.T) {
	t.Parallel()
	var called atomic.Int32
	logFn := func(level, source, message string) {
		called.Add(1)
	}
	func() {
		defer recoverPanic("noop", logFn)
		// no panic
	}()
	if got := called.Load(); got != 0 {
		t.Errorf("expected 0 log calls on no-panic path, got %d", got)
	}
}

// TestRecoverPanic_NilLogFnSafe — log callback is optional; nil must not
// itself panic. Used for early-startup paths where the event log isn't
// wired yet.
func TestRecoverPanic_NilLogFnSafe(t *testing.T) {
	t.Parallel()
	// Should not panic even though logFn is nil.
	func() {
		defer recoverPanic("nil-logger", nil)
		panic("boom")
	}()
}

// TestRecoverPanic_BadLogFnDoesNotRecurse — if the user-supplied logger
// itself panics (e.g. closed channel during shutdown), recoverPanic must
// swallow that inner panic instead of escaping. Otherwise a shutdown race
// could still crash the app despite the recovery.
func TestRecoverPanic_BadLogFnDoesNotRecurse(t *testing.T) {
	t.Parallel()
	badLogger := func(level, source, message string) {
		panic("logger died")
	}
	// If the inner recover isn't there, this call propagates "logger died"
	// out and the test binary crashes.
	func() {
		defer recoverPanic("nested", badLogger)
		panic("outer")
	}()
}

// TestSafeGoFn_RunsAndRecovers — covers the happy path (fn runs) plus the
// panic path (synthetic panic captured), executed via the helper used by
// app.go for auto-cleanup / auto-backup.
func TestSafeGoFn_RunsAndRecovers(t *testing.T) {
	t.Parallel()
	var ran atomic.Bool
	done := make(chan struct{})
	safeGoFn("happy", nil, func() {
		ran.Store(true)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGoFn did not run within 2s")
	}
	if !ran.Load() {
		t.Error("expected fn to set ran=true")
	}

	// And the panic path — must not crash the test binary.
	panicDone := make(chan struct{})
	var captured atomic.Int32
	logFn := func(level, source, message string) {
		if strings.Contains(message, "deliberate") {
			captured.Store(1)
		}
		close(panicDone)
	}
	safeGoFn("crash", logFn, func() {
		panic("deliberate")
	})
	select {
	case <-panicDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expected log callback to fire within 2s")
	}
	if captured.Load() != 1 {
		t.Error("expected log message to contain 'deliberate'")
	}
}

// TestSummarizeStack_TruncatesLongStacks — keeps log messages under the
// EventLog 2 KB cap. Without truncation a deep stack from a runtime panic
// could blow past the cap and get clipped mid-frame, making the trace
// unreadable.
func TestSummarizeStack_TruncatesLongStacks(t *testing.T) {
	t.Parallel()
	// Build a synthetic many-line stack so we can deterministically check
	// the truncation count.
	var sb strings.Builder
	sb.WriteString("goroutine 1 [running]:\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("github.com/example/pkg.fn()\n")
		sb.WriteString("\t/path/to/file.go:123 +0x42\n")
	}
	out := summarizeStack([]byte(sb.String()), 3)
	// Header + 3 frames * 2 lines = 7 lines, so the result should have
	// fewer than 50 frames worth of content.
	lines := strings.Count(out, "\n")
	if lines >= 8 {
		t.Errorf("expected ≤7 lines after truncation, got %d", lines)
	}
	if !strings.Contains(out, "goroutine 1 [running]:") {
		t.Error("expected header to survive truncation")
	}
}

// TestSummarizeStack_ShortStackUnchanged — when the stack is already short
// (small synthetic panic in a tight goroutine), the helper must not lose
// frames trying to be helpful.
func TestSummarizeStack_ShortStackUnchanged(t *testing.T) {
	t.Parallel()
	short := []byte("goroutine 1 [running]:\nfoo.bar()\n\t/a.go:1\n")
	out := summarizeStack(short, 5)
	if out != string(short) {
		t.Errorf("expected short stack returned verbatim, got %q", out)
	}
}

// TestSummarizeStack_EmptyInputEmpty — defensive nil/empty handling.
func TestSummarizeStack_EmptyInputEmpty(t *testing.T) {
	t.Parallel()
	if got := summarizeStack(nil, 5); got != "" {
		t.Errorf("expected empty result for nil input, got %q", got)
	}
	if got := summarizeStack([]byte{}, 5); got != "" {
		t.Errorf("expected empty result for empty slice, got %q", got)
	}
}

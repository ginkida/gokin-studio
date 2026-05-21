package studio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// TestSendWithRetry_HonorsServerRetryAfter is the iter 1020+ regression
// guard. When the engine wraps a 429 in HTTPError with a Retry-After hint,
// sendWithRetry must use that delay (capped at RetryAfterMaxDelay) instead
// of its own exponential value. Without this, a provider asking for 10s
// would get our 2s backoff, likely 429 again, and burn the 3-attempt
// budget in 14 seconds.
func TestSendWithRetry_HonorsServerRetryAfter(t *testing.T) {
	var notifyDelays []int
	notify := func(attempt, max, delayMs int, reason string) {
		notifyDelays = append(notifyDelays, delayMs)
	}
	calls := 0
	start := time.Now()
	// 50ms server hint > 5ms initial delay → should use 50ms.
	hint := 50 * time.Millisecond
	_, err := sendWithRetry(context.Background(), notify, 5*time.Millisecond, func() (*client.StreamingResponse, error) {
		calls++
		return nil, &client.HTTPError{StatusCode: 429, Message: "rate limited", RetryAfter: hint}
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	// Total wait should be at least 2x the hint (waits between attempts 1→2 and 2→3).
	if elapsed < 2*hint {
		t.Errorf("expected elapsed >= 2*hint (%v), got %v", 2*hint, elapsed)
	}
	// Both notify calls should carry the hint, not the initial 5ms.
	if len(notifyDelays) != 2 {
		t.Fatalf("expected 2 notify calls, got %d", len(notifyDelays))
	}
	for i, d := range notifyDelays {
		if d != int(hint/time.Millisecond) {
			t.Errorf("notify[%d] delay = %dms, want %dms (server hint)", i, d, hint/time.Millisecond)
		}
	}
}

// TestSendWithRetry_CapsRetryAfter: a hostile provider returning a Retry-After
// of 1 hour must not park the UI for an hour. The cap RetryAfterMaxDelay
// (30s in production) is what notify should report.
//
// To avoid actually waiting 30s in a unit test, we schedule context.Cancel
// 10ms after start — that's long enough for notify to fire (notify is
// called BEFORE the select waiting on time.After), but short enough that
// the test exits in 10ms instead of 30s.
func TestSendWithRetry_CapsRetryAfter(t *testing.T) {
	var notifyDelays []int
	notify := func(attempt, max, delayMs int, reason string) {
		notifyDelays = append(notifyDelays, delayMs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	calls := 0
	_, err := sendWithRetry(ctx, notify, time.Millisecond, func() (*client.StreamingResponse, error) {
		calls++
		return nil, &client.HTTPError{StatusCode: 429, Message: "rate limit, retry later", RetryAfter: 1 * time.Hour}
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if len(notifyDelays) == 0 {
		t.Fatal("expected at least one notify call before cancel fired")
	}
	want := int(RetryAfterMaxDelay / time.Millisecond)
	if notifyDelays[0] != want {
		t.Errorf("first notify delay = %dms, want %dms (cap)", notifyDelays[0], want)
	}
}

// TestSendWithRetry_RetryAfterShorterThanBackoff: when the hint is SHORTER
// than our current exponential value, we should keep using ours. Avoids a
// provider sending Retry-After: 1 from making us hammer them after just 1s
// when we'd otherwise be on a 4s backoff.
func TestSendWithRetry_RetryAfterShorterThanBackoff(t *testing.T) {
	var notifyDelays []int
	notify := func(attempt, max, delayMs int, reason string) {
		notifyDelays = append(notifyDelays, delayMs)
	}
	initialDelay := 50 * time.Millisecond
	calls := 0
	_, _ = sendWithRetry(context.Background(), notify, initialDelay, func() (*client.StreamingResponse, error) {
		calls++
		// Hint 1ms — much shorter than our 50ms backoff. Message must
		// contain a known-retryable substring since IsRetryableError's
		// string fallback is what classifies HTTPError-wrapped errors.
		return nil, &client.HTTPError{StatusCode: 429, Message: "rate limit hit", RetryAfter: 1 * time.Millisecond}
	})
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if len(notifyDelays) != 2 {
		t.Fatalf("expected 2 notify calls, got %d", len(notifyDelays))
	}
	// First notify should report 50ms (our backoff), not 1ms.
	if notifyDelays[0] != int(initialDelay/time.Millisecond) {
		t.Errorf("first notify delay = %dms, want %dms (backoff > hint)",
			notifyDelays[0], initialDelay/time.Millisecond)
	}
}

// TestSendWithRetry_NoHintUsesBackoff: when the error doesn't carry a
// Retry-After (e.g. a 500 from a server that doesn't expose retry info),
// sendWithRetry must still use the configured exponential backoff. This is
// the "old behavior preserved" check.
func TestSendWithRetry_NoHintUsesBackoff(t *testing.T) {
	var notifyDelays []int
	notify := func(attempt, max, delayMs int, reason string) {
		notifyDelays = append(notifyDelays, delayMs)
	}
	initialDelay := 25 * time.Millisecond
	calls := 0
	_, _ = sendWithRetry(context.Background(), notify, initialDelay, func() (*client.StreamingResponse, error) {
		calls++
		// "overloaded" matches the IsRetryableError string fallback patterns
		// (the only way an untyped error is classified retryable).
		return nil, errors.New("503 server overloaded")
	})
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if len(notifyDelays) != 2 {
		t.Fatalf("expected 2 notify calls, got %d", len(notifyDelays))
	}
	if notifyDelays[0] != int(initialDelay/time.Millisecond) {
		t.Errorf("first notify delay = %dms, want %dms", notifyDelays[0], initialDelay/time.Millisecond)
	}
	// Second notify should be 2x (50ms).
	if notifyDelays[1] != 2*int(initialDelay/time.Millisecond) {
		t.Errorf("second notify delay = %dms, want %dms", notifyDelays[1], 2*int(initialDelay/time.Millisecond))
	}
}

// TestRetryAfterFromError_HTTPError: engine helper extracts the duration
// from an HTTPError. Documents the contract studio depends on.
func TestRetryAfterFromError_HTTPError(t *testing.T) {
	err := &client.HTTPError{StatusCode: 429, RetryAfter: 5 * time.Second}
	got := client.RetryAfterFromError(err)
	if got != 5*time.Second {
		t.Errorf("got %v, want 5s", got)
	}
}

// TestRetryAfterFromError_NoHint: a generic error (or an HTTPError without
// the RetryAfter field set) returns 0.
func TestRetryAfterFromError_NoHint(t *testing.T) {
	if got := client.RetryAfterFromError(errors.New("plain error")); got != 0 {
		t.Errorf("plain error: got %v, want 0", got)
	}
	if got := client.RetryAfterFromError(&client.HTTPError{StatusCode: 500}); got != 0 {
		t.Errorf("HTTPError without RetryAfter: got %v, want 0", got)
	}
	if got := client.RetryAfterFromError(nil); got != 0 {
		t.Errorf("nil error: got %v, want 0", got)
	}
}

// TestRetryAfterFromError_WrappedError: errors.As must traverse a wrapped
// chain so a caller can wrap the HTTPError and still extract the hint.
// Mirrors how studio might wrap an engine error before logging.
func TestRetryAfterFromError_WrappedError(t *testing.T) {
	inner := &client.HTTPError{StatusCode: 429, RetryAfter: 3 * time.Second}
	chained := wrapErr{inner}
	got := client.RetryAfterFromError(chained)
	if got != 3*time.Second {
		t.Errorf("wrapped HTTPError: got %v, want 3s", got)
	}
}

type wrapErr struct{ inner error }

func (w wrapErr) Error() string { return "context: " + w.inner.Error() }
func (w wrapErr) Unwrap() error { return w.inner }

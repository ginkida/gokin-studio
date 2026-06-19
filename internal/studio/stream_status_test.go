package studio

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// compile-time: streamStatusCallback must satisfy the client interface.
var _ client.StatusCallback = (*streamStatusCallback)(nil)

// TestStreamStatusCallback_Mapping verifies the liveness signals map to the
// right chat:stream_status statuses, and that the signals we deliberately don't
// surface here (retry / rate-limit / error — handled elsewhere) emit nothing.
func TestStreamStatusCallback_Mapping(t *testing.T) {
	var got []string
	cb := &streamStatusCallback{
		emit: func(status, provider string, _ int) {
			got = append(got, status+"/"+provider)
		},
	}

	cb.OnThinkingIdle(15*time.Second, "glm")
	cb.OnStreamIdle(30 * time.Second)
	cb.OnStreamResume()

	// Inherited no-ops (DefaultStatusCallback) — must NOT emit.
	cb.OnRetry(1, 3, time.Second, "blip")
	cb.OnRateLimit(time.Second)
	cb.OnError(errors.New("x"), true)

	want := []string{"thinking/glm", "stalled/", "resumed/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status sequence = %v, want %v", got, want)
	}
}

// TestStreamStatusCallback_ElapsedForwarded checks the elapsed ms is passed
// through for the idle hints (used by the UI to show how long it's been quiet).
func TestStreamStatusCallback_ElapsedForwarded(t *testing.T) {
	var lastElapsed int
	cb := &streamStatusCallback{emit: func(_, _ string, elapsedMs int) { lastElapsed = elapsedMs }}
	cb.OnStreamIdle(2 * time.Second)
	if lastElapsed != 2000 {
		t.Errorf("elapsedMs = %d, want 2000", lastElapsed)
	}
}

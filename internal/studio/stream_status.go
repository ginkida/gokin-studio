package studio

import (
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// streamStatusCallback adapts client.StatusCallback to studio chat:stream_status
// events so the UI can show that a stalled or slowly-thinking stream is still
// being worked on, instead of looking frozen during a long quiet pause. Only the
// stream-liveness signals are surfaced here — app-level retries already go out as
// chat:retry, and hard errors as chat:error. Embeds DefaultStatusCallback so the
// methods we don't override (OnRetry / OnRateLimit / OnError) are safe no-ops.
//
// The callbacks fire from the client's streaming goroutine; emit must be
// goroutine-safe (p.emitEvent is).
type streamStatusCallback struct {
	client.DefaultStatusCallback
	emit func(status, provider string, elapsedMs int)
}

// OnThinkingIdle: the model is in its silent reasoning phase (no content yet) —
// the pause is expected, so tell the UI "thinking" rather than "stalled".
func (s *streamStatusCallback) OnThinkingIdle(elapsed time.Duration, provider string) {
	s.emit("thinking", provider, int(elapsed/time.Millisecond))
}

// OnStreamIdle: the stream has gone quiet mid-response (e.g. a GLM/Kimi
// Coding-Plan endpoint pausing mid-stream). Not an error yet — the idle-timeout
// extension may still let it resume — so surface it as a soft "stalled" hint.
func (s *streamStatusCallback) OnStreamIdle(elapsed time.Duration) {
	s.emit("stalled", "", int(elapsed/time.Millisecond))
}

// OnStreamResume: data started flowing again after a quiet pause — clear the hint.
func (s *streamStatusCallback) OnStreamResume() {
	s.emit("resumed", "", 0)
}

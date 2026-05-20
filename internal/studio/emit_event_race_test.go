package studio

import (
	"context"
	"sync"
	"testing"
)

// TestEmitEvent_NoRaceWithRename is the iter 985+ regression guard for the
// p.Name read inside emitEvent's chat:error/chat:retry tee.
//
// Pre-iter-985 the tee built the log message via `fmt.Sprintf("[%s] %s",
// p.Name, msg)` without holding p.mu. RenameProject writes p.Name under
// p.mu.Lock(), so a concurrent retry burst + a flurry of renames produced
// torn reads that the race detector flagged.
//
// We don't bother emitting realistic events — emitEvent's interesting work
// happens inside the chat:error / chat:retry branch which only touches
// p.Name (no real IO once we route around the Wails runtime via testEmitter).
func TestEmitEvent_NoRaceWithRename(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "race-emit")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	// Suppress the real Wails emit so the test stays hermetic. testEmitter
	// also short-circuits the post-tee normal emit path.
	p.testEmitter = func(event string, data any) {}

	const iters = 200
	var wg sync.WaitGroup

	// Writer: rename project as fast as possible.
	wg.Go(func() {
		for i := range iters {
			name := "alpha"
			if i%2 == 1 {
				name = "beta"
			}
			_ = s.RenameProject(info.ID, name)
		}
	})

	// Reader: fire chat:error / chat:retry events whose tee path reads p.Name.
	wg.Go(func() {
		ctx := context.Background()
		for range iters {
			p.emitEvent(ctx, EventChatError, ChatTextEvent{
				ProjectID: info.ID,
				SessionID: "default",
				Text:      "synthetic error",
			})
			p.emitEvent(ctx, EventChatRetry, ChatTextEvent{
				ProjectID: info.ID,
				SessionID: "default",
				Text:      "synthetic retry",
			})
		}
	})

	wg.Wait()
}

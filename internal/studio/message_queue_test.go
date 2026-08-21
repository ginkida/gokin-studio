package studio

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func historyContainsText(history []*genai.Content, want string) bool {
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && part.Text == want {
				return true
			}
		}
	}
	return false
}

func TestMessageQueueRunsFollowUpAfterActiveTurn(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	mc := &mockClient{
		responses:   []mockResp{{text: "first answer"}, {text: "second answer"}},
		sendEntered: entered,
		sendRelease: release,
	}
	p, rec := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	p.studio = s
	s.projects[p.ID] = p

	if err := s.startMessage(p.ID, "first prompt", nil, "default"); err != nil {
		t.Fatalf("startMessage: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not start")
	}
	if err := s.QueueMessage(p.ID, "queued prompt", "default", "queue-1"); err != nil {
		t.Fatalf("QueueMessage: %v", err)
	}
	close(release)
	s.wg.Wait()

	mc.mu.Lock()
	callCount := mc.callCount
	histories := append([][]*genai.Content(nil), mc.sendHistoryCalls...)
	mc.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("provider calls = %d, want 2", callCount)
	}
	if len(histories) != 2 || !historyContainsText(histories[1], "queued prompt") {
		t.Fatalf("second provider history does not contain queued prompt: %#v", histories)
	}
	started := rec.find(EventChatQueueStarted)
	if len(started) != 1 {
		t.Fatalf("queue-start events = %d, want 1", len(started))
	}
	ev, ok := started[0].data.(ChatQueueEvent)
	if !ok || ev.ID != "queue-1" {
		t.Fatalf("queue-start event = %#v", started[0].data)
	}
	session := p.GetSession("default")
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.active || session.queueWorker || len(session.queuedTurns) != 0 {
		t.Fatalf("queue worker did not become idle: active=%v worker=%v queued=%d",
			session.active, session.queueWorker, len(session.queuedTurns))
	}
}

func TestMessageQueueRemoveAndStopClearPending(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Queue")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	rec := &recorder{}
	p.testEmitter = rec.emit
	session := p.GetSession("default")
	session.mu.Lock()
	session.queueWorker = true
	session.mu.Unlock()

	if err := s.QueueMessage(info.ID, "one", "default", "q1"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueMessage(info.ID, "two", "default", "q2"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveQueuedMessage(info.ID, "default", "q1"); err != nil {
		t.Fatal(err)
	}
	if err := s.StopGeneration(info.ID, "default"); err != nil {
		t.Fatal(err)
	}

	session.mu.RLock()
	remaining := len(session.queuedTurns)
	halted := session.queueHalt
	session.mu.RUnlock()
	if remaining != 0 || !halted {
		t.Fatalf("after StopGeneration: queued=%d halted=%v", remaining, halted)
	}
	cleared := rec.find(EventChatQueueCleared)
	if len(cleared) != 1 {
		t.Fatalf("queue-cleared events = %d, want 1", len(cleared))
	}
	ev, ok := cleared[0].data.(ChatQueueEvent)
	if !ok || len(ev.IDs) != 1 || ev.IDs[0] != "q2" {
		t.Fatalf("queue-cleared event = %#v", cleared[0].data)
	}
}

func TestStopBetweenQueueClaimAndAgentStartPreventsFirstProviderCall(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "must not run"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	session := p.GetSession("default")

	// Reproduce the synchronous claim made by startMessage before its worker
	// goroutine begins. In this window cancelFn does not exist yet.
	session.mu.Lock()
	session.queueWorker = true
	session.queueHalt = false
	session.mu.Unlock()
	session.Stop()

	p.SendMessage(context.Background(), "already stopped", Settings{
		DefaultProvider: "glm", DefaultModel: "glm-5.2",
	}, "default")

	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	session.mu.RLock()
	active := session.active
	history := append([]*genai.Content(nil), session.history...)
	session.mu.RUnlock()
	if calls != 0 || active || len(history) != 0 {
		t.Fatalf("stopped micro-phase reached provider: calls=%d active=%v history=%#v", calls, active, history)
	}
}

func TestMessageQueueRejectsIdleDuplicateAndLimit(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Queue limits")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	session := p.GetSession("default")

	if err := s.QueueMessage(info.ID, "idle", "default", "idle"); err == nil {
		t.Fatal("idle session accepted a queued message")
	}
	session.mu.Lock()
	session.queueWorker = true
	session.mu.Unlock()
	if err := s.QueueMessage(info.ID, "first", "default", "same"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueMessage(info.ID, "duplicate", "default", "same"); err == nil {
		t.Fatal("duplicate queue ID was accepted")
	}
	for i := 1; i < maxQueuedTurns; i++ {
		id := fmt.Sprintf("q-%d", i)
		if err := s.QueueMessage(info.ID, "next", "default", id); err != nil {
			t.Fatalf("queue item %d: %v", i, err)
		}
	}
	if err := s.QueueMessage(info.ID, "overflow", "default", "overflow"); err == nil {
		t.Fatal("queue accepted more than maxQueuedTurns")
	}
}

func TestMessageStartAndQueueNeverFallbackToDefaultSession(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "must not run"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	p.studio = s
	s.projects[p.ID] = p

	if err := s.startMessage(p.ID, "wrong target", nil, "missing-session"); err == nil {
		t.Fatal("startMessage accepted a missing session")
	}
	def := p.sessions["default"]
	def.mu.Lock()
	def.queueWorker = true
	def.mu.Unlock()
	if err := s.QueueMessage(p.ID, "wrong queue", "missing-session", "missing-q"); err == nil {
		t.Fatal("QueueMessage accepted a missing session")
	}
	def.mu.Lock()
	def.queueWorker = false
	queued := len(def.queuedTurns)
	def.mu.Unlock()
	mc.mu.Lock()
	providerCalls := mc.callCount
	mc.mu.Unlock()
	if queued != 0 || providerCalls != 0 {
		t.Fatalf("missing session fell back to default: queued=%d provider_calls=%d", queued, providerCalls)
	}
}

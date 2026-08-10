package studio

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQuitWorkSummaryCountsRunningQueuedAndSideWork(t *testing.T) {
	s := newStudioForTest(t)
	first := addTestProject(t, s, "First")
	second := addTestProject(t, s, "Second")
	p := s.projects[first.ID]
	defaultSession := p.GetSession("default")
	defaultSession.mu.Lock()
	defaultSession.active = true
	defaultSession.queuedTurns = []*queuedTurn{{ID: "q1"}, {ID: "q2"}}
	defaultSession.mu.Unlock()
	otherInfo, err := s.CreateChatSession(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := p.GetSession(otherInfo.ID)
	other.mu.Lock()
	other.queueWorker = true
	other.mu.Unlock()
	s.sideChatMu.Lock()
	s.sideChatRuns["side"] = sideChatRun{projectID: second.ID, sessionID: "default"}
	s.sideChatMu.Unlock()

	summary := s.quitWorkSummary()
	if summary.Projects != 2 || summary.RunningSessions != 2 || summary.QueuedTurns != 2 || summary.SideQuestions != 1 {
		t.Fatalf("quit summary = %#v", summary)
	}
	message := quitWarningMessage(summary)
	for _, phrase := range []string{"2 chats are still running", "2 follow-ups are queued", "1 side question is still running", "2 projects", "recovery data"} {
		if !strings.Contains(message, phrase) {
			t.Errorf("warning missing %q: %s", phrase, message)
		}
	}
}

func TestBeforeCloseAllowsIdleAndHonoursExplicitDecision(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Quit guard")
	var prompts atomic.Int32
	s.testQuitConfirmation = func(QuitWorkSummary) (bool, error) {
		prompts.Add(1)
		return false, nil
	}
	if prevent := s.BeforeClose(context.Background()); prevent || prompts.Load() != 0 {
		t.Fatalf("idle close prevent=%v prompts=%d", prevent, prompts.Load())
	}

	session := s.projects[info.ID].GetSession("default")
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()
	if prevent := s.BeforeClose(context.Background()); !prevent || prompts.Load() != 1 {
		t.Fatalf("keep-running decision prevent=%v prompts=%d", prevent, prompts.Load())
	}
	s.testQuitConfirmation = func(summary QuitWorkSummary) (bool, error) {
		prompts.Add(1)
		return summary.RunningSessions == 1, nil
	}
	if prevent := s.BeforeClose(context.Background()); prevent || prompts.Load() != 2 {
		t.Fatalf("quit decision prevent=%v prompts=%d", prevent, prompts.Load())
	}
}

func TestBeforeCloseFailsClosedAndSuppressesDuplicatePrompts(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Quit guard")
	session := s.projects[info.ID].GetSession("default")
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()

	s.testQuitConfirmation = func(QuitWorkSummary) (bool, error) {
		return false, errors.New("native dialog unavailable")
	}
	if !s.BeforeClose(context.Background()) {
		t.Fatal("dialog error allowed quit with active work")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	s.testQuitConfirmation = func(QuitWorkSummary) (bool, error) {
		close(entered)
		<-release
		return false, nil
	}
	result := make(chan bool, 1)
	go func() { result <- s.BeforeClose(context.Background()) }()
	<-entered
	if !s.BeforeClose(context.Background()) {
		t.Fatal("repeated quit bypassed the already-open confirmation")
	}
	close(release)
	if !<-result {
		t.Fatal("keep-running decision did not prevent quit")
	}
}

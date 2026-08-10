package studio

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAnswerQuestionResolvesPending(t *testing.T) {
	r := newAskUserRegistry()
	ch := r.register("qid-1")

	// Resolver runs concurrently; consumer blocks on the channel.
	go func() {
		time.Sleep(5 * time.Millisecond)
		if !r.resolve("qid-1", "blue") {
			t.Errorf("resolve should have returned true")
		}
	}()

	select {
	case ans := <-ch:
		if ans != "blue" {
			t.Errorf("wrong answer: %q", ans)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("channel never fired")
	}
}

func TestResolveUnknownReturnsFalse(t *testing.T) {
	r := newAskUserRegistry()
	if r.resolve("nope", "x") {
		t.Error("resolve on missing id should return false")
	}
}

func TestCleanupRemovesEntry(t *testing.T) {
	r := newAskUserRegistry()
	r.register("qid-2")
	r.cleanup("qid-2")
	if r.resolve("qid-2", "x") {
		t.Error("after cleanup, resolve should miss")
	}
}

func TestStudioAnswerQuestion(t *testing.T) {
	s := NewStudio()
	ch := s.askUsers.register("qid-3")

	if err := s.AnswerQuestion("qid-3", "yes"); err != nil {
		t.Fatalf("AnswerQuestion error: %v", err)
	}
	select {
	case got := <-ch:
		if got != "yes" {
			t.Errorf("expected 'yes', got %q", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel never fired after AnswerQuestion")
	}

	if err := s.AnswerQuestion("qid-3", "late"); err == nil {
		t.Error("second AnswerQuestion on same id should error")
	}
}

func TestStudioCancelQuestion(t *testing.T) {
	s := NewStudio()
	ch := s.askUsers.register("qid-4")
	if err := s.CancelQuestion("qid-4"); err != nil {
		t.Fatalf("CancelQuestion error: %v", err)
	}
	select {
	case _, ok := <-ch:
		// CancelQuestion closes the channel without sending a value, so the
		// receive returns (zero, false). The agent handler then sees ok=false
		// and returns an error ("user dismissed the question") to the tool.
		if ok {
			t.Error("expected channel close (ok=false) for cancel, got a value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel never signalled after CancelQuestion")
	}
}

// TestAskUserRouting_RoundTrip verifies that withAskUserRouting seeds the
// context values that askUserRouting reads back. Tests the full round-trip
// and also the "missing values" path (plain context → empty strings).
func TestAskUserRouting_RoundTrip(t *testing.T) {
	ctx := withAskUserRouting(context.Background(), "my-project", "my-session")
	pid, sid := askUserRouting(ctx)
	if pid != "my-project" {
		t.Errorf("projectID = %q, want %q", pid, "my-project")
	}
	if sid != "my-session" {
		t.Errorf("sessionID = %q, want %q", sid, "my-session")
	}

	// Plain context without routing values should return empty strings.
	pid2, sid2 := askUserRouting(context.Background())
	if pid2 != "" || sid2 != "" {
		t.Errorf("expected empty strings for unset routing, got (%q, %q)", pid2, sid2)
	}
}

// TestAnswerQuestion_NilRegistry verifies that AnswerQuestion returns an
// error when the Studio's askUsers field is nil (Studio created directly
// without NewStudio).
func TestAnswerQuestion_NilRegistry(t *testing.T) {
	s := &Studio{} // askUsers is nil
	err := s.AnswerQuestion("any-id", "any-answer")
	if err == nil {
		t.Error("expected error for nil ask-user registry, got nil")
	}
}

// TestCancelQuestion_NilRegistry verifies that CancelQuestion returns an
// error when the Studio's askUsers field is nil.
func TestCancelQuestion_NilRegistry(t *testing.T) {
	s := &Studio{} // askUsers is nil
	err := s.CancelQuestion("any-id")
	if err == nil {
		t.Error("expected error for nil ask-user registry, got nil")
	}
}

// TestCancelQuestion_UnknownID verifies that CancelQuestion returns an error
// when no question with the given ID is pending (cancel false-branch).
func TestCancelQuestion_UnknownID(t *testing.T) {
	s := NewStudio()
	err := s.CancelQuestion("question-that-was-never-registered")
	if err == nil {
		t.Error("expected error for unknown question ID, got nil")
	}
}

// TestCancelRegistry_ClosesChannel verifies that askUserRegistry.cancel closes
// the channel without sending a value, which is distinct from resolve (which
// sends AND closes).
func TestCancelRegistry_ClosesChannel(t *testing.T) {
	r := newAskUserRegistry()
	ch := r.register("qid-cancel")

	if !r.cancel("qid-cancel") {
		t.Fatal("cancel returned false for registered ID")
	}

	select {
	case val, ok := <-ch:
		if ok {
			t.Errorf("expected channel closed (ok=false), got value %q", val)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel did not close after cancel")
	}

	// A second cancel on the same id must return false (already removed).
	if r.cancel("qid-cancel") {
		t.Error("second cancel should return false")
	}
}

func TestAskUserRegistryResolveCancelRaceHasSingleWinner(t *testing.T) {
	for i := 0; i < 100; i++ {
		r := newAskUserRegistry()
		ch := r.register("qid-race")
		start := make(chan struct{})
		results := make(chan bool, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results <- r.resolve("qid-race", "allow")
		}()
		go func() {
			defer wg.Done()
			<-start
			results <- r.cancel("qid-race")
		}()
		close(start)
		wg.Wait()
		close(results)

		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("iteration %d had %d winners, want exactly one", i, winners)
		}
		if _, ok := <-ch; ok {
			if _, stillOpen := <-ch; stillOpen {
				t.Fatalf("iteration %d left resolved channel open", i)
			}
		}
	}
}

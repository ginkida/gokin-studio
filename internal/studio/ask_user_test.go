package studio

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAnswerQuestionResolvesPending(t *testing.T) {
	r := newAskUserRegistry()
	ch, ok := r.register("qid-1")
	if !ok {
		t.Fatal("register returned false")
	}

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
	ch, ok := r.register("qid-2")
	if !ok {
		t.Fatal("register returned false")
	}
	r.cleanup("qid-2", ch)
	if r.resolve("qid-2", "x") {
		t.Error("after cleanup, resolve should miss")
	}
}

func TestStudioAnswerQuestion(t *testing.T) {
	s := NewStudio()
	ch, ok := s.askUsers.register("qid-3")
	if !ok {
		t.Fatal("register returned false")
	}

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
	ch, ok := s.askUsers.register("qid-4")
	if !ok {
		t.Fatal("register returned false")
	}
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
	ch, ok := r.register("qid-cancel")
	if !ok {
		t.Fatal("register returned false")
	}

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
		ch, ok := r.register("qid-race")
		if !ok {
			t.Fatal("register returned false")
		}
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

func TestAskUserRegistryRejectsDuplicateWithoutOrphaningOriginal(t *testing.T) {
	r := newAskUserRegistry()
	original, ok := r.register("same-id")
	if !ok {
		t.Fatal("first register returned false")
	}
	if duplicate, ok := r.register("same-id"); ok || duplicate != nil {
		t.Fatalf("duplicate register = (%v, %v), want (nil, false)", duplicate, ok)
	}
	if !r.resolve("same-id", "original answer") {
		t.Fatal("original owner was not resolvable")
	}
	if got := <-original; got != "original answer" {
		t.Fatalf("original owner received %q", got)
	}
}

func TestAskUserRegistryStaleCleanupCannotDeleteReplacement(t *testing.T) {
	r := newAskUserRegistry()
	old, ok := r.register("reused-id")
	if !ok {
		t.Fatal("old register returned false")
	}
	if !r.resolve("reused-id", "old answer") {
		t.Fatal("old resolve returned false")
	}
	if got := <-old; got != "old answer" {
		t.Fatalf("old owner received %q", got)
	}

	replacement, ok := r.register("reused-id")
	if !ok {
		t.Fatal("replacement register returned false")
	}
	r.cleanup("reused-id", old)
	if !r.resolve("reused-id", "new answer") {
		t.Fatal("stale cleanup deleted replacement owner")
	}
	if got := <-replacement; got != "new answer" {
		t.Fatalf("replacement owner received %q", got)
	}
}

func TestAskUserRegistrySerializesExactRouteOnly(t *testing.T) {
	r := newAskUserRegistry()
	releaseFirst, err := r.acquireRoute(context.Background(), "project", "chat")
	if err != nil {
		t.Fatalf("acquire first route: %v", err)
	}

	acquiredSame := make(chan func(), 1)
	go func() {
		release, acquireErr := r.acquireRoute(context.Background(), "project", "chat")
		if acquireErr == nil {
			acquiredSame <- release
		}
	}()
	select {
	case release := <-acquiredSame:
		release()
		t.Fatal("same route acquired before its predecessor released")
	case <-time.After(20 * time.Millisecond):
	}

	releaseOther, err := r.acquireRoute(context.Background(), "project", "other-chat")
	if err != nil {
		t.Fatalf("independent route was blocked: %v", err)
	}
	releaseOther()

	releaseFirst()
	select {
	case release := <-acquiredSame:
		release()
	case <-time.After(200 * time.Millisecond):
		t.Fatal("queued route did not acquire after release")
	}
}

func TestAskUserRegistryRouteWaitHonorsCancellation(t *testing.T) {
	r := newAskUserRegistry()
	release, err := r.acquireRoute(context.Background(), "project", "chat")
	if err != nil {
		t.Fatalf("acquire held route: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := r.acquireRoute(ctx, "project", "chat")
		result <- acquireErr
	}()
	cancel()
	select {
	case acquireErr := <-result:
		if acquireErr != context.Canceled {
			t.Fatalf("acquire error = %v, want context.Canceled", acquireErr)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cancelled route waiter did not return")
	}
	release()

	r.mu.Lock()
	remaining := len(r.routes)
	r.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("route registry leaked %d entries", remaining)
	}
}

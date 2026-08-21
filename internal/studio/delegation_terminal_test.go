package studio

import (
	"testing"
)

// A legacy dispatch emits two completion events: delegation:complete for the
// new panel and dispatch:complete for the frontend shape that already existed.
// Both frontend handlers raise a desktop notification, so the run has to say
// which shape it is or the user gets pinged twice for one completion.
func TestLegacyDispatchCompletionIsMarkedForTheFrontend(t *testing.T) {
	s := newStudioForTest(t)

	var events []DelegationEvent
	s.testDelegationEmitter = func(name string, payload DelegationEvent) {
		if name == EventDelegationComplete {
			events = append(events, payload)
		}
	}

	s.emitDelegationTerminal(DelegationRun{
		ID: "run-legacy", Status: "completed", LegacyDispatch: true,
		FromProjectID: "a", ToProjectID: "b",
	}, "Beta")
	s.emitDelegationTerminal(DelegationRun{
		ID: "run-modern", Status: "completed",
		FromProjectID: "a", ToProjectID: "b",
	}, "Beta")

	if len(events) != 2 {
		t.Fatalf("expected two completion events, got %d", len(events))
	}
	if !events[0].LegacyDispatch {
		t.Error("a legacy dispatch is not flagged, so the delegation handler will notify on top of " +
			"the dispatch handler and the user is told twice")
	}
	if events[1].LegacyDispatch {
		t.Error("an ordinary delegation is flagged as legacy, so its notification would be suppressed " +
			"and the user would never be told")
	}
}

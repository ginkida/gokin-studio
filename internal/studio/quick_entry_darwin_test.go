//go:build darwin && cgo

package studio

import (
	"testing"
	"time"
)

func TestQuickEntryAcceptedKeysHaveDarwinMappings(t *testing.T) {
	for _, key := range quickEntryKeyNames() {
		if _, ok := darwinQuickEntryKeyCode(key); !ok {
			t.Errorf("accepted key %s has no macOS key code", key)
		}
	}
}

func TestDarwinDoubleOptionGesture(t *testing.T) {
	triggered := 0
	state := &darwinQuickEntryGestureState{kind: quickEntryDoubleOption, trigger: func() { triggered++ }}
	start := uint64(time.Second)
	tap := func(at uint64) {
		state.handle(darwinCGEventFlagsChanged, 58, darwinOptionFlag, at)
		state.handle(darwinCGEventFlagsChanged, 58, 0, at+uint64(40*time.Millisecond))
	}
	tap(start)
	if triggered != 0 {
		t.Fatal("single Option tap triggered Quick Entry")
	}
	tap(start + uint64(180*time.Millisecond))
	if triggered != 1 {
		t.Fatalf("double Option triggers = %d, want 1", triggered)
	}
}

func TestDarwinDoubleOptionRejectsModifiedLongAndInterruptedTaps(t *testing.T) {
	triggered := 0
	state := &darwinQuickEntryGestureState{kind: quickEntryDoubleOption, trigger: func() { triggered++ }}
	start := uint64(time.Second)
	// A held modifier is not a standalone Option tap.
	state.handle(darwinCGEventFlagsChanged, 58, darwinOptionFlag|(1<<17), start)
	state.handle(darwinCGEventFlagsChanged, 58, 1<<17, start+uint64(30*time.Millisecond))
	// A long hold is not a tap.
	state.handle(darwinCGEventFlagsChanged, 58, darwinOptionFlag, start+uint64(time.Second))
	state.handle(darwinCGEventFlagsChanged, 58, 0, start+uint64(time.Second+500*time.Millisecond))
	// A normal first tap followed by another key must reset the sequence.
	state.handle(darwinCGEventFlagsChanged, 58, darwinOptionFlag, start+uint64(2*time.Second))
	state.handle(darwinCGEventFlagsChanged, 58, 0, start+uint64(2*time.Second+30*time.Millisecond))
	state.handle(darwinCGEventKeyDown, 0, 0, start+uint64(2*time.Second+60*time.Millisecond))
	state.handle(darwinCGEventFlagsChanged, 58, darwinOptionFlag, start+uint64(2*time.Second+100*time.Millisecond))
	state.handle(darwinCGEventFlagsChanged, 58, 0, start+uint64(2*time.Second+130*time.Millisecond))
	if triggered != 0 {
		t.Fatalf("invalid Option sequences triggered %d times", triggered)
	}
}

func TestDarwinCapsLockGestureDebouncesDuplicateFlagEvents(t *testing.T) {
	triggered := 0
	state := &darwinQuickEntryGestureState{kind: quickEntryCapsLock, trigger: func() { triggered++ }}
	start := uint64(time.Second)
	state.handle(darwinCGEventFlagsChanged, 57, 1<<16, start)
	state.handle(darwinCGEventFlagsChanged, 57, 0, start+uint64(20*time.Millisecond))
	state.handle(darwinCGEventFlagsChanged, 57, 1<<16, start+uint64(500*time.Millisecond))
	if triggered != 2 {
		t.Fatalf("Caps Lock triggers = %d, want 2", triggered)
	}
}

package studio

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNativeMenuCommandsQueueUntilFrontendReady(t *testing.T) {
	s := NewStudio()
	activations := 0
	s.testWindowActivator = func() { activations++ }

	s.HandleNativeMenuCommand(NativeCommandNewChat)
	s.HandleNativeMenuCommand(" arbitrary-browser-event ")
	s.HandleNativeMenuCommand("  " + NativeCommandSettings + "  ")

	if activations != 2 {
		t.Fatalf("window activations = %d, want 2 valid commands", activations)
	}
	if got, want := s.StartNativeMenuEvents(), []string{NativeCommandNewChat, NativeCommandSettings}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending commands = %#v, want %#v", got, want)
	}
	if got := s.StartNativeMenuEvents(); len(got) != 0 {
		t.Fatalf("pending commands were delivered twice: %#v", got)
	}
}

func TestNativeMenuCommandsEmitLiveAfterFrontendReady(t *testing.T) {
	s := NewStudio()
	var emitted []string
	s.testNativeCommandEmitter = func(command string) { emitted = append(emitted, command) }
	s.testWindowActivator = func() {}
	s.StartNativeMenuEvents()

	want := []string{
		NativeCommandNewChat,
		NativeCommandCloseChat,
		NativeCommandAddProject,
		NativeCommandCommandPalette,
		NativeCommandChat,
		NativeCommandFiles,
		NativeCommandArtifacts,
		NativeCommandSettings,
		NativeCommandFindChat,
		NativeCommandSearchAll,
		NativeCommandBack,
		NativeCommandForward,
		NativeCommandToggleSidebar,
		NativeCommandTranscriptMode,
		NativeCommandSideChat,
		NativeCommandDiff,
		NativeCommandPreview,
		NativeCommandSelectPreview,
		NativeCommandHelp,
	}
	for _, command := range want {
		s.HandleNativeMenuCommand(command)
	}
	s.HandleNativeMenuCommand("not-allowed")

	if !reflect.DeepEqual(emitted, want) {
		t.Fatalf("emitted commands = %#v, want %#v", emitted, want)
	}
	if len(nativeCommands) != len(want) {
		t.Fatalf("allowlist has %d commands, test contract has %d", len(nativeCommands), len(want))
	}
}

func TestNativeMenuCommandReadyHandoffDoesNotLoseOrDuplicate(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := NewStudio()
		s.testWindowActivator = func() {}
		var emitted atomic.Int32
		s.testNativeCommandEmitter = func(string) { emitted.Add(1) }
		start := make(chan struct{})
		var pending []string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			pending = s.StartNativeMenuEvents()
		}()
		go func() {
			defer wg.Done()
			<-start
			s.HandleNativeMenuCommand(NativeCommandChat)
		}()
		close(start)
		wg.Wait()
		if got := len(pending) + int(emitted.Load()); got != 1 {
			t.Fatalf("iteration %d delivered command %d times (pending=%d emitted=%d)", i, got, len(pending), emitted.Load())
		}
	}
}

func TestNativeMenuCommandColdStartQueueIsBounded(t *testing.T) {
	s := NewStudio()
	s.testWindowActivator = func() {}
	for i := 0; i < nativeCommandPendingMax+9; i++ {
		s.HandleNativeMenuCommand(NativeCommandForward)
	}
	pending := s.StartNativeMenuEvents()
	if len(pending) != nativeCommandPendingMax {
		t.Fatalf("pending commands = %d, want bounded %d", len(pending), nativeCommandPendingMax)
	}
	for i, command := range pending {
		if command != NativeCommandForward {
			t.Fatalf("pending[%d] = %q", i, command)
		}
	}
}

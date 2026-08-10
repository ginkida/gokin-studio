package studio

import (
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	NativeCommandNewChat        = "new-chat"
	NativeCommandCloseChat      = "close-chat"
	NativeCommandAddProject     = "add-project"
	NativeCommandCommandPalette = "command-palette"
	NativeCommandChat           = "chat"
	NativeCommandFiles          = "files"
	NativeCommandArtifacts      = "artifacts"
	NativeCommandSettings       = "settings"
	NativeCommandFindChat       = "find-chat"
	NativeCommandSearchAll      = "search-all"
	NativeCommandBack           = "back"
	NativeCommandForward        = "forward"
	NativeCommandToggleSidebar  = "toggle-sidebar"
	NativeCommandTranscriptMode = "cycle-transcript-mode"
	NativeCommandSideChat       = "side-chat"
	NativeCommandDiff           = "diff"
	NativeCommandPreview        = "preview"
	NativeCommandSelectPreview  = "select-preview-element"
	NativeCommandHelp           = "help"

	nativeCommandPendingMax = 32
)

var nativeCommands = map[string]struct{}{
	NativeCommandNewChat:        {},
	NativeCommandCloseChat:      {},
	NativeCommandAddProject:     {},
	NativeCommandCommandPalette: {},
	NativeCommandChat:           {},
	NativeCommandFiles:          {},
	NativeCommandArtifacts:      {},
	NativeCommandSettings:       {},
	NativeCommandFindChat:       {},
	NativeCommandSearchAll:      {},
	NativeCommandBack:           {},
	NativeCommandForward:        {},
	NativeCommandToggleSidebar:  {},
	NativeCommandTranscriptMode: {},
	NativeCommandSideChat:       {},
	NativeCommandDiff:           {},
	NativeCommandPreview:        {},
	NativeCommandSelectPreview:  {},
	NativeCommandHelp:           {},
}

// HandleNativeMenuCommand is the only bridge from the operating-system menu
// into the workspace UI. Keeping a fixed allowlist prevents a menu callback or
// future binding caller from turning this into an arbitrary browser-event
// channel. Commands received before React is ready are retained in a bounded
// process-local queue.
func (s *Studio) HandleNativeMenuCommand(command string) {
	command = strings.TrimSpace(command)
	if _, ok := nativeCommands[command]; !ok {
		return
	}

	s.nativeCommandMu.Lock()
	ready := s.nativeCommandReady
	if !ready {
		if len(s.nativeCommandPending) == nativeCommandPendingMax {
			copy(s.nativeCommandPending, s.nativeCommandPending[1:])
			s.nativeCommandPending = s.nativeCommandPending[:nativeCommandPendingMax-1]
		}
		s.nativeCommandPending = append(s.nativeCommandPending, command)
	}
	s.nativeCommandMu.Unlock()

	// Menu actions are workspace actions. Restore the existing process before
	// asking React to navigate; this also moves the WebView back out of the
	// compact Quick Entry panel when necessary.
	s.activateStudioWindow()
	if ready {
		s.emitNativeCommand(command)
	}
}

// StartNativeMenuEvents is called after React installs its event listener. It
// enables live delivery and returns every cold-start command exactly once.
func (s *Studio) StartNativeMenuEvents() []string {
	s.nativeCommandMu.Lock()
	s.nativeCommandReady = true
	pending := append([]string(nil), s.nativeCommandPending...)
	s.nativeCommandPending = nil
	s.nativeCommandMu.Unlock()
	return pending
}

func (s *Studio) emitNativeCommand(command string) {
	if s.testNativeCommandEmitter != nil {
		s.testNativeCommandEmitter(command)
		return
	}
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventNativeCommand, command)
	}
}

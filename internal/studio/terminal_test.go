package studio

import (
	"errors"
	"os"
	"testing"
)

// TestOpenTerminal_UnknownProject verifies that OpenTerminal returns an error
// for a project that does not exist in the studio's project map, without
// requiring a real PTY (the error fires before any allocation).
func TestOpenTerminal_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	_, err := s.OpenTerminal("no-such-project")
	if err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestWriteTerminal_UnknownTerminal verifies that WriteTerminal returns an
// error when the terminal ID is not in the map (no PTY required).
func TestWriteTerminal_UnknownTerminal(t *testing.T) {
	s := newStudioForTest(t)
	err := s.WriteTerminal("no-such-term", "hello")
	if err == nil {
		t.Error("expected error for unknown terminal, got nil")
	}
}

// TestResizeTerminal_UnknownTerminal verifies that ResizeTerminal returns an
// error when the terminal ID is not in the map (no PTY required).
func TestResizeTerminal_UnknownTerminal(t *testing.T) {
	s := newStudioForTest(t)
	err := s.ResizeTerminal("no-such-term", 80, 24)
	if err == nil {
		t.Error("expected error for unknown terminal, got nil")
	}
}

// TestCloseTerminal_UnknownTerminal verifies that CloseTerminal returns an
// error when the terminal ID is not in the map (no PTY required).
func TestCloseTerminal_UnknownTerminal(t *testing.T) {
	s := newStudioForTest(t)
	err := s.CloseTerminal("no-such-term")
	if err == nil {
		t.Error("expected error for unknown terminal, got nil")
	}
}

// TestWriteTerminal_ClosedTerminal verifies that WriteTerminal propagates the
// ErrClosed result from Terminal.Write when the terminal is already closed.
// Covers the success path through WriteTerminal (terminal found → t.Write)
// and the `if t.closed { return os.ErrClosed }` branch inside Terminal.Write.
func TestWriteTerminal_ClosedTerminal(t *testing.T) {
	s := newStudioForTest(t)

	// Inject a pre-closed terminal directly — no PTY allocation needed.
	// closed=true causes Terminal.Write to return ErrClosed immediately,
	// before touching the nil ptmx field.
	term := &Terminal{ID: "term-closed-w", closed: true}
	s.terminals["term-closed-w"] = term

	err := s.WriteTerminal("term-closed-w", "data")
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected ErrClosed from closed terminal write, got %v", err)
	}
}

// TestResizeTerminal_ClosedTerminal verifies that ResizeTerminal propagates
// ErrClosed when the terminal is already marked closed, covering the
// `if t.closed { return os.ErrClosed }` branch inside Terminal.Resize.
func TestResizeTerminal_ClosedTerminal(t *testing.T) {
	s := newStudioForTest(t)

	term := &Terminal{ID: "term-closed-r", closed: true}
	s.terminals["term-closed-r"] = term

	err := s.ResizeTerminal("term-closed-r", 80, 24)
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("expected ErrClosed from closed terminal resize, got %v", err)
	}
}

// TestCloseTerminal_ClosedTerminal verifies the successful CloseTerminal path:
// terminal found → deleted from map → Terminal.Close called. With closed=true,
// Terminal.Close returns after the guard without touching nil PTY fields.
// Covers the `if ok { delete }` block and `t.Close(); return nil` in app.go,
// and the `if t.closed { return }` early-exit in terminal.go.
func TestCloseTerminal_ClosedTerminal(t *testing.T) {
	s := newStudioForTest(t)

	term := &Terminal{ID: "term-closed-c", closed: true}
	s.terminals["term-closed-c"] = term

	if err := s.CloseTerminal("term-closed-c"); err != nil {
		t.Errorf("CloseTerminal on already-closed terminal: %v", err)
	}
	// Terminal must be removed from the map.
	if _, ok := s.terminals["term-closed-c"]; ok {
		t.Error("terminal still in map after CloseTerminal")
	}
}

package studio

import (
	"context"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Terminal manages a single PTY session tied to a project.
type Terminal struct {
	ID        string
	ProjectID string

	cmd    *exec.Cmd
	ptmx   *os.File
	cancel context.CancelFunc
	closed bool
	mu     sync.Mutex
}

// NewTerminal spawns a shell in projectDir and streams output via Wails events.
func NewTerminal(wailsCtx context.Context, projectDir, projectID, termID string) (*Terminal, error) {
	return newTerminalWithLogger(wailsCtx, projectDir, projectID, termID, nil, nil)
}

// newTerminalWithLogger is the testable / panic-aware variant. logFn is used
// to record PTY read-loop panics into the studio event log so users can see
// them in Diagnostics. onExit, when non-nil, is invoked once the shell exits
// on its own (user typed `exit`/Ctrl+D, or the shell crashed) so the owner
// can drop its registry entry; the read loop also reaps the child + closes
// the PTY fd before calling it, so the zombie process and file descriptor
// don't leak while the terminal panel stays open. Pass nil for either when
// not wired (early bring-up, tests).
func newTerminalWithLogger(wailsCtx context.Context, projectDir, projectID, termID string, logFn func(level, source, message string), onExit func(termID string)) (*Terminal, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	cmd := exec.Command(shell)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Terminal{
		ID:        termID,
		ProjectID: projectID,
		cmd:       cmd,
		ptmx:      ptmx,
		cancel:    cancel,
	}

	// Read loop: PTY output → Wails events → xterm.js. Linux PTY read paths
	// have historically been a source of unexpected panics (closed-fd reads
	// after process exit, EventsEmit during shutdown when wailsCtx is nil).
	// recoverPanic keeps the panic from killing the whole app and surfaces
	// it in the event log so users can report it.
	go func() {
		defer recoverPanic("terminal-read-loop", logFn)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				// Check if this was a deliberate close (Close() cancelled ctx
				// and already reaped the child).
				select {
				case <-ctx.Done():
					return
				default:
				}
				// Shell exited on its own (exit / Ctrl+D / crash). Reap the
				// child process and close the PTY fd here so we don't leak a
				// zombie + descriptor while the panel stays open; Close() is
				// idempotent, so a later CloseTerminal/Shutdown is a no-op.
				t.Close()
				if onExit != nil {
					onExit(termID)
				}
				wailsRuntime.EventsEmit(wailsCtx, EventTerminalExit, map[string]any{
					"id": termID,
				})
				return
			}
			wailsRuntime.EventsEmit(wailsCtx, EventTerminalOutput, TerminalOutputEvent{
				ID:   termID,
				Data: string(buf[:n]),
			})
		}
	}()

	return t, nil
}

// Write sends user input to the PTY.
func (t *Terminal) Write(data string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	_, err := t.ptmx.Write([]byte(data))
	return err
}

// Resize changes the PTY dimensions.
func (t *Terminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	return pty.Setsize(t.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close terminates the terminal and cleans up resources.
func (t *Terminal) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.cancel()
	_ = t.ptmx.Close() // unblocks the read goroutine
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait() // reap zombie — use cmd.Wait not Process.Wait
	}
}

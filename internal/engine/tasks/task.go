package tasks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/fileutil"
	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

// SafeEnvVars is the whitelist of environment variables passed to task commands.
// This prevents leaking sensitive environment variables like API keys.
var SafeEnvVars = []string{
	"PATH",
	"HOME",
	"USER",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TMPDIR",
	"TMP",
	"TEMP",
	"EDITOR",
	"VISUAL",
	"PAGER",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"XDG_RUNTIME_DIR",
	"GOPATH",
	"GOROOT",
	"GOPROXY",
	"GOPRIVATE",
	"GOFLAGS",
	"NODE_PATH",
	"NPM_CONFIG_PREFIX",
	"PYTHONPATH",
	"VIRTUAL_ENV",
	"GIT_AUTHOR_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME",
	"GIT_COMMITTER_EMAIL",
}

// buildSafeEnv creates a sanitized environment for command execution.
func buildSafeEnv() []string {
	env := make([]string, 0, len(SafeEnvVars))
	for _, key := range SafeEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	// Always set a safe PATH if not already set
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	// Set TERM for proper terminal handling
	hasTerm := false
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	return env
}

// Status represents the status of a background task.
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusCompleted
	StatusFailed
	StatusCancelled
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// safeBuffer is a bytes.Buffer protected by its own mutex for concurrent access.
// This is needed because exec.Cmd writes to Stdout/Stderr from OS goroutines
// while GetOutput/GetInfo read concurrently.
//
// When OutputFile is set, writes go to both the in-memory buffer and a file.
// The in-memory buffer is capped at maxMemoryOutputBytes; beyond that, only
// the file contains the full output. String() returns in-memory content.
type safeBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	file       *os.File
	filePath   string
	totalBytes int64
	truncated  bool
	fileFailed bool
}

const maxMemoryOutputBytes = 10 * 1024 * 1024 // 10 MB cap for in-memory output

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Always write to file if available
	var writeErr error
	if b.file != nil {
		n, err := b.file.Write(p)
		if err != nil {
			writeErr = err
		} else if n != len(p) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			b.fileFailed = true
		}
	}

	b.totalBytes += int64(len(p))

	// Write to in-memory buffer only if under cap
	if !b.truncated && b.buf.Len()+len(p) <= maxMemoryOutputBytes {
		_, err := b.buf.Write(p)
		return len(p), errors.Join(writeErr, err)
	}

	if !b.truncated {
		b.truncated = true
	}
	return len(p), writeErr // Accept write but don't store in memory
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stringLocked()
}

func (b *safeBuffer) stringLocked() string {
	s := b.buf.String()
	if b.truncated {
		if b.filePath != "" && !b.fileFailed {
			s += fmt.Sprintf("\n\n[Output truncated in memory: %d bytes total. Full output in: %s]",
				b.totalBytes, b.filePath)
		} else {
			s += fmt.Sprintf("\n\n[Output truncated in memory: %d bytes total. Full file output is unavailable.]", b.totalBytes)
		}
	}
	return s
}

func (b *safeBuffer) snapshot() (output, filePath string, totalBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stringLocked(), b.filePath, b.totalBytes
}

// SetOutputFile configures file-backed output streaming.
func (b *safeBuffer) SetOutputFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		return fmt.Errorf("output file already configured")
	}
	f, actualPath, err := fileutil.CreatePrivateOutputFile(path)
	if err != nil {
		return err
	}
	b.file = f
	b.filePath = actualPath
	return nil
}

// Close closes the output file if open.
func (b *safeBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		err := errors.Join(b.file.Sync(), b.file.Close())
		if err != nil {
			b.fileFailed = true
		}
		b.file = nil
		return err
	}
	return nil
}

// FilePath returns the path to the output file, or empty string if none.
func (b *safeBuffer) FilePath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.filePath
}

// TotalBytes returns the total bytes written (including file-only output).
func (b *safeBuffer) TotalBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalBytes
}

// Task represents a background task.
type Task struct {
	ID        string
	Command   string
	Status    Status
	Output    safeBuffer
	Error     string
	ExitCode  int
	StartTime time.Time
	EndTime   time.Time
	WorkDir   string

	// Program and Args allow exec without shell interpretation (prevents injection).
	Program string
	Args    []string
	// Sandboxed requires a real workspace filesystem sandbox. If the backend
	// cannot be created, Start fails instead of silently executing on the host.
	Sandboxed bool
	// AllowNetwork is set only after an exact-action approval for this task.
	// The sandbox otherwise blocks external/LAN access by default.
	AllowNetwork bool

	cmd            *exec.Cmd
	cancelFunc     context.CancelFunc
	processStarted bool
	done           chan struct{} // closed when task reaches a terminal state
	doneOnce       sync.Once
	mu             sync.RWMutex
}

// NewTask creates a new background task.
func NewTask(id, command, workDir string) *Task {
	return &Task{
		ID:      id,
		Command: command,
		Status:  StatusPending,
		WorkDir: workDir,
		done:    make(chan struct{}),
	}
}

// NewTaskWithArgs creates a new background task that executes a program directly
// without shell interpretation. This prevents command injection attacks.
func NewTaskWithArgs(id, program string, args []string, workDir string) *Task {
	return &Task{
		ID:      id,
		Command: program + " " + fmt.Sprintf("%v", args),
		Program: program,
		Args:    append([]string(nil), args...),
		Status:  StatusPending,
		WorkDir: workDir,
		done:    make(chan struct{}),
	}
}

// Start starts the task execution.
func (t *Task) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	if t.Status != StatusPending {
		t.mu.Unlock()
		return fmt.Errorf("task already started")
	}
	if !fileutil.SafeFilenameComponent(t.ID) {
		t.mu.Unlock()
		return fmt.Errorf("invalid task ID")
	}

	// Create cancellable context
	execCtx, cancel := context.WithCancel(ctx)
	t.cancelFunc = cancel

	// Create command - use Program/Args if set (no shell interpretation),
	// otherwise fall back to shell execution
	if t.Sandboxed {
		config := security.DefaultSandboxConfig()
		config.AllowNetwork = t.AllowNetwork
		var isolated *security.SandboxedCommand
		var err error
		if t.Program != "" {
			isolated, err = security.NewSandboxedCommandArgs(execCtx, t.WorkDir, t.Program, t.Args, config)
		} else {
			isolated, err = security.NewSandboxedCommand(execCtx, t.WorkDir, t.Command, config)
		}
		if err != nil {
			cancel()
			t.mu.Unlock()
			return fmt.Errorf("create workspace sandbox: %w", err)
		}
		t.cmd = isolated.Command()
	} else if t.Program != "" {
		t.cmd = exec.CommandContext(execCtx, t.Program, t.Args...)
	} else {
		t.cmd = exec.CommandContext(execCtx, "sh", "-c", t.Command)
	}
	t.cmd.Dir = t.WorkDir
	t.cmd.Stdout = &t.Output
	t.cmd.Stderr = &t.Output

	// Use sanitized environment to prevent leaking sensitive env vars
	if !t.Sandboxed {
		env, err := security.WorkspaceSafeEnvironment(t.WorkDir)
		if err != nil {
			cancel()
			t.mu.Unlock()
			return fmt.Errorf("create isolated task environment: %w", err)
		}
		t.cmd.Env = env
	}

	// A background task in a WSL project belongs inside the distro, exactly like
	// a foreground bash command. This MUST come after cmd.Env above: ApplyExec/
	// ApplyShell own Env as well as Dir, and assigning Env afterwards would drop
	// the WSLENV overlay — the only channel by which injected variables reach
	// the distro. It must also precede setProcAttr, which would otherwise
	// overwrite the console-hiding attributes. Inert for non-WSL directories.
	if target := wsl.DetectFor(t.WorkDir); target.IsWSL() {
		inject := security.WorkspaceEnvironmentSnapshot()
		if t.Program != "" {
			wsl.ApplyExec(t.cmd, target, append([]string{t.Program}, t.Args...), inject)
		} else {
			wsl.ApplyShell(t.cmd, target, t.Command, inject)
		}
	}

	// Set up file-backed output only after every fallible command-preparation
	// step. Otherwise a setup error would leave an open, unreachable log file.
	outputDir := filepath.Join(t.WorkDir, ".gokin", "task-output")
	if err := t.Output.SetOutputFile(filepath.Join(outputDir, t.ID+".log")); err != nil {
		// Non-fatal: the bounded in-memory output remains available.
		_ = err
	}

	// Set up process group for proper cleanup of child processes
	setProcAttr(t.cmd)
	// exec.CommandContext kills only the direct process by default. Background
	// tasks may spawn children which inherit the output descriptors, so leaving
	// those children alive can make Cmd.Wait (and therefore Done) hang long after
	// the context was cancelled. Route context cancellation through the same
	// process-group cleanup used by Task.Cancel.
	cmd := t.cmd
	t.cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}

	t.Status = StatusRunning
	t.StartTime = time.Now()
	t.mu.Unlock()

	// Run in background
	go t.run(execCtx)

	return nil
}

// run executes the command and updates status.
func (t *Task) run(execCtx context.Context) {
	err := t.cmd.Start()
	if err != nil {
		err = errors.Join(err, t.Output.Close())
	}

	t.mu.Lock()
	if err != nil {
		defer t.mu.Unlock()
		defer t.doneOnce.Do(func() { close(t.done) }) // Guarantees done is closed on any exit path

		contextErr := execCtx.Err()
		// Release context resources regardless of how the command finished.
		if t.cancelFunc != nil {
			t.cancelFunc()
		}

		t.EndTime = time.Now()

		if t.Status == StatusCancelled || contextErr != nil {
			t.Status = StatusCancelled
			t.ExitCode = -1
			return
		}

		t.Status = StatusFailed
		t.Error = err.Error()
		t.ExitCode = -1
		return
	}

	t.processStarted = true
	cancelled := t.Status == StatusCancelled
	t.mu.Unlock()

	if cancelled {
		_ = killProcessGroup(t.cmd)
	}

	waitErr := t.cmd.Wait()
	outputErr := t.Output.Close()
	err = errors.Join(waitErr, outputErr)

	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.doneOnce.Do(func() { close(t.done) }) // Guarantees done is closed on any exit path

	contextErr := execCtx.Err()
	// Release context resources regardless of how the command finished.
	if t.cancelFunc != nil {
		t.cancelFunc()
	}

	t.EndTime = time.Now()

	// A context can be cancelled without going through Task.Cancel (for
	// example, when the owning chat or request stops). If cancellation actually
	// interrupted Wait, report the same terminal state as an explicit stop. A
	// successful Wait still wins a boundary race with a late context cancel.
	if t.Status == StatusCancelled || (waitErr != nil && contextErr != nil) {
		t.Status = StatusCancelled
		if waitErr == nil {
			t.ExitCode = 0
		} else if exitErr, ok := waitErr.(*exec.ExitError); ok {
			t.ExitCode = exitErr.ExitCode()
		} else {
			t.ExitCode = -1
		}
		return
	}

	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			t.ExitCode = exitErr.ExitCode()
		} else {
			t.ExitCode = -1
		}
	} else {
		t.Status = StatusCompleted
		t.ExitCode = 0
	}
}

// Cancel transitions a running task to cancelled. It returns false when
// another terminal transition already owns the task.
func (t *Task) Cancel() bool {
	t.mu.Lock()
	if t.Status != StatusRunning || t.cancelFunc == nil {
		t.mu.Unlock()
		return false
	}

	cancel := t.cancelFunc
	cmd := t.cmd
	processStarted := t.processStarted

	t.Status = StatusCancelled
	t.EndTime = time.Now()
	t.mu.Unlock()

	cancel()

	if processStarted {
		// Kill entire process group for proper cleanup once Start() has published cmd.Process.
		_ = killProcessGroup(cmd)
	}
	return true
}

// Done returns a channel that is closed when the task reaches a terminal state.
func (t *Task) Done() <-chan struct{} { return t.done }

// GetStatus returns the current status.
func (t *Task) GetStatus() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// GetOutput returns the current output.
func (t *Task) GetOutput() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Output.String()
}

// GetError returns the error message if failed.
func (t *Task) GetError() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Error
}

// IsRunning returns true if the task is still running.
func (t *Task) IsRunning() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status == StatusRunning
}

// IsComplete returns true if the task has finished (success, fail, or cancelled).
func (t *Task) IsComplete() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled
}

// IsCompleteAndBefore returns true if the task is complete and ended before cutoff.
// Reads both Status and EndTime atomically under one lock.
func (t *Task) IsCompleteAndBefore(cutoff time.Time) bool {
	select {
	case <-t.done:
		// A cancelled task becomes terminal before cmd.Wait returns. Requiring
		// Done prevents cleanup from unlinking a log that is still being written.
	default:
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return (t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled) &&
		t.EndTime.Before(cutoff)
}

// durationLocked returns the task duration. Caller must hold t.mu.
func (t *Task) durationLocked() time.Duration {
	if t.StartTime.IsZero() {
		return 0
	}
	if t.EndTime.IsZero() {
		return time.Since(t.StartTime)
	}
	return t.EndTime.Sub(t.StartTime)
}

// Duration returns the task duration.
func (t *Task) Duration() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.durationLocked()
}

// Info returns a summary of the task.
type Info struct {
	ID         string
	Command    string
	Status     string
	Output     string
	OutputFile string
	TotalBytes int64
	Error      string
	ExitCode   int
	Duration   time.Duration
	StartTime  time.Time
	EndTime    time.Time
}

// GetInfo returns task information.
func (t *Task) GetInfo() Info {
	t.mu.RLock()
	defer t.mu.RUnlock()
	output, outputFile, totalBytes := t.Output.snapshot()

	return Info{
		ID:         t.ID,
		Command:    t.Command,
		Status:     t.Status.String(),
		Output:     output,
		OutputFile: outputFile,
		TotalBytes: totalBytes,
		Error:      t.Error,
		ExitCode:   t.ExitCode,
		Duration:   t.durationLocked(),
		StartTime:  t.StartTime,
		EndTime:    t.EndTime,
	}
}

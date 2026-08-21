package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
	"github.com/ginkida/gokin-studio/internal/engine/security"
)

var removeTaskOutputFile = os.Remove

// ErrManagerClosed is returned when new work is submitted after the manager
// has entered its permanent teardown phase.
var ErrManagerClosed = errors.New("task manager is closed")

// CompletionHandler is called when a task completes.
type CompletionHandler func(task *Task)

// Manager manages background tasks.
type Manager struct {
	tasks   map[string]*Task
	workDir string
	counter int

	onComplete CompletionHandler
	sandbox    bool
	closed     bool
	// monitorWG covers completion callbacks as well as the channel observer.
	// monitorDone is closed once the permanent Close gate guarantees no future
	// Add can race Wait.
	monitorWG          sync.WaitGroup
	monitorsDone       chan struct{}
	monitorWaitStarted bool

	mu sync.RWMutex
}

// NewManager creates a new task manager.
func NewManager(workDir string) *Manager {
	isolation := security.DetectWorkspaceIsolation()
	return &Manager{
		tasks:        make(map[string]*Task),
		workDir:      workDir,
		sandbox:      isolation.Available,
		monitorsDone: make(chan struct{}),
	}
}

// SetWorkspaceSandboxEnabled controls whether newly started background tasks
// use the platform workspace sandbox.
func (m *Manager) SetWorkspaceSandboxEnabled(enabled bool) {
	m.mu.Lock()
	m.sandbox = enabled
	m.mu.Unlock()
}

// WorkspaceIsolationStatus reports the isolation applied to new tasks.
func (m *Manager) WorkspaceIsolationStatus() security.WorkspaceIsolationStatus {
	status := security.DetectWorkspaceIsolation()
	m.mu.RLock()
	enabled := m.sandbox
	m.mu.RUnlock()
	if !enabled || !status.Available {
		status.Enforced = false
		status.Mode = "host"
		if status.Available {
			status.Detail = "Workspace isolation was disabled; background tasks would run with host filesystem access."
		}
	}
	return status
}

// SetCompletionHandler sets the handler called when tasks complete.
func (m *Manager) SetCompletionHandler(handler CompletionHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onComplete = handler
}

// Start starts a new background task and returns its ID.
func (m *Manager) Start(ctx context.Context, command string) (string, error) {
	return m.StartWithNetwork(ctx, command, false)
}

// StartWithNetwork starts a shell task and records whether its sandbox may use
// the host network. Callers must exact-gate allowNetwork=true.
func (m *Manager) StartWithNetwork(ctx context.Context, command string, allowNetwork bool) (string, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ErrManagerClosed
	}
	m.counter++
	id := fmt.Sprintf("task_%d_%d", time.Now().Unix(), m.counter)

	task := NewTask(id, command, m.workDir)
	task.Sandboxed = m.sandbox
	task.AllowNetwork = allowNetwork
	if err := task.Start(ctx); err != nil {
		m.mu.Unlock()
		return "", err
	}
	// Publish only after Task.Start has made the task cancellable and changed
	// it to Running. Teardown can therefore never observe an uncancellable
	// Pending task between map insertion and startup.
	m.tasks[id] = task
	m.monitorWG.Add(1)
	m.mu.Unlock()

	// Monitor for completion
	go m.monitorTask(task)

	return id, nil
}

// StartWithArgs starts a new background task using direct exec (no shell interpretation).
// This prevents command injection attacks when constructing commands from user input.
func (m *Manager) StartWithArgs(ctx context.Context, program string, args []string) (string, error) {
	return m.StartWithArgsAndNetwork(ctx, program, args, false)
}

// StartWithArgsAndNetwork is the direct-exec counterpart of StartWithNetwork.
func (m *Manager) StartWithArgsAndNetwork(
	ctx context.Context,
	program string,
	args []string,
	allowNetwork bool,
) (string, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ErrManagerClosed
	}
	m.counter++
	id := fmt.Sprintf("task_%d_%d", time.Now().Unix(), m.counter)

	task := NewTaskWithArgs(id, program, args, m.workDir)
	task.Sandboxed = m.sandbox
	task.AllowNetwork = allowNetwork
	if err := task.Start(ctx); err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.tasks[id] = task
	m.monitorWG.Add(1)
	m.mu.Unlock()

	// Monitor for completion
	go m.monitorTask(task)

	return id, nil
}

// monitorTask waits for task completion and calls the handler.
func (m *Manager) monitorTask(task *Task) {
	defer m.monitorWG.Done()
	<-task.Done()

	m.mu.RLock()
	onComplete := m.onComplete
	m.mu.RUnlock()
	if onComplete != nil {
		callCompletionHandler(onComplete, task)
	}
}

func callCompletionHandler(handler CompletionHandler, task *Task) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Warn("background task completion handler panicked",
				"task_id", task.ID,
				"panic", fmt.Sprint(recovered))
		}
	}()
	handler(task)
}

// Get returns a task by ID.
func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	return task, ok
}

// GetOutput returns the output of a task.
func (m *Manager) GetOutput(id string) (string, bool) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return "", false
	}
	return task.GetOutput(), true
}

// GetInfo returns information about a task.
func (m *Manager) GetInfo(id string) (Info, bool) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return Info{}, false
	}
	return task.GetInfo(), true
}

// Wait blocks until the task has finished closing its process and output
// stream. A cancelled status is published eagerly, so status polling alone is
// not a safe completion barrier.
func (m *Manager) Wait(ctx context.Context, id string) (Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()
	if !ok {
		return Info{}, fmt.Errorf("task not found: %s", id)
	}

	// Give an already-settled task deterministic precedence over a context that
	// was cancelled at the same boundary. The second probe closes the small race
	// where both cases become ready before select chooses the context branch.
	select {
	case <-task.Done():
		return task.GetInfo(), nil
	default:
	}
	select {
	case <-task.Done():
		return task.GetInfo(), nil
	case <-ctx.Done():
		select {
		case <-task.Done():
			return task.GetInfo(), nil
		default:
			return task.GetInfo(), ctx.Err()
		}
	}
}

// Cancel cancels a running task.
func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	if !task.Cancel() {
		return fmt.Errorf("task is not running: %s", id)
	}
	return nil
}

// List returns all tasks.
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Info, 0, len(m.tasks))
	for _, task := range m.tasks {
		result = append(result, task.GetInfo())
	}
	sortTaskInfoNewestFirst(result)
	return result
}

// ListRunning returns all running tasks.
func (m *Manager) ListRunning() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Info
	for _, task := range m.tasks {
		if task.IsRunning() {
			result = append(result, task.GetInfo())
		}
	}
	sortTaskInfoNewestFirst(result)
	return result
}

// ListCompleted returns all completed tasks.
func (m *Manager) ListCompleted() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Info
	for _, task := range m.tasks {
		if task.IsComplete() {
			result = append(result, task.GetInfo())
		}
	}
	sortTaskInfoNewestFirst(result)
	return result
}

func sortTaskInfoNewestFirst(infos []Info) {
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].StartTime.Equal(infos[j].StartTime) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].StartTime.After(infos[j].StartTime)
	})
}

// Cleanup removes completed tasks older than the given duration.
func (m *Manager) Cleanup(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	cutoff := time.Now().Add(-maxAge)

	for id, task := range m.tasks {
		if task.IsCompleteAndBefore(cutoff) {
			// Clean up output file if it exists
			if fp := task.Output.FilePath(); fp != "" {
				if err := removeTaskOutputFile(fp); err != nil && !os.IsNotExist(err) {
					// Retain the task so a later cleanup can retry and the orphan is
					// still discoverable through its output path.
					continue
				}
			}
			delete(m.tasks, id)
			count++
		}
	}
	return count
}

// CancelAll cancels all running tasks.
func (m *Manager) CancelAll() {
	m.mu.RLock()
	tasks := make([]*Task, 0)
	for _, task := range m.tasks {
		if task.IsRunning() {
			tasks = append(tasks, task)
		}
	}
	m.mu.RUnlock()

	for _, task := range tasks {
		task.Cancel()
	}
}

// Close permanently rejects new tasks, cancels every task that is still
// running, and waits until process/output cleanup and completion observers have
// finished. Repeated calls are safe; a call whose context expires can be
// retried with a fresh context to wait for the same completion barriers.
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.closed = true
	all := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		all = append(all, task)
	}
	if !m.monitorWaitStarted {
		m.monitorWaitStarted = true
		go func() {
			m.monitorWG.Wait()
			close(m.monitorsDone)
		}()
	}
	monitorsDone := m.monitorsDone
	m.mu.Unlock()

	for _, task := range all {
		task.Cancel()
	}
	for _, task := range all {
		select {
		case <-task.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-monitorsDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Count returns the number of tasks.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

// RunningCount returns the number of running tasks.
func (m *Manager) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, task := range m.tasks {
		if task.IsRunning() {
			count++
		}
	}
	return count
}

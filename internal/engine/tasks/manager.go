package tasks

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

// CompletionHandler is called when a task completes.
type CompletionHandler func(task *Task)

// Manager manages background tasks.
type Manager struct {
	tasks   map[string]*Task
	workDir string
	counter int

	onComplete CompletionHandler
	sandbox    bool

	mu sync.RWMutex
}

// NewManager creates a new task manager.
func NewManager(workDir string) *Manager {
	isolation := security.DetectWorkspaceIsolation()
	return &Manager{
		tasks:   make(map[string]*Task),
		workDir: workDir,
		sandbox: isolation.Available,
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
	m.counter++
	id := fmt.Sprintf("task_%d_%d", time.Now().Unix(), m.counter)

	task := NewTask(id, command, m.workDir)
	task.Sandboxed = m.sandbox
	task.AllowNetwork = allowNetwork
	m.tasks[id] = task
	onComplete := m.onComplete
	m.mu.Unlock()

	// Start the task
	if err := task.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		return "", err
	}

	// Monitor for completion
	go m.monitorTask(task, onComplete)

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
	m.counter++
	id := fmt.Sprintf("task_%d_%d", time.Now().Unix(), m.counter)

	task := NewTaskWithArgs(id, program, args, m.workDir)
	task.Sandboxed = m.sandbox
	task.AllowNetwork = allowNetwork
	m.tasks[id] = task
	onComplete := m.onComplete
	m.mu.Unlock()

	// Start the task
	if err := task.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		return "", err
	}

	// Monitor for completion
	go m.monitorTask(task, onComplete)

	return id, nil
}

// monitorTask waits for task completion and calls the handler.
func (m *Manager) monitorTask(task *Task, onComplete CompletionHandler) {
	<-task.Done()

	if onComplete != nil {
		onComplete(task)
	}
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

// Cancel cancels a running task.
func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	task.Cancel()
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
	return result
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
				os.Remove(fp)
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

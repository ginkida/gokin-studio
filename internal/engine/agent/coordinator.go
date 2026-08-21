package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// Constants for resource management
const (
	// MaxCoordinatorTasks is the maximum number of completed tasks to keep
	MaxCoordinatorTasks = 100
)

// Coordinator manages multiple agents with dependencies and parallelism.
type Coordinator struct {
	runner       *Runner
	tasks        map[string]*CoordinatedTask
	dependencies map[string][]string // taskID -> dependent task IDs
	queue        *TaskQueue
	maxParallel  int

	// Tracking running agents
	running        map[string]string // agentID -> taskID
	completed      map[string]bool   // completed taskIDs retained for dependency checks
	completedOrder []string          // terminal taskIDs in completion order

	// Callbacks
	onTaskStart    func(task *CoordinatedTask)
	onTaskComplete func(task *CoordinatedTask, result *AgentResult)
	onAllComplete  func(results map[string]*AgentResult)

	// Event-driven channels for efficient processing
	taskReadyCh chan struct{} // Signals when a task becomes ready
	agentDoneCh chan string   // Signals when an agent completes (carries agentID)

	// Reflection for error learning feedback loop
	reflector *Reflector

	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	finished bool
	done     chan struct{}
}

// CoordinatorConfig holds configuration for the coordinator.
type CoordinatorConfig struct {
	MaxParallel int // Maximum concurrent agents (default: 3)
}

// NewCoordinator creates a new coordinator.
func NewCoordinator(ctx context.Context, runner *Runner, config *CoordinatorConfig) *Coordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if config == nil {
		config = &CoordinatorConfig{MaxParallel: 3}
	}
	if config.MaxParallel <= 0 {
		config.MaxParallel = 3
	}

	ctx, cancel := context.WithCancel(ctx)

	return &Coordinator{
		runner:       runner,
		tasks:        make(map[string]*CoordinatedTask),
		dependencies: make(map[string][]string),
		queue:        NewTaskQueue(),
		maxParallel:  config.MaxParallel,
		running:      make(map[string]string),
		completed:    make(map[string]bool),
		taskReadyCh:  make(chan struct{}, 100), // Buffered to avoid blocking
		agentDoneCh:  make(chan string, 100),   // Buffered for agent completions
		reflector:    NewReflector(),           // Initialize reflector for feedback loop
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
}

// SetReflector sets the reflector for error learning feedback loop.
func (c *Coordinator) SetReflector(r *Reflector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reflector = r
}

// cleanupCompletedTasks removes the oldest terminal task snapshots while
// retaining any completion tombstone still needed by a blocked dependent.
func (c *Coordinator) cleanupCompletedTasks() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Count completed tasks
	completedCount := len(c.completed)
	if completedCount <= MaxCoordinatorTasks {
		return
	}

	// Remove in actual completion order. Map iteration order is randomized and
	// previously made both retention and dependency behavior nondeterministic.
	removeCount := completedCount - MaxCoordinatorTasks/2
	removed := 0
	retainedOrder := make([]string, 0, len(c.completedOrder))
	for _, taskID := range c.completedOrder {
		if !c.completed[taskID] {
			continue
		}
		if removed >= removeCount || !c.canDiscardCompletedTaskLocked(taskID) {
			retainedOrder = append(retainedOrder, taskID)
			continue
		}

		task := c.tasks[taskID]
		if task != nil {
			for _, dependencyID := range task.Dependencies {
				c.dependencies[dependencyID] = removeTaskID(c.dependencies[dependencyID], taskID)
				if len(c.dependencies[dependencyID]) == 0 {
					delete(c.dependencies, dependencyID)
				}
			}
		}
		delete(c.tasks, taskID)
		delete(c.completed, taskID)
		delete(c.dependencies, taskID)
		removed++
	}
	c.completedOrder = retainedOrder

	if removed > 0 {
		logging.Debug("coordinator cleaned up old tasks", "removed", removed)
	}
}

func (c *Coordinator) canDiscardCompletedTaskLocked(taskID string) bool {
	for _, dependentID := range c.dependencies[taskID] {
		dependent := c.tasks[dependentID]
		if dependent != nil && (dependent.Status == TaskStatusPending || dependent.Status == TaskStatusBlocked) {
			return false
		}
	}
	return true
}

func removeTaskID(taskIDs []string, target string) []string {
	kept := taskIDs[:0]
	for _, taskID := range taskIDs {
		if taskID != target {
			kept = append(kept, taskID)
		}
	}
	return kept
}

// generateTaskID creates a unique task ID.
func generateTaskID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "task_" + hex.EncodeToString(b)
}

// AddTask adds a new task to the coordinator.
func (c *Coordinator) AddTask(prompt string, agentType AgentType, priority TaskPriority, deps []string) string {
	parsedType := ParseAgentType(string(agentType))
	if strings.TrimSpace(prompt) == "" || parsedType == "" || priority < 1 || priority > 10 {
		return ""
	}
	agentType = parsedType
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return ""
	}
	seenDependencies := make(map[string]bool, len(deps))
	for _, dependencyID := range deps {
		if dependencyID == "" || seenDependencies[dependencyID] || c.tasks[dependencyID] == nil {
			return ""
		}
		seenDependencies[dependencyID] = true
	}

	taskID := generateTaskID()
	task := &CoordinatedTask{
		ID:           taskID,
		Prompt:       prompt,
		AgentType:    agentType,
		Priority:     priority,
		Dependencies: append([]string(nil), deps...),
		Status:       TaskStatusPending,
	}

	c.tasks[taskID] = task

	// Build reverse dependency map
	for _, depID := range deps {
		c.dependencies[depID] = append(c.dependencies[depID], taskID)
	}

	// Check if task is ready
	if c.areDependenciesMet(task) {
		task.Status = TaskStatusReady
		c.queue.PushTask(task)
		// Signal that a task is ready (non-blocking)
		select {
		case c.taskReadyCh <- struct{}{}:
		default:
		}
	} else {
		task.Status = TaskStatusBlocked
	}

	logging.Debug("coordinator: task added",
		"task_id", taskID,
		"agent_type", agentType,
		"priority", priority,
		"dependencies", task.Dependencies,
		"status", task.Status)

	return taskID
}

// areDependenciesMet checks if all dependencies are completed.
func (c *Coordinator) areDependenciesMet(task *CoordinatedTask) bool {
	for _, depID := range task.Dependencies {
		if !c.completed[depID] {
			return false
		}
	}
	return true
}

func (c *Coordinator) markCompletedLocked(taskID string) {
	if c.completed[taskID] {
		return
	}
	c.completed[taskID] = true
	c.completedOrder = append(c.completedOrder, taskID)
}

// Start begins processing tasks.
func (c *Coordinator) Start() {
	c.mu.Lock()
	if c.started || c.finished {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()

	if err := c.ctx.Err(); err != nil {
		c.finalizeStoppedTasks(err)
		return
	}
	if c.publishCompletion(false) {
		return
	}
	go c.processLoop()
	c.signalTaskReady()
}

// Stop stops the coordinator.
func (c *Coordinator) Stop() {
	c.cancel()
	c.finalizeStoppedTasks(context.Canceled)
}

// processLoop is the main coordination loop.
// Uses event-driven approach with fallback ticker to reduce CPU usage.
func (c *Coordinator) processLoop() {
	// Fallback ticker for periodic checks (5s safety net — primary notification is event-driven)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			c.finalizeStoppedTasks(c.ctx.Err())
			return

		case <-c.taskReadyCh:
			// A task became ready - process it
			c.processReadyTasks()

			// Check if all done
			if c.publishCompletion(false) {
				return
			}

		case agentID := <-c.agentDoneCh:
			// An agent completed - handle it
			c.handleAgentCompletion(agentID)

			// Check if all done
			if c.publishCompletion(false) {
				return
			}

		case <-ticker.C:
			// Fallback: periodic check for any missed events
			c.processReadyTasks()
			c.checkCompletedAgents()

			// Check if all done
			if c.publishCompletion(false) {
				return
			}
		}
	}
}

// processReadyTasks starts ready tasks up to maxParallel.
func (c *Coordinator) processReadyTasks() {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}

	runningCount := len(c.running)
	availableSlots := c.maxParallel - runningCount
	toStart := make([]string, 0, availableSlots)

	for availableSlots > 0 {
		task := c.queue.PopTask()
		if task == nil {
			break
		}

		if task.Status != TaskStatusReady {
			continue
		}

		// Start the task
		task.Status = TaskStatusRunning
		toStart = append(toStart, task.ID)
		availableSlots--
	}
	c.mu.Unlock()

	for _, taskID := range toStart {
		c.startTask(taskID)
	}
}

// startTask spawns an agent for a task.
func (c *Coordinator) startTask(taskID string) {
	if err := c.ctx.Err(); err != nil {
		c.finalizeStoppedTasks(err)
		return
	}
	c.mu.RLock()
	task := c.tasks[taskID]
	if task == nil || task.Status != TaskStatusRunning || c.finished {
		c.mu.RUnlock()
		return
	}
	taskSnapshot := cloneCoordinatedTask(task)
	onStart := c.onTaskStart
	c.mu.RUnlock()

	logging.Info("coordinator: starting task",
		"task_id", taskSnapshot.ID,
		"agent_type", taskSnapshot.AgentType,
		"prompt", truncate(taskSnapshot.Prompt, 100))

	if onStart != nil {
		callCoordinatorObserver("task start", func() { onStart(taskSnapshot) })
	}
	if c.ctx.Err() != nil {
		c.finalizeStoppedTasks(c.ctx.Err())
		return
	}
	c.mu.RLock()
	task = c.tasks[taskID]
	canSpawn := !c.finished && task != nil && task.Status == TaskStatusRunning
	c.mu.RUnlock()
	if !canSpawn {
		return
	}

	if c.runner == nil {
		c.failTaskStart(taskID, taskSnapshot.AgentType, fmt.Errorf("agent runner is not configured"))
		return
	}

	// Spawn async agent
	agentID := c.runner.SpawnAsync(c.ctx, string(taskSnapshot.AgentType), taskSnapshot.Prompt, 30, "")
	if agentID == "" {
		c.failTaskStart(taskID, taskSnapshot.AgentType, fmt.Errorf("agent runner rejected the task"))
		return
	}
	c.mu.Lock()
	task = c.tasks[taskID]
	if c.finished || task == nil || task.Status != TaskStatusRunning {
		c.mu.Unlock()
		_ = c.runner.Cancel(agentID)
		return
	}
	c.running[agentID] = taskID
	c.mu.Unlock()

	// Monitor completion and notify coordinator immediately via agentDoneCh
	go func() {
		if _, err := c.runner.WaitWithContext(c.ctx, agentID); err == nil || c.ctx.Err() == nil {
			select {
			case c.agentDoneCh <- agentID:
			case <-c.ctx.Done():
			}
		}
	}()
}

func (c *Coordinator) failTaskStart(taskID string, agentType AgentType, startErr error) {
	result := &AgentResult{
		Type:      agentType,
		Status:    AgentStatusFailed,
		Error:     startErr.Error(),
		Completed: true,
	}

	c.mu.Lock()
	task := c.tasks[taskID]
	if c.finished || task == nil || task.Status != TaskStatusRunning {
		c.mu.Unlock()
		return
	}
	task.Status = TaskStatusFailed
	task.Result = cloneAgentResult(result)
	c.markCompletedLocked(taskID)
	c.unblockDependents(taskID)
	onComplete := c.onTaskComplete
	taskSnapshot := cloneCoordinatedTask(task)
	resultSnapshot := cloneAgentResult(result)
	needsCleanup := len(c.completed) > MaxCoordinatorTasks
	c.mu.Unlock()

	logging.Warn("coordinator: failed to start task", "task_id", taskID, "error", startErr)
	if onComplete != nil {
		callCoordinatorObserver("task complete", func() { onComplete(taskSnapshot, resultSnapshot) })
	}
	if needsCleanup {
		c.cleanupCompletedTasks()
	}
	c.publishCompletion(false)
}

func (c *Coordinator) signalTaskReady() {
	select {
	case c.taskReadyCh <- struct{}{}:
	default:
	}
}

// checkCompletedAgents checks for completed agents and updates tasks.
func (c *Coordinator) checkCompletedAgents() {
	c.mu.RLock()
	agentIDs := make([]string, 0, len(c.running))
	for agentID := range c.running {
		agentIDs = append(agentIDs, agentID)
	}
	c.mu.RUnlock()

	for _, agentID := range agentIDs {
		c.handleAgentCompletion(agentID)
	}
}

// recordReflectionFeedback records success/failure for learned error solutions.
func (c *Coordinator) recordReflectionFeedback(result *AgentResult, success bool) {
	c.mu.RLock()
	reflector := c.reflector
	c.mu.RUnlock()
	if reflector == nil {
		return
	}

	// Check if this result used a learned solution (has LearnedEntryID in metadata)
	// The agent would have stored this in the result metadata during error recovery
	if result.Metadata != nil {
		if entryID, ok := result.Metadata["learned_entry_id"].(string); ok && entryID != "" {
			var err error
			if success {
				err = reflector.RecordSolutionSuccess(entryID)
			} else {
				err = reflector.RecordSolutionFailure(entryID)
			}
			if err != nil {
				logging.Warn("coordinator: failed to record reflection feedback",
					"entry_id", entryID,
					"success", success,
					"error", err)
			} else {
				logging.Debug("coordinator: recorded reflection feedback",
					"entry_id", entryID,
					"success", success)
			}
		}
	}
}

// unblockDependents moves blocked tasks to ready if dependencies are met.
func (c *Coordinator) unblockDependents(completedID string) {
	dependents := c.dependencies[completedID]
	for _, depTaskID := range dependents {
		task := c.tasks[depTaskID]
		if task == nil || task.Status != TaskStatusBlocked {
			continue
		}

		if c.areDependenciesMet(task) {
			task.Status = TaskStatusReady
			c.queue.PushTask(task)

			// Signal that a task is ready (non-blocking)
			select {
			case c.taskReadyCh <- struct{}{}:
			default:
			}

			logging.Debug("coordinator: task unblocked",
				"task_id", depTaskID,
				"unblocked_by", completedID)
		}
	}
}

// handleAgentCompletion handles a single agent completion event.
func (c *Coordinator) handleAgentCompletion(agentID string) {
	c.mu.RLock()
	taskID, ok := c.running[agentID]
	c.mu.RUnlock()
	if !ok {
		return
	}

	result, ok := c.runner.GetResult(agentID)
	if !ok || result == nil || !result.Completed {
		return
	}

	c.mu.Lock()
	if currentTaskID, running := c.running[agentID]; !running || currentTaskID != taskID {
		c.mu.Unlock()
		return
	}
	task := c.tasks[taskID]
	if task == nil {
		delete(c.running, agentID)
		c.mu.Unlock()
		return
	}

	// Update task status
	if result.Status == AgentStatusCompleted {
		task.Status = TaskStatusCompleted
	} else {
		task.Status = TaskStatusFailed
	}
	task.Result = cloneAgentResult(result)

	// Mark completed
	c.markCompletedLocked(taskID)
	delete(c.running, agentID)

	logging.Info("coordinator: task completed",
		"task_id", taskID,
		"status", task.Status,
		"duration", result.Duration)

	// Snapshot callback and unblock dependents under lock
	onComplete := c.onTaskComplete
	taskSnapshot := cloneCoordinatedTask(task)
	resultSnapshot := cloneAgentResult(result)
	c.unblockDependents(taskID)
	needsCleanup := len(c.completed) > MaxCoordinatorTasks
	c.mu.Unlock()

	c.recordReflectionFeedback(resultSnapshot, result.Status == AgentStatusCompleted)
	if onComplete != nil {
		callCoordinatorObserver("task complete", func() { onComplete(taskSnapshot, resultSnapshot) })
	}
	if needsCleanup {
		c.cleanupCompletedTasks()
	}
	c.publishCompletion(false)
}

func (c *Coordinator) isAllCompleteLocked() bool {
	if len(c.running) > 0 {
		return false
	}

	for _, task := range c.tasks {
		if task.Status != TaskStatusCompleted && task.Status != TaskStatusFailed {
			return false
		}
	}

	return true
}

func (c *Coordinator) resultsSnapshotLocked() map[string]*AgentResult {
	results := make(map[string]*AgentResult, len(c.tasks))
	for taskID, task := range c.tasks {
		results[taskID] = cloneAgentResult(task.Result)
	}
	return results
}

// publishCompletion closes the durable completion signal exactly once. When
// force is true, unfinished tasks are assumed to have already been finalized.
func (c *Coordinator) publishCompletion(force bool) bool {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return true
	}
	if !force && (!c.started || !c.isAllCompleteLocked()) {
		c.mu.Unlock()
		return false
	}
	c.finished = true
	results := c.resultsSnapshotLocked()
	onAllComplete := c.onAllComplete
	close(c.done)
	c.mu.Unlock()

	if onAllComplete != nil {
		callCoordinatorObserver("all tasks complete", func() { onAllComplete(results) })
	}
	return true
}

func (c *Coordinator) finalizeStoppedTasks(reason error) {
	if reason == nil {
		reason = context.Canceled
	}

	type completionCallback struct {
		task   *CoordinatedTask
		result *AgentResult
	}

	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	runningAgentIDs := make([]string, 0, len(c.running))
	for agentID := range c.running {
		runningAgentIDs = append(runningAgentIDs, agentID)
	}
	callbacks := make([]completionCallback, 0)
	onComplete := c.onTaskComplete
	for taskID, task := range c.tasks {
		if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed {
			continue
		}
		agentID := ""
		for runningID, runningTaskID := range c.running {
			if runningTaskID == taskID {
				agentID = runningID
				break
			}
		}
		c.queue.RemoveTask(taskID)
		task.Status = TaskStatusFailed
		task.Result = &AgentResult{
			AgentID:   agentID,
			Type:      task.AgentType,
			Status:    AgentStatusCancelled,
			Error:     reason.Error(),
			Completed: true,
		}
		c.markCompletedLocked(taskID)
		if onComplete != nil {
			callbacks = append(callbacks, completionCallback{
				task:   cloneCoordinatedTask(task),
				result: cloneAgentResult(task.Result),
			})
		}
	}
	clear(c.running)
	c.mu.Unlock()

	for _, agentID := range runningAgentIDs {
		if err := c.runner.Cancel(agentID); err != nil {
			logging.Debug("coordinator: agent cancellation raced with completion", "agent_id", agentID, "error", err)
		}
	}
	for _, callback := range callbacks {
		cb := callback
		callCoordinatorObserver("task complete", func() { onComplete(cb.task, cb.result) })
	}
	c.publishCompletion(true)
}

// Wait blocks until all tasks are complete.
func (c *Coordinator) Wait() map[string]*AgentResult {
	select {
	case <-c.done:
	case <-c.ctx.Done():
		c.finalizeStoppedTasks(c.ctx.Err())
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resultsSnapshotLocked()
}

// WaitWithTimeout waits for completion with a timeout.
func (c *Coordinator) WaitWithTimeout(timeout time.Duration) (map[string]*AgentResult, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-c.done:
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.resultsSnapshotLocked(), nil
	case <-timer.C:
		return nil, fmt.Errorf("coordination timed out after %v", timeout)
	case <-c.ctx.Done():
		c.finalizeStoppedTasks(c.ctx.Err())
		return nil, c.ctx.Err()
	}
}

// GetTask returns a task by ID.
func (c *Coordinator) GetTask(taskID string) *CoordinatedTask {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneCoordinatedTask(c.tasks[taskID])
}

// GetTaskAgentID returns the agent ID assigned to a task, if it's currently running.
func (c *Coordinator) GetTaskAgentID(taskID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for agentID, tid := range c.running {
		if tid == taskID {
			return agentID
		}
	}
	return ""
}

// GetAllTasks returns all tasks.
func (c *Coordinator) GetAllTasks() []*CoordinatedTask {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tasks := make([]*CoordinatedTask, 0, len(c.tasks))
	for _, task := range c.tasks {
		tasks = append(tasks, cloneCoordinatedTask(task))
	}
	return tasks
}

// GetStatus returns the current status summary.
func (c *Coordinator) GetStatus() *CoordinatorStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := &CoordinatorStatus{
		TotalTasks:     len(c.tasks),
		CompletedTasks: len(c.completed),
		RunningTasks:   len(c.running),
	}

	for _, task := range c.tasks {
		switch task.Status {
		case TaskStatusPending:
			status.PendingTasks++
		case TaskStatusBlocked:
			status.BlockedTasks++
		case TaskStatusReady:
			status.ReadyTasks++
		case TaskStatusFailed:
			status.FailedTasks++
		}
	}

	return status
}

// CoordinatorStatus represents the current state of coordination.
type CoordinatorStatus struct {
	TotalTasks     int
	PendingTasks   int
	BlockedTasks   int
	ReadyTasks     int
	RunningTasks   int
	CompletedTasks int
	FailedTasks    int
}

// SetCallbacks sets callback functions.
func (c *Coordinator) SetCallbacks(
	onStart func(*CoordinatedTask),
	onComplete func(*CoordinatedTask, *AgentResult),
	onAllComplete func(map[string]*AgentResult),
) {
	c.mu.Lock()
	c.onTaskStart = onStart
	c.onTaskComplete = onComplete
	c.onAllComplete = onAllComplete
	finished := c.finished
	results := c.resultsSnapshotLocked()
	c.mu.Unlock()

	if finished && onAllComplete != nil {
		callCoordinatorObserver("all tasks complete", func() { onAllComplete(results) })
	}
}

// UIBroadcaster interface for sending task events to UI.
type UIBroadcaster interface {
	BroadcastTaskStarted(taskID, message, planType string)
	BroadcastTaskCompleted(taskID string, success bool, duration time.Duration, err error, planType string)
	BroadcastTaskProgress(taskID string, progress float64, message string)
}

// SetUIBroadcaster sets the UI broadcaster for sending task events.
func (c *Coordinator) SetUIBroadcaster(broadcaster UIBroadcaster) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Wire up callbacks to broadcast to UI
	c.onTaskStart = func(task *CoordinatedTask) {
		if broadcaster != nil {
			broadcaster.BroadcastTaskStarted(task.ID, task.Prompt, string(task.AgentType))
		}
	}

	c.onTaskComplete = func(task *CoordinatedTask, result *AgentResult) {
		if broadcaster != nil {
			var err error
			if result != nil && result.Error != "" {
				err = fmt.Errorf("%s", result.Error)
			}
			success := result != nil && result.Status == AgentStatusCompleted
			duration := time.Duration(0)
			if result != nil {
				duration = result.Duration
			}
			broadcaster.BroadcastTaskCompleted(task.ID, success, duration, err, string(task.AgentType))
		}
	}
}

// CancelTask cancels a specific task.
func (c *Coordinator) CancelTask(taskID string) error {
	c.mu.RLock()
	task := c.tasks[taskID]
	if task == nil {
		c.mu.RUnlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed {
		c.mu.RUnlock()
		return fmt.Errorf("task is already complete: %s", taskID)
	}
	agentID := ""
	for runningID, tid := range c.running {
		if tid == taskID {
			agentID = runningID
			break
		}
	}
	c.mu.RUnlock()

	if agentID != "" {
		if err := c.runner.Cancel(agentID); err != nil {
			// The agent may have completed between the snapshots above. Reconcile
			// that terminal result before surfacing a cancellation error.
			c.handleAgentCompletion(agentID)
			c.mu.RLock()
			current := c.tasks[taskID]
			terminal := current != nil && (current.Status == TaskStatusCompleted || current.Status == TaskStatusFailed)
			c.mu.RUnlock()
			if terminal {
				return nil
			}
			return err
		}
	}

	c.mu.Lock()
	task = c.tasks[taskID]
	if task == nil {
		c.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed {
		c.mu.Unlock()
		return nil
	}
	if agentID != "" {
		delete(c.running, agentID)
	}

	// Remove from queue if pending/ready
	c.queue.RemoveTask(taskID)
	task.Status = TaskStatusFailed
	task.Result = &AgentResult{
		AgentID:   agentID,
		Type:      task.AgentType,
		Status:    AgentStatusCancelled,
		Error:     "cancelled by coordinator",
		Completed: true,
	}
	c.markCompletedLocked(taskID)
	c.unblockDependents(taskID)
	needsCleanup := len(c.completed) > MaxCoordinatorTasks
	onComplete := c.onTaskComplete
	taskSnapshot := cloneCoordinatedTask(task)
	resultSnapshot := cloneAgentResult(task.Result)
	c.mu.Unlock()

	if onComplete != nil {
		callCoordinatorObserver("task complete", func() { onComplete(taskSnapshot, resultSnapshot) })
	}
	if needsCleanup {
		c.cleanupCompletedTasks()
	}
	c.publishCompletion(false)
	return nil
}

func cloneCoordinatedTask(task *CoordinatedTask) *CoordinatedTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Dependencies = append([]string(nil), task.Dependencies...)
	clone.Result = cloneAgentResult(task.Result)
	clone.index = -1
	return &clone
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

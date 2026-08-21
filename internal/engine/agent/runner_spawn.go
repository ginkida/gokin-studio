package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
	"github.com/ginkida/gokin-studio/internal/engine/permission"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// Spawn creates and starts a new agent with the given task.
// agentType should be a built-in type or a dynamic type registered on Runner.
// Also supports dynamic types registered via AgentTypeRegistry.
func (r *Runner) Spawn(ctx context.Context, agentType string, prompt string, maxTurns int, model string) (string, error) {
	ctx = r.executionContext(ctx)
	deps := r.snapshotAgentDeps()
	var err error
	agentType, model, err = normalizeAgentSpawnRequest(deps, agentType, prompt, maxTurns, model)
	if err != nil {
		return "", err
	}

	// Cleanup old completed agents and results to prevent unbounded growth
	r.cleanupOldResults()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	r.mu.Unlock()
	defer r.markExecutionFinished(agent.ID)

	// Wire tool activity to both meta-agent and UI
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()

	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		safeSubAgentActivityEvent(onSubAgentActivity, id, string(agent.Type), toolName, args, "tool_"+status)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(agent.Type), "start")

	// Run agent synchronously with per-agent timeout
	runCtx := ctx
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
		defer cancel()
	}
	startTime := time.Now()
	result, err := r.runAgentSafely(agent, runCtx, prompt, "agent panic")
	duration := time.Since(startTime)

	// Report activity after completion
	r.reportActivity()

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	// Save agent state for potential resume
	r.saveAgentState(agent)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "agent returned nil result",
			Completed: true,
			Duration:  duration,
		}
	}
	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn")

	r.mu.Lock()
	r.setResultLocked(agent.ID, result)
	r.mu.Unlock()
	r.notifyResultReady()
	notifyAgentTerminalCallbacks(nil, onSubAgentActivity, agent, result)

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// SpawnWithContext creates and runs a sub-agent with project context and streaming.
// Unlike Spawn, it returns the AgentResult directly for immediate use by the caller.
// When skipPermissions is true, the sub-agent will not ask for permission before
// executing tools (used for approved plan execution).
func (r *Runner) SpawnWithContext(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	projectContext string,
	onText func(string),
	skipPermissions bool,
	progressCallback ProgressCallback,
) (string, *AgentResult, error) {
	ctx = r.executionContext(ctx)
	deps := r.snapshotAgentDeps()
	var validationErr error
	agentType, model, validationErr = normalizeAgentSpawnRequest(deps, agentType, prompt, maxTurns, model)
	if validationErr != nil {
		return "", nil, validationErr
	}

	r.cleanupOldResults()

	// Pass nil permissions for approved plan execution to avoid per-tool prompts
	var perms *permission.Manager
	if !skipPermissions {
		perms = deps.permissions
	}
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, perms)

	// Inject project context and streaming callbacks
	agent.SetProjectContext(projectContext)
	agent.SetOnText(onText)

	// Wire thinking callback from runner
	r.mu.RLock()
	onThinking := r.onThinking
	r.mu.RUnlock()
	if onThinking != nil {
		agent.SetOnThinking(onThinking)
	}

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

	// Wire progress callback for real-time sub-agent progress
	if progressCallback != nil {
		agent.SetProgressCallback(progressCallback)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agentID := agent.ID
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		safeSubAgentActivityEvent(onSubAgentActivity, id, string(agent.Type), toolName, args, "tool_"+status)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agentID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	r.mu.Unlock()
	defer r.markExecutionFinished(agent.ID)

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agentID, agent.Type)
	}

	r.reportActivity()
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(agent.Type), "start")

	// Apply per-agent timeout and store cancel func for explicit Cancel()
	var runCtx context.Context
	var runCancel context.CancelFunc
	if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, agentTimeout)
	} else {
		runCtx, runCancel = context.WithCancel(ctx)
	}
	defer runCancel()
	agent.SetCancelFunc(runCancel)

	startTime := time.Now()
	result, err := r.runAgentSafely(agent, runCtx, prompt, "agent panic")
	duration := time.Since(startTime)

	// Ensure result is never nil (matches SpawnAsync pattern)
	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
		}
	}

	if err != nil {
		applyAgentRunError(agent, result, err)
	}

	// Ensure Completed is always true so WaitWithContext doesn't spin
	result.Completed = true

	// Save error checkpoint on failure for potential recovery
	if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
		if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
			logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
		}
	}

	r.reportActivity()

	// Unregister from meta-agent
	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agentID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	r.saveAgentState(agent)
	r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_with_context")

	r.mu.Lock()
	r.setResultLocked(agent.ID, result)
	r.mu.Unlock()
	r.notifyResultReady()
	notifyAgentTerminalCallbacks(nil, onSubAgentActivity, agent, result)

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}

	return agent.ID, result, err
}

// SpawnAsync creates and starts a new agent asynchronously.
// Invalid requests are rejected before any Runner state is published and return
// an empty ID; callers using the AgentRunner interface surface that as a launch
// failure because its asynchronous methods cannot return an error separately.
func (r *Runner) SpawnAsync(ctx context.Context, agentType string, prompt string, maxTurns int, model string) string {
	ctx = r.executionContext(ctx)
	deps := r.snapshotAgentDeps()
	requestedType := agentType
	var err error
	agentType, model, err = normalizeAgentSpawnRequest(deps, agentType, prompt, maxTurns, model)
	if err != nil {
		logging.Warn("rejecting invalid async agent spawn", "agent_type", requestedType, "error", err)
		return ""
	}

	r.cleanupOldResults()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Wire checkpoint store and enable auto-checkpoint for long-running agents
	if r.store != nil {
		agent.SetStore(r.store)
		if maxTurns > 10 {
			agent.EnableAutoCheckpoint(0)
		}
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Wire tool activity to both meta-agent and UI
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()

	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		safeSubAgentActivityEvent(onSubAgentActivity, id, string(agent.Type), toolName, args, "tool_"+status)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	safeAgentStartCallback(onStart, agent.ID, agentType, prompt)
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, agentType, "start")

	// Run agent asynchronously with proper cleanup
	go func() {
		agentID := agent.ID
		defer r.markExecutionFinished(agentID)
		defer r.recoverAsyncAgentPanic(agent, deps, onComplete, onSubAgentActivity, "agent panic")

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			agent.Cancel()
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.saveAgentState(agent)
			r.mu.Lock()
			r.setResultLocked(agentID, cancelledResult)
			r.mu.Unlock()
			r.notifyResultReady()
			notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, cancelledResult)
			return
		default:
		}

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					safeAgentProgressCallback(onAgentProgress, agentID, &progress)
				case <-progressCtx.Done():
					return
				}
			}
		}()

		startTime := time.Now()
		result, err := r.runAgentSafely(agent, agentCtx, prompt, "agent panic")
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			applyAgentRunError(agent, result, err)
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save error checkpoint on failure for potential recovery
		if err != nil && r.store != nil && agent.GetTurnCount() > 0 {
			if _, cpErr := agent.SaveCheckpoint("error"); cpErr != nil {
				logging.Debug("failed to save error checkpoint", "agent_id", agent.ID, "error", cpErr)
			}
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async")

		r.mu.Lock()
		r.setResultLocked(agentID, result)
		r.mu.Unlock()
		r.notifyResultReady()
		notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, result)
	}()

	return agent.ID
}

// SpawnAsyncWithStreaming creates and starts a new agent asynchronously with streaming support.
// The onText callback receives real-time text output from the agent.
// The onProgress callback receives progress updates.
func (r *Runner) SpawnAsyncWithStreaming(
	ctx context.Context,
	agentType string,
	prompt string,
	maxTurns int,
	model string,
	onText func(string),
	onProgress func(id string, progress *AgentProgress),
) string {
	ctx = r.executionContext(ctx)
	deps := r.snapshotAgentDeps()
	requestedType := agentType
	var err error
	agentType, model, err = normalizeAgentSpawnRequest(deps, agentType, prompt, maxTurns, model)
	if err != nil {
		logging.Warn("rejecting invalid streaming agent spawn", "agent_type", requestedType, "error", err)
		return ""
	}

	r.cleanupOldResults()
	agent := r.newConfiguredAgent(ctx, deps, agentType, maxTurns, model, deps.permissions)

	// Set up streaming callbacks
	if onText != nil {
		agent.SetOnText(onText)
	}

	// Wire thinking callback from runner
	r.mu.RLock()
	onThinking := r.onThinking
	r.mu.RUnlock()
	if onThinking != nil {
		agent.SetOnThinking(onThinking)
	}

	// Wire sub-agent activity callback (chain with meta-agent UpdateActivity)
	r.mu.RLock()
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.RUnlock()
	agent.SetOnToolActivity(func(id, toolName string, args map[string]any, status string) {
		safeSubAgentActivityEvent(onSubAgentActivity, id, string(agent.Type), toolName, args, "tool_"+status)
		if deps.metaAgent != nil && status == "start" {
			deps.metaAgent.UpdateActivity(agent.ID, toolName, agent.GetTurnCount())
		}
	})

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	r.results[agent.ID] = &AgentResult{
		AgentID: agent.ID,
		Type:    agent.Type,
		Status:  AgentStatusPending,
	}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onAgentProgress := r.onAgentProgress
	r.mu.Unlock()

	// Register with meta-agent for monitoring
	if deps.metaAgent != nil {
		deps.metaAgent.RegisterAgent(agent.ID, agent.Type)
	}

	// Report activity to coordinator
	r.reportActivity()

	// Notify UI about agent start
	safeAgentStartCallback(onStart, agent.ID, agentType, prompt)
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, agentType, "start")

	// Run agent asynchronously with streaming and progress updates
	go func() {
		agentID := agent.ID
		defer r.markExecutionFinished(agentID)
		defer r.recoverAsyncAgentPanic(agent, deps, onComplete, onSubAgentActivity, "agent panic")

		// Detach from caller's context so agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		var agentCtx context.Context
		var agentCancel context.CancelFunc
		if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(bgCtx, agentTimeout)
		} else {
			agentCtx, agentCancel = context.WithCancel(bgCtx)
		}
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Start progress ticker for periodic updates
		progressTicker := time.NewTicker(2 * time.Second)
		defer progressTicker.Stop()

		// Create a context with cancellation for the progress goroutine
		progressCtx, progressCancel := context.WithCancel(agentCtx)
		defer progressCancel()

		// Progress update goroutine
		go func() {
			for {
				select {
				case <-progressTicker.C:
					progress := agent.GetProgress()
					safeAgentProgressCallback(onProgress, agentID, &progress)
					safeAgentProgressCallback(onAgentProgress, agentID, &progress)
				case <-progressCtx.Done():
					return
				}
			}
		}()

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			agent.Cancel()
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agentID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.saveAgentState(agent)
			r.mu.Lock()
			r.setResultLocked(agentID, cancelledResult)
			r.mu.Unlock()
			r.notifyResultReady()
			notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, cancelledResult)
			return
		default:
		}

		startTime := time.Now()
		result, err := r.runAgentSafely(agent, agentCtx, prompt, "agent panic")
		duration := time.Since(startTime)

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agentID,
				Type:      agent.Type,
				Status:    AgentStatusFailed,
				Error:     "nil result from agent",
				Completed: true,
			}
		}

		// Handle error by updating result status
		if err != nil {
			applyAgentRunError(agent, result, err)
		}

		// Ensure Completed is always true so WaitWithContext doesn't spin
		result.Completed = true

		// Unregister from meta-agent
		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agentID)
		}

		// Save agent state for potential resume
		r.finalizeAgentWorkspace(agent, result)
		r.saveAgentState(agent)
		r.recordAgentExecutionLearning(deps, agentType, prompt, result, duration, "spawn_async_streaming")

		r.mu.Lock()
		r.setResultLocked(agentID, result)
		r.mu.Unlock()
		r.notifyResultReady()
		notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, result)
	}()

	return agent.ID
}

// SpawnMultiple creates and starts multiple agents in parallel.
func (r *Runner) SpawnMultiple(ctx context.Context, tasks []AgentTask) ([]string, error) {
	ctx = r.executionContext(ctx)
	deps := r.snapshotAgentDeps()
	validatedTasks := append([]AgentTask(nil), tasks...)
	for index := range validatedTasks {
		task := &validatedTasks[index]
		agentType, model, err := normalizeAgentSpawnRequest(deps, string(task.Type), task.Prompt, task.MaxTurns, task.Model)
		if err != nil {
			return nil, fmt.Errorf("task %d: %w", index+1, err)
		}
		task.Type = AgentType(agentType)
		task.Model = model
		if task.Thoroughness != "" {
			task.Thoroughness = strings.ToLower(strings.TrimSpace(task.Thoroughness))
			switch task.Thoroughness {
			case "quick", "normal", "thorough":
			default:
				return nil, fmt.Errorf("task %d: unknown thoroughness %q", index+1, task.Thoroughness)
			}
		}
		if task.OutputStyle != "" {
			task.OutputStyle = strings.ToLower(strings.TrimSpace(task.OutputStyle))
			switch task.OutputStyle {
			case "concise", "normal", "detailed":
			default:
				return nil, fmt.Errorf("task %d: unknown output style %q", index+1, task.OutputStyle)
			}
		}
	}

	r.cleanupOldResults()
	ids := make([]string, len(validatedTasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range validatedTasks {
		wg.Add(1)
		go func(idx int, t AgentTask) {
			defer wg.Done()

			agentConfigCtx := ctx
			if t.Thoroughness != "" {
				agentConfigCtx = tools.WithThoroughness(agentConfigCtx, tools.ParseThoroughness(t.Thoroughness))
			}
			if t.OutputStyle != "" {
				agentConfigCtx = tools.WithOutputStyle(agentConfigCtx, tools.ParseOutputStyle(t.OutputStyle))
			}
			agent := r.newConfiguredAgent(agentConfigCtx, deps, string(t.Type), t.MaxTurns, t.Model, deps.permissions)

			r.mu.Lock()
			r.agents[agent.ID] = agent
			r.markExecutionStartedLocked(agent.ID)
			onSubAgentActivity := r.onSubAgentActivity
			r.mu.Unlock()
			defer r.markExecutionFinished(agent.ID)

			attachMetaAgentMonitoring(agent, deps.metaAgent, onSubAgentActivity)
			safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(agent.Type), "start")

			// Apply per-agent timeout
			runCtx := ctx
			if agentTimeout := agent.GetTimeout(); agentTimeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(ctx, agentTimeout)
				defer cancel()
			}
			startTime := time.Now()
			result, err := r.runAgentSafely(agent, runCtx, t.Prompt, "agent panic")
			duration := time.Since(startTime)

			// Ensure result is never nil (matches SpawnAsync pattern)
			if result == nil {
				result = &AgentResult{
					AgentID:   agent.ID,
					Type:      agent.Type,
					Status:    AgentStatusFailed,
					Error:     "nil result from agent",
					Completed: true,
				}
			}
			if err != nil {
				applyAgentRunError(agent, result, err)
			}
			result.Completed = true

			// Unregister from meta-agent
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}

			workspaceErr := r.finalizeAgentWorkspace(agent, result)
			r.saveAgentState(agent)
			r.recordAgentExecutionLearning(deps, string(t.Type), t.Prompt, result, duration, "spawn_multiple")
			if err == nil && workspaceErr != nil {
				err = workspaceErr
			}

			mu.Lock()
			ids[idx] = agent.ID
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()

			r.mu.Lock()
			r.setResultLocked(agent.ID, result)
			r.mu.Unlock()
			r.notifyResultReady()
			notifyAgentTerminalCallbacks(nil, onSubAgentActivity, agent, result)
		}(i, task)
	}

	wg.Wait()

	return ids, firstErr
}

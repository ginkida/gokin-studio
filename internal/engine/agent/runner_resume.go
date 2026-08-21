package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

// Resume resumes an agent from a saved state.
func (r *Runner) Resume(ctx context.Context, agentID string, prompt string) (string, error) {
	ctx = r.executionContext(ctx)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be blank")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("resume prompt must not be blank")
	}
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}
	r.cleanupOldResults()

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	if err := validateRestoredAgentState(deps, state); err != nil {
		return "", err
	}
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.Unlock()
	defer r.markExecutionFinished(agent.ID)

	attachMetaAgentMonitoring(agent, deps.metaAgent, onSubAgentActivity)
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(state.Type), "start")

	// Run agent with the new prompt (continuing from previous context)
	startTime := time.Now()
	result, err := r.runAgentSafely(agent, ctx, prompt, "agent panic on resume")
	duration := time.Since(startTime)

	if result == nil {
		result = &AgentResult{
			AgentID:   agent.ID,
			Type:      agent.Type,
			Status:    AgentStatusFailed,
			Error:     "nil result from agent",
			Completed: true,
			Duration:  duration,
		}
	}
	if err != nil {
		applyAgentRunError(agent, result, err)
	}
	result.Completed = true

	if deps.metaAgent != nil {
		deps.metaAgent.UnregisterAgent(agent.ID)
	}

	workspaceErr := r.finalizeAgentWorkspace(agent, result)
	// Save updated state
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}

	r.mu.Lock()
	r.setResultLocked(agent.ID, result)
	r.mu.Unlock()
	r.notifyResultReady()
	r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume")
	notifyAgentTerminalCallbacks(nil, onSubAgentActivity, agent, result)

	if err == nil && workspaceErr != nil {
		err = workspaceErr
	}
	if err != nil {
		return agent.ID, err
	}

	return agent.ID, nil
}

// ResumeAsync resumes an agent asynchronously.
func (r *Runner) ResumeAsync(ctx context.Context, agentID string, prompt string) (string, error) {
	ctx = r.executionContext(ctx)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", fmt.Errorf("agent ID must not be blank")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("resume prompt must not be blank")
	}
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}
	r.cleanupOldResults()

	// Load state from store
	state, err := r.store.Load(agentID)
	if err != nil {
		return "", fmt.Errorf("failed to load agent state: %w", err)
	}

	// Create a new agent with the same configuration
	deps := r.snapshotAgentDeps()
	if err := validateRestoredAgentState(deps, state); err != nil {
		return "", err
	}
	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	// Restore history
	if err := agent.RestoreHistory(state); err != nil {
		return "", fmt.Errorf("failed to restore agent history: %w", err)
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
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent, onSubAgentActivity)

	// Notify UI about agent start (resumed)
	safeAgentStartCallback(onStart, agent.ID, string(state.Type), prompt)
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(state.Type), "start")

	// Run agent asynchronously with proper cleanup
	go func() {
		defer r.markExecutionFinished(agent.ID)
		defer r.recoverAsyncAgentPanic(agent, deps, onComplete, onSubAgentActivity, "agent panic")

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()

		// Store cancel func for explicit Agent.Cancel()
		agent.SetCancelFunc(agentCancel)

		// Check if original context is already cancelled
		select {
		case <-ctx.Done():
			agent.Cancel()
			if deps.metaAgent != nil {
				deps.metaAgent.UnregisterAgent(agent.ID)
			}
			cancelledResult := &AgentResult{
				AgentID:   agent.ID,
				Type:      state.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(agent, cancelledResult)
			r.saveAgentState(agent)
			r.mu.Lock()
			r.setResultLocked(agent.ID, cancelledResult)
			r.mu.Unlock()
			r.notifyResultReady()
			notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, cancelledResult)
			return
		default:
		}

		result, err := r.runAgentSafely(agent, agentCtx, prompt, "agent panic on resume")
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}

		// Ensure result is never nil
		if result == nil {
			result = &AgentResult{
				AgentID:   agent.ID,
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

		if deps.metaAgent != nil {
			deps.metaAgent.UnregisterAgent(agent.ID)
		}

		r.finalizeAgentWorkspace(agent, result)
		// Save updated state
		if r.store != nil {
			if saveErr := r.store.Save(agent); saveErr != nil {
				logging.Warn("failed to save agent state",
					"agent_id", agent.ID,
					"error", saveErr)
			}
		}

		r.mu.Lock()
		r.setResultLocked(agent.ID, result)
		r.mu.Unlock()
		r.notifyResultReady()
		r.recordAgentExecutionLearning(deps, string(state.Type), prompt, result, duration, "resume_async")

		notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, agent, result)
	}()

	return agent.ID, nil
}

// saveAgentState saves the agent state if store is configured.
func (r *Runner) saveAgentState(agent *Agent) {
	if r.store != nil {
		if err := r.store.Save(agent); err != nil {
			logging.Warn("failed to save agent state", "agent_id", agent.ID, "error", err)
		}
	}
}

// CleanupOldCheckpoints removes checkpoint files older than maxAge.
func (r *Runner) CleanupOldCheckpoints(maxAge time.Duration) {
	if r.store == nil {
		return
	}
	cleaned, err := r.store.CleanupOldCheckpointFiles(maxAge)
	if err != nil {
		logging.Debug("checkpoint cleanup error", "error", err)
	} else if cleaned > 0 {
		logging.Debug("cleaned up old checkpoint files", "count", cleaned)
	}
}

// ResumeErrorCheckpoints finds error checkpoints and silently resumes agents in the background.
// Each checkpoint is deleted before resume to prevent infinite retry loops.
// Resumed agents do NOT get auto-checkpoint enabled — if they fail again, no new error checkpoint is created.
// Returns the number of agents successfully resumed.
func (r *Runner) ResumeErrorCheckpoints(ctx context.Context) int {
	ctx = r.executionContext(ctx)
	if r.store == nil {
		return 0
	}
	r.cleanupOldResults()

	checkpoints, err := r.store.ListErrorCheckpoints()
	if err != nil || len(checkpoints) == 0 {
		return 0
	}

	resumed := 0
	for _, cp := range checkpoints {
		if cp.AgentState == nil {
			_ = r.store.DeleteCheckpoint(cp.CheckpointID)
			continue
		}

		// Delete BEFORE resume — anti-infinite-retry
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)

		// Create agent, restore from checkpoint
		state := cp.AgentState
		deps := r.snapshotAgentDeps()
		if err := validateRestoredAgentState(deps, state); err != nil {
			logging.Debug("skipping invalid error checkpoint", "checkpoint_id", cp.CheckpointID, "error", err)
			continue
		}
		agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)

		// Store without auto-checkpoint (if it fails again, no new error cp)
		if r.store != nil {
			agent.SetStore(r.store)
		}

		if err := agent.RestoreFromCheckpoint(cp); err != nil {
			logging.Debug("failed to restore from checkpoint", "checkpoint_id", cp.CheckpointID, "error", err)
			continue
		}

		r.mu.Lock()
		r.agents[agent.ID] = agent
		r.markExecutionStartedLocked(agent.ID)
		r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
		onStart := r.onAgentStart
		onComplete := r.onAgentComplete
		onSubAgentActivity := r.onSubAgentActivity
		r.mu.Unlock()

		attachMetaAgentMonitoring(agent, deps.metaAgent, onSubAgentActivity)

		stateType := string(state.Type)
		resumePrompt := "You were restarted after an error. Continue your previous task or report what went wrong."
		safeAgentStartCallback(onStart, agent.ID, stateType, resumePrompt)
		safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, stateType, "start")

		go func(a *Agent, runDeps runnerAgentDeps, restoredType string, continuationPrompt string) {
			defer r.markExecutionFinished(a.ID)
			defer r.recoverAsyncAgentPanic(a, runDeps, onComplete, onSubAgentActivity, "agent panic on resume")

			// Detach from caller's context so resumed agent survives tool timeout.
			bgCtx := context.WithoutCancel(ctx)
			agentCtx, agentCancel := context.WithCancel(bgCtx)
			defer agentCancel()
			a.SetCancelFunc(agentCancel)

			select {
			case <-ctx.Done():
				a.Cancel()
				if runDeps.metaAgent != nil {
					runDeps.metaAgent.UnregisterAgent(a.ID)
				}
				cancelledResult := &AgentResult{
					AgentID:   a.ID,
					Type:      a.Type,
					Status:    AgentStatusCancelled,
					Error:     ctx.Err().Error(),
					Completed: true,
				}
				r.finalizeAgentWorkspace(a, cancelledResult)
				r.saveAgentState(a)
				r.mu.Lock()
				r.setResultLocked(a.ID, cancelledResult)
				r.mu.Unlock()
				r.notifyResultReady()
				notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, a, cancelledResult)
				return
			default:
			}

			result, err := r.runAgentSafely(a, agentCtx, continuationPrompt, "agent panic on resume")
			var duration time.Duration
			if result != nil {
				duration = result.Duration
			}
			if result == nil {
				result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
			}
			if err != nil {
				applyAgentRunError(a, result, err)
			}
			result.Completed = true

			if runDeps.metaAgent != nil {
				runDeps.metaAgent.UnregisterAgent(a.ID)
			}

			r.finalizeAgentWorkspace(a, result)
			r.saveAgentState(a)
			r.recordAgentExecutionLearning(
				runDeps,
				restoredType,
				continuationPrompt,
				result,
				duration,
				"resume_error_checkpoint",
			)

			r.mu.Lock()
			r.setResultLocked(a.ID, result)
			r.mu.Unlock()
			r.notifyResultReady()

			notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, a, result)
		}(agent, deps, stateType, resumePrompt)

		resumed++
	}

	if resumed > 0 {
		logging.Debug("auto-resumed agents from error checkpoints", "count", resumed)
	}
	return resumed
}

// ResumeLastCheckpoint finds the most recent checkpoint across all agents and resumes it.
// The checkpoint is deleted before resume to prevent duplicate runs.
// Returns the agent ID and any error.
func (r *Runner) ResumeLastCheckpoint(ctx context.Context) (string, error) {
	ctx = r.executionContext(ctx)
	if r.store == nil {
		return "", fmt.Errorf("agent store not configured")
	}
	r.cleanupOldResults()

	// List all checkpoints (empty agentID = no filter)
	ids, err := r.store.ListCheckpoints("")
	if err != nil {
		return "", fmt.Errorf("failed to list checkpoints: %w", err)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no checkpoints found")
	}

	// Checkpoint names include timestamps — pick the lexicographically latest
	latestID := ids[0]
	for _, id := range ids[1:] {
		if id > latestID {
			latestID = id
		}
	}

	cp, err := r.store.LoadCheckpoint(latestID)
	if err != nil {
		return "", fmt.Errorf("failed to load checkpoint %s: %w", latestID, err)
	}
	if cp.AgentState == nil {
		_ = r.store.DeleteCheckpoint(cp.CheckpointID)
		return "", fmt.Errorf("checkpoint %s has no agent state", latestID)
	}

	state := cp.AgentState
	deps := r.snapshotAgentDeps()
	if err := validateRestoredAgentState(deps, state); err != nil {
		return "", err
	}

	// Delete only after the state is known to be runnable, but still before the
	// execution is published, to prevent both data loss on validation failures
	// and duplicate successful resumes.
	_ = r.store.DeleteCheckpoint(cp.CheckpointID)

	agent := r.newRestoredAgent(ctx, deps, state, deps.permissions)
	if r.store != nil {
		agent.SetStore(r.store)
	}

	if err := agent.RestoreFromCheckpoint(cp); err != nil {
		return "", fmt.Errorf("failed to restore from checkpoint: %w", err)
	}

	r.mu.Lock()
	r.agents[agent.ID] = agent
	r.markExecutionStartedLocked(agent.ID)
	r.results[agent.ID] = &AgentResult{AgentID: agent.ID, Type: agent.Type, Status: AgentStatusPending}
	onStart := r.onAgentStart
	onComplete := r.onAgentComplete
	onSubAgentActivity := r.onSubAgentActivity
	r.mu.Unlock()

	attachMetaAgentMonitoring(agent, deps.metaAgent, onSubAgentActivity)

	safeAgentStartCallback(onStart, agent.ID, string(state.Type), "Resumed from checkpoint")
	safeSubAgentActivityCallback(onSubAgentActivity, agent.ID, string(state.Type), "start")

	go func(a *Agent, runDeps runnerAgentDeps) {
		defer r.markExecutionFinished(a.ID)
		defer r.recoverAsyncAgentPanic(a, runDeps, onComplete, onSubAgentActivity, "agent panic on resume")

		// Detach from caller's context so resumed agent survives tool timeout.
		bgCtx := context.WithoutCancel(ctx)
		agentCtx, agentCancel := context.WithCancel(bgCtx)
		defer agentCancel()
		a.SetCancelFunc(agentCancel)

		select {
		case <-ctx.Done():
			a.Cancel()
			if runDeps.metaAgent != nil {
				runDeps.metaAgent.UnregisterAgent(a.ID)
			}
			cancelledResult := &AgentResult{
				AgentID:   a.ID,
				Type:      a.Type,
				Status:    AgentStatusCancelled,
				Error:     ctx.Err().Error(),
				Completed: true,
			}
			r.finalizeAgentWorkspace(a, cancelledResult)
			r.saveAgentState(a)
			r.mu.Lock()
			r.setResultLocked(a.ID, cancelledResult)
			r.mu.Unlock()
			r.notifyResultReady()
			notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, a, cancelledResult)
			return
		default:
		}

		result, err := r.runAgentSafely(a, agentCtx, "You were resumed from a checkpoint. Continue your previous task.", "agent panic on resume")
		var duration time.Duration
		if result != nil {
			duration = result.Duration
		}
		if result == nil {
			result = &AgentResult{AgentID: a.ID, Type: a.Type, Status: AgentStatusFailed, Error: "nil result", Completed: true}
		}
		if err != nil {
			applyAgentRunError(a, result, err)
		}
		result.Completed = true

		if runDeps.metaAgent != nil {
			runDeps.metaAgent.UnregisterAgent(a.ID)
		}

		r.finalizeAgentWorkspace(a, result)
		r.saveAgentState(a)
		r.recordAgentExecutionLearning(
			runDeps,
			string(state.Type),
			"You were resumed from a checkpoint. Continue your previous task.",
			result,
			duration,
			"resume_last_checkpoint",
		)

		r.mu.Lock()
		r.setResultLocked(a.ID, result)
		r.mu.Unlock()
		r.notifyResultReady()

		notifyAgentTerminalCallbacks(onComplete, onSubAgentActivity, a, result)
	}(agent, deps)

	logging.Debug("resumed agent from last checkpoint", "agent_id", agent.ID, "checkpoint_id", latestID)
	return agent.ID, nil
}

// Close flushes all agent data (project learning) to prevent data loss on shutdown.
func (r *Runner) Close() {
	r.mu.RLock()
	agents := make([]*Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	store := r.store
	r.mu.RUnlock()
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })

	for _, agent := range agents {
		if err := agent.Close(); err != nil {
			logging.Warn("failed to close agent", "agent_id", agent.ID, "error", err)
		}
	}

	// Cleanup old checkpoints on shutdown, keeping only 2 most recent per agent
	if store != nil {
		for _, agent := range agents {
			if cleaned, err := store.CleanupCheckpoints(agent.ID, 2); err == nil && cleaned > 0 {
				logging.Debug("cleaned up agent checkpoints", "agent_id", agent.ID, "cleaned", cleaned)
			}
		}
	}
}

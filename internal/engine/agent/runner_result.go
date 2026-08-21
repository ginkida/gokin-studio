package agent

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

var removeAgentOutputFile = os.Remove

// Wait waits for an agent to complete and returns its result.
// Uses a default 10-minute timeout. For context-aware waiting, use WaitWithContext.
func (r *Runner) Wait(agentID string) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// notifyResultReady signals that an agent result became complete.
// Non-blocking: if the channel already has a pending signal, the send is skipped.
func (r *Runner) notifyResultReady() {
	select {
	case r.resultReady <- struct{}{}:
	default:
	}
}

// WaitWithContext waits for an agent to complete, respecting context cancellation.
func (r *Runner) WaitWithContext(ctx context.Context, agentID string) (*AgentResult, error) {
	completedResult := func() (*AgentResult, bool) {
		r.mu.RLock()
		result, ok := r.results[agentID]
		result = cloneAgentResult(result)
		r.mu.RUnlock()
		return result, ok && result.Completed
	}
	if result, complete := completedResult(); complete {
		return result, nil
	}

	// resultReady is intentionally coalesced and shared by all agents. A waiter
	// for agent A can consume the notification for B, so retain a low-frequency
	// safety check to guarantee progress with multiple concurrent waiters.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.resultReady:
			if result, complete := completedResult(); complete {
				return result, nil
			}
		case <-ticker.C:
			if result, complete := completedResult(); complete {
				return result, nil
			}
		}
	}
}

// WaitWithTimeout waits for an agent to complete with a specific timeout.
func (r *Runner) WaitWithTimeout(agentID string, timeout time.Duration) (*AgentResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.WaitWithContext(ctx, agentID)
}

// WaitAll waits for multiple agents to complete.
func (r *Runner) WaitAll(agentIDs []string) ([]*AgentResult, error) {
	results := make([]*AgentResult, len(agentIDs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, id := range agentIDs {
		wg.Add(1)
		go func(idx int, agentID string) {
			defer wg.Done()

			result, err := r.Wait(agentID)

			mu.Lock()
			// Ensure result is never nil
			if result == nil {
				result = &AgentResult{
					AgentID:   agentID,
					Status:    AgentStatusFailed,
					Error:     fmt.Sprintf("wait failed: %v", err),
					Completed: true,
				}
			}
			results[idx] = result
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}(i, id)
	}

	wg.Wait()
	return results, firstErr
}

// GetResult returns the result for an agent.
func (r *Runner) GetResult(agentID string) (*AgentResult, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, ok := r.results[agentID]
	return cloneAgentResult(result), ok
}

func cloneAgentResult(result *AgentResult) *AgentResult {
	if result == nil {
		return nil
	}
	clone := *result
	if result.Metadata != nil {
		clone.Metadata = make(map[string]interface{}, len(result.Metadata))
		for key, value := range result.Metadata {
			clone.Metadata[key] = cloneJSONValue(value)
		}
	}
	return &clone
}

// setResultLocked stores a runner-owned snapshot. Callbacks and synchronous
// APIs may retain and mutate their result pointer after return; those changes
// must not rewrite the registry behind r.mu.
func (r *Runner) setResultLocked(agentID string, result *AgentResult) {
	r.results[agentID] = cloneAgentResult(result)
}

// removeAgentLocked removes one finalized agent/result pair. The caller must
// hold r.mu. Failed output cleanup retains the pair so a later pass can retry
// instead of losing the only reference to an orphaned file.
func (r *Runner) removeAgentLocked(agentID string) bool {
	if r.activeExecutions[agentID] > 0 {
		return false
	}
	if result, ok := r.results[agentID]; ok && result.OutputFile != "" {
		if err := removeAgentOutputFile(result.OutputFile); err != nil && !os.IsNotExist(err) {
			return false
		}
	}
	delete(r.agents, agentID)
	delete(r.results, agentID)
	delete(r.activeExecutions, agentID)
	return true
}

// GetAgent returns an agent by ID.
func (r *Runner) GetAgent(agentID string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[agentID]
	return agent, ok
}

// Cancel cancels an agent's execution.
func (r *Runner) Cancel(agentID string) error {
	r.mu.Lock()

	agent, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("agent not found: %s", agentID)
	}
	status := agent.GetStatus()
	if status != AgentStatusPending && status != AgentStatusRunning {
		r.mu.Unlock()
		return fmt.Errorf("agent is not running: %s", agentID)
	}

	agent.Cancel()

	// Publish cancellation immediately, but keep Completed false until the Run
	// goroutine has closed output and finalized its authoritative result.
	if result, ok := r.results[agentID]; ok {
		result.Status = AgentStatusCancelled
	}
	r.mu.Unlock()

	return nil
}

// ListAgents returns all agent IDs.
func (r *Runner) ListAgents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ListRunning returns IDs of currently running agents.
func (r *Runner) ListRunning() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0)
	for id, agent := range r.agents {
		if agent.GetStatus() == AgentStatusRunning {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Cleanup removes completed agents older than the specified duration.
// Also removes associated output files from disk.
func (r *Runner) Cleanup(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0

	for id, agent := range r.agents {
		if r.activeExecutions[id] > 0 {
			continue
		}
		status := agent.GetStatus()
		if status == AgentStatusCompleted || status == AgentStatusFailed || status == AgentStatusCancelled {
			endTime := agent.GetEndTime()
			if !endTime.IsZero() && endTime.Before(cutoff) {
				if r.removeAgentLocked(id) {
					cleaned++
				}
			}
		}
	}

	return cleaned
}

// SetStore sets the agent store for persistence.
func (r *Runner) SetStore(store *AgentStore) {
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

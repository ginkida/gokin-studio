package studio

import (
	"fmt"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
)

// Bounded fan-out.
//
// This is the honest replacement for the `coordinate` tool, which returned a
// markdown to-do list telling the model to do the work itself because no real
// agent.Coordinator was ever wired.
//
// The decision that makes it safe: the optional synthesis step is a REAL QUEUED
// TURN in the caller's own session, not a hidden extra provider call. As a
// queued turn it is visible in the transcript, cancellable with Stop, billed
// through recordResponse, and subject to the caller's own budget — which is
// exactly what the previous cross-agent paths failed to be.

const maxDelegationBatchTargets = tools.DelegateBatchMaxTargets

// DelegationTarget is one item of a fan-out request.
type DelegationTarget struct {
	ProjectID string `json:"projectID"`
	Task      string `json:"task"`
}

// DelegationBatchResult reports what a fan-out started.
type DelegationBatchResult struct {
	BatchID         string          `json:"batchID"`
	Runs            []DelegationRun `json:"runs"`
	AggregateQueued bool            `json:"aggregateQueued"`
}

// StartDelegationBatch validates every target before any paid work begins and
// then starts one delegation per target under the same global concurrency cap.
//
// One bad target rejects the WHOLE batch: a partially-started fan-out is a
// spend commitment the user never approved and cannot easily reason about.
func (s *Studio) StartDelegationBatch(fromProjectID, fromSessionID string, targets []DelegationTarget, goal, task string) (DelegationBatchResult, error) {
	return s.startDelegationBatch(fromProjectID, fromSessionID, targets, goal, task, nil)
}

// startDelegationBatch carries the caller's chain stamp. Without it every
// fan-out target would begin a brand-new chain, so the depth and cycle rules
// could not see the hops that led here — a batch would be a way around them.
func (s *Studio) startDelegationBatch(fromProjectID, fromSessionID string, targets []DelegationTarget, goal, task string, parent *delegationStamp) (DelegationBatchResult, error) {
	if len(targets) == 0 {
		return DelegationBatchResult{}, fmt.Errorf("at least one target is required")
	}
	if len(targets) > maxDelegationBatchTargets {
		return DelegationBatchResult{}, fmt.Errorf(
			"a fan-out addresses at most %d projects", maxDelegationBatchTargets)
	}
	fromSid := fromSessionID
	if fromSid == "" {
		fromSid = "default"
	}

	// Validate everything first — existence, distinctness, self-target, and the
	// shared task fallback — so nothing starts if the request is malformed.
	seen := make(map[string]bool, len(targets))
	prepared := make([]DelegationTarget, 0, len(targets))
	for _, target := range targets {
		target.ProjectID = strings.TrimSpace(target.ProjectID)
		if target.ProjectID == "" {
			return DelegationBatchResult{}, fmt.Errorf("every target needs a project_id")
		}
		if target.ProjectID == fromProjectID {
			return DelegationBatchResult{}, newDelegationError(DelegationErrorCycle,
				"a fan-out cannot include the calling project")
		}
		if seen[target.ProjectID] {
			return DelegationBatchResult{}, fmt.Errorf(
				"duplicate target %s; each project may appear once", target.ProjectID)
		}
		seen[target.ProjectID] = true
		if strings.TrimSpace(target.Task) == "" {
			target.Task = task
		}
		if strings.TrimSpace(target.Task) == "" {
			return DelegationBatchResult{}, fmt.Errorf(
				"target %s has no task and no shared task was given", target.ProjectID)
		}
		prepared = append(prepared, target)
	}

	// Second pass: prove every target is actually startable BEFORE the first
	// one costs anything. Unwinding a half-started fan-out still bills the runs
	// that already began, so the only honest option is to refuse up front.
	if err := s.preflightDelegationBatch(fromProjectID, prepared, parent); err != nil {
		return DelegationBatchResult{}, err
	}

	batchID := uuid.NewString()
	reservations, err := s.reserveDelegationBatchForSession(fromProjectID, fromSid, batchID, prepared)
	if err != nil {
		return DelegationBatchResult{}, err
	}
	// Activated reservations are left untouched; anything not activated is
	// released on every return path, including a shutdown or session failure.
	defer func() {
		for _, runID := range reservations {
			s.releaseDelegationReservation(runID)
		}
	}()
	runs := make([]DelegationRun, 0, len(prepared))
	for _, target := range prepared {
		run, err := s.startDelegation(delegationRequest{
			FromProjectID: fromProjectID,
			FromSessionID: fromSid,
			ToProjectID:   target.ProjectID,
			Kind:          "run",
			Goal:          goal,
			Task:          target.Task,
			BatchID:       batchID,
			ReservedRunID: reservations[target.ProjectID],
			Parent:        parent,
		})
		if err != nil {
			// Unwind: a half-started fan-out is worse than none.
			for _, started := range runs {
				_ = s.CancelDelegationRun(started.ID)
			}
			return DelegationBatchResult{}, err
		}
		runs = append(runs, run)
	}

	queued := s.scheduleBatchSynthesis(fromProjectID, fromSid, batchID, goal, runs)
	return DelegationBatchResult{BatchID: batchID, Runs: runs, AggregateQueued: queued}, nil
}

// scheduleBatchSynthesis waits for every run in the batch and then enqueues a
// synthesis turn in the CALLER's own session.
//
// Deliberately a queued turn rather than a hidden second provider call: the
// user sees it in their transcript, Stop cancels it, and it is billed and
// budgeted like any other turn they started.
func (s *Studio) scheduleBatchSynthesis(fromProjectID, fromSessionID, batchID, goal string, runs []DelegationRun) bool {
	if len(runs) < 2 {
		// A single delegation reports itself; a synthesis turn would just pay
		// to restate one answer.
		return false
	}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return s.startBackground("delegation-batch", func() {
		ready, waitErr := s.waitForDelegationBatch(ids)
		if waitErr != nil {
			s.LogEvent("error", "delegation", fmt.Sprintf("batch %s cannot read run store: %v", batchID, waitErr))
			return
		}
		if !ready {
			return
		}
		message, messageErr := s.batchSynthesisMessage(batchID, goal, ids)
		if messageErr != nil {
			s.LogEvent("error", "delegation", fmt.Sprintf("batch %s cannot build synthesis: %v", batchID, messageErr))
			return
		}
		if message == "" {
			return
		}
		// Skip if the caller's chat went away or was stopped meanwhile.
		s.mu.RLock()
		project := s.projects[fromProjectID]
		s.mu.RUnlock()
		if project == nil {
			return
		}
		session := project.GetSession(fromSessionID)
		if session == nil || session.ID != fromSessionID {
			return
		}
		queueID := "batch-" + batchID
		if err := s.QueueMessage(fromProjectID, message, fromSessionID, queueID); err != nil {
			// Busy or stopped: the per-run cards already carry the answers, so
			// a missing synthesis is a degradation, not a failure.
			s.LogEvent("info", "delegation", fmt.Sprintf(
				"batch %s synthesis not queued: %v", batchID, err))
			return
		}
		project.emitEvent(s.ctx, EventChatQueueAdded, ChatQueueEvent{
			ProjectID: fromProjectID, SessionID: fromSessionID, ID: queueID, Text: message,
		})
	})
}

// waitForDelegationBatch blocks until every run reaches a terminal state.
// Returns false if the studio is shutting down.
func (s *Studio) waitForDelegationBatch(ids []string) (bool, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return false, nil
		case <-ticker.C:
		}
		pending := false
		for _, id := range ids {
			run, ok, err := loadDelegationRun(id)
			if err != nil {
				return false, fmt.Errorf("read delegation run %s: %w", id, err)
			}
			if !ok {
				return false, fmt.Errorf("delegation run disappeared: %s", id)
			}
			if !delegationRunTerminal(run.Status) {
				if !s.delegationOwnerAlive(run) {
					return false, fmt.Errorf("non-terminal delegation %s has no live owner", id)
				}
				pending = true
				break
			}
		}
		if !pending {
			return true, nil
		}
	}
}

// batchSynthesisMessage carries bounded previews, never full bodies: the caller
// can fetch any answer in full with FetchDelegationAnswer.
func (s *Studio) batchSynthesisMessage(batchID, goal string, ids []string) (string, error) {
	var b strings.Builder
	b.WriteString("## Delegation results\n\n")
	if goal = strings.TrimSpace(goal); goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n\n", goal)
	}
	included := 0
	for _, id := range ids {
		run, ok, err := loadDelegationRun(id)
		if err != nil {
			return "", fmt.Errorf("read delegation run %s: %w", id, err)
		}
		if !ok {
			continue
		}
		name := run.ToProjectID
		s.mu.RLock()
		if project := s.projects[run.ToProjectID]; project != nil {
			project.mu.RLock()
			name = project.Name
			project.mu.RUnlock()
		}
		s.mu.RUnlock()

		status := run.Status
		if run.ErrorType != "" {
			status += " · " + run.ErrorType
		}
		preview := truncateUTF8(strings.TrimSpace(run.Answer), delegationSummaryMaxBytes)
		fmt.Fprintf(&b, "### Project: %s [%s]\n\n", name, status)
		if preview != "" {
			b.WriteString(preview)
			if run.Truncated || run.AnswerBytes > len(preview) {
				fmt.Fprintf(&b, "\n\n(preview of %d bytes; full answer available via delegate action=\"fetch\" run_id=%q)",
					run.AnswerBytes, run.ID)
			}
			b.WriteString("\n\n")
		} else if run.Error != "" {
			fmt.Fprintf(&b, "%s\n\n", run.Error)
		} else {
			b.WriteString("No answer was produced.\n\n")
		}
		included++
	}
	if included == 0 {
		return "", nil
	}
	b.WriteString("## Task\n\n")
	b.WriteString("Synthesise the results above into one answer. Call out where the projects " +
		"disagree rather than averaging them, and end with concrete next steps.\n\n")
	b.WriteString(crossAgentInjectionFooter)
	return b.String(), nil
}

// preflightDelegationBatch checks existence, policy, hop legality, budget and
// availability for every target under one read lock. It creates nothing.
func (s *Studio) preflightDelegationBatch(fromProjectID string, targets []DelegationTarget, parent *delegationStamp) error {
	busy := make(map[string]bool)
	s.delegationMu.Lock()
	for _, handle := range s.delegations {
		busy[handle.toProjectID] = true
	}
	s.delegationMu.Unlock()

	// The whole batch must fit in the remaining global capacity, not just be
	// small on its own: comparing len(targets) to the cap alone is unreachable
	// because the target count is already capped lower.
	s.delegationMu.Lock()
	inFlight := len(s.delegations)
	s.delegationMu.Unlock()
	if inFlight+len(targets) > maxConcurrentDelegations {
		return newDelegationError(DelegationErrorBusy,
			"a fan-out of %d would exceed the %d concurrent delegation limit (%d already running)",
			len(targets), maxConcurrentDelegations, inFlight)
	}

	// Everything that needs s.mu is resolved in ONE read-locked pass; the
	// budget check is deliberately left out of it. Project.totalCostUSD seeds
	// its cache through Studio.ProjectUsageStats, which takes s.mu.RLock, and a
	// recursive read lock deadlocks behind any queued writer.
	resolved := make([]*Project, 0, len(targets))
	s.mu.RLock()
	if _, ok := s.projects[fromProjectID]; !ok {
		s.mu.RUnlock()
		return newDelegationError(DelegationErrorUnknownTarget,
			"source project not found: %s", fromProjectID)
	}
	for _, target := range targets {
		project := s.projects[target.ProjectID]
		if project == nil {
			s.mu.RUnlock()
			return newDelegationError(DelegationErrorUnknownTarget,
				"target project not found: %s", target.ProjectID)
		}
		if busy[target.ProjectID] {
			project.mu.RLock()
			name := project.Name
			project.mu.RUnlock()
			s.mu.RUnlock()
			return newDelegationError(DelegationErrorBusy,
				"project %q is already handling a delegation", name)
		}
		if _, err := s.delegationPolicyAllowsLocked(fromProjectID, project); err != nil {
			s.mu.RUnlock()
			return err
		}
		hop := delegationHop{Applies: true, TargetProject: target.ProjectID, CrossProject: true}
		if errType, refusal := delegationHopAllowed(parent, fromProjectID, hop); errType != "" {
			s.mu.RUnlock()
			return &delegationError{Type: errType, Message: refusal}
		}
		resolved = append(resolved, project)
	}
	s.mu.RUnlock()

	// Read outside the s.mu region above: its read locks are not reentrant.
	batchDefaults := s.settingsSnapshot()
	for _, project := range resolved {
		if err := delegationBudgetAllows(project, batchDefaults); err != nil {
			return err
		}
	}
	return nil
}

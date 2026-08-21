package studio

import (
	"context"
	"fmt"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// The studio side of the `delegate` tool.
//
// The caller is resolved from the routing value the agent loop seeds on the
// tool context — the same mechanism that makes session_agent addressable. It is
// precisely what ask_agent lacked: with no idea who is calling, ask_agent could
// only guess a target, and guessed wrong.

// DelegationTargetInfo is the inert catalog entry returned by
// `delegate action="list"`. It exists so a model can name a real project
// instead of choosing from an invented role enum.
type DelegationTargetInfo struct {
	ProjectID    string   `json:"project_id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	Group        string   `json:"group,omitempty"`
	Busy         bool     `json:"busy"`
	Reachable    bool     `json:"reachable"`
	Reason       string   `json:"reason,omitempty"`
}

func (s *Studio) makeDelegateHandler() tools.DelegateHandler {
	return func(ctx context.Context, action string, args map[string]any) (tools.ToolResult, error) {
		callerProjectID, callerSessionID := askUserRouting(ctx)
		if strings.TrimSpace(callerProjectID) == "" {
			return tools.NewErrorResult("delegation is unavailable: this turn has no addressable caller"), nil
		}
		parent := stampFromToolContext(tools.DelegationFromContext(ctx))

		switch action {
		case "list":
			return s.delegateList(callerProjectID, parent), nil
		case "ask", "run":
			return s.delegateStart(action, callerProjectID, callerSessionID, parent, args), nil
		case "batch":
			return s.delegateBatch(callerProjectID, callerSessionID, parent, args), nil
		case "status":
			return s.delegateStatus(callerProjectID, callerSessionID, args), nil
		case "fetch":
			return s.delegateFetch(callerProjectID, callerSessionID, args), nil
		case "cancel":
			return s.delegateCancel(callerProjectID, callerSessionID, args), nil
		default:
			return tools.NewErrorResult(fmt.Sprintf("unknown delegate action %q", action)), nil
		}
	}
}

func (s *Studio) delegateList(callerProjectID string, parent *delegationStamp) tools.ToolResult {
	targets := s.delegationTargets(callerProjectID, parent)
	if len(targets) == 0 {
		return tools.NewSuccessResultWithData(
			"No other projects are connected, so there is nobody to delegate to. Do the work here.",
			map[string]any{"targets": []DelegationTargetInfo{}},
		)
	}
	var b strings.Builder
	b.WriteString("Projects you can delegate to:\n")
	for _, target := range targets {
		fmt.Fprintf(&b, "\n- %s (project_id=%s)", target.Name, target.ProjectID)
		if target.Description != "" {
			fmt.Fprintf(&b, "\n  %s", target.Description)
		}
		if len(target.Capabilities) > 0 {
			fmt.Fprintf(&b, "\n  good for: %s", strings.Join(target.Capabilities, ", "))
		}
		if !target.Reachable {
			fmt.Fprintf(&b, "\n  unavailable: %s", target.Reason)
		} else if target.Busy {
			b.WriteString("\n  currently busy with another delegation")
		}
	}
	return tools.NewSuccessResultWithData(b.String(), map[string]any{"targets": targets})
}

// delegationTargets lists every other project with its legality already
// resolved, so the model never has to guess whether a call would be refused.
func (s *Studio) delegationTargets(callerProjectID string, parent *delegationStamp) []DelegationTargetInfo {
	s.mu.RLock()
	type snapshot struct {
		id, name, description, provider, model string
		capabilities                           []string
		policyErr                              error
		groupName                              string
	}
	snapshots := make([]snapshot, 0, len(s.projects))
	for id, project := range s.projects {
		if id == callerProjectID {
			continue
		}
		project.mu.RLock()
		entry := snapshot{
			id: id, name: project.Name, description: project.Description,
			provider: project.Provider, model: project.Model,
			capabilities: append([]string(nil), project.Capabilities...),
		}
		project.mu.RUnlock()
		// Legality is resolved here so the model never has to guess whether a
		// call would be refused, and never burns a turn finding out.
		group, err := s.delegationPolicyAllowsLocked(callerProjectID, project)
		entry.policyErr, entry.groupName = err, group.Name
		snapshots = append(snapshots, entry)
	}
	s.mu.RUnlock()

	busy := make(map[string]bool)
	s.delegationMu.Lock()
	for _, handle := range s.delegations {
		busy[handle.toProjectID] = true
	}
	s.delegationMu.Unlock()

	targets := make([]DelegationTargetInfo, 0, len(snapshots))
	for _, snap := range snapshots {
		info := DelegationTargetInfo{
			ProjectID: snap.id, Name: snap.name, Description: snap.description,
			Capabilities: snap.capabilities, Provider: snap.provider, Model: snap.model,
			Busy: busy[snap.id], Reachable: true,
		}
		info.Group = snap.groupName
		hop := delegationHop{Applies: true, TargetProject: snap.id, CrossProject: true}
		if _, refusal := delegationHopAllowed(parent, callerProjectID, hop); refusal != "" {
			info.Reachable, info.Reason = false, refusal
		} else if snap.policyErr != nil {
			info.Reachable, info.Reason = false, snap.policyErr.Error()
		} else if info.Busy {
			info.Reason = "already handling a delegation"
		}
		targets = append(targets, info)
	}
	return targets
}

func (s *Studio) delegateStart(kind, callerProjectID, callerSessionID string, parent *delegationStamp, args map[string]any) tools.ToolResult {
	targetID := strings.TrimSpace(stringArg(args, "project_id"))
	run, err := s.startDelegation(delegationRequest{
		FromProjectID: callerProjectID,
		FromSessionID: callerSessionID,
		ToProjectID:   targetID,
		Kind:          kind,
		Goal:          strings.TrimSpace(stringArg(args, "goal")),
		Task:          strings.TrimSpace(stringArg(args, "task")),
		Parent:        parent,
	})
	if err != nil {
		errType := DelegationErrorType(err)
		// Two-tier shape. A caller mistake is a tool ERROR carrying the valid
		// targets so the model can correct itself; a target-side failure is a
		// SUCCESS carrying error_type, because the call itself was well-formed.
		if errType == DelegationErrorUnknownTarget {
			return tools.NewErrorResultWithData(
				err.Error()+" — call action=\"list\" for the exact project IDs.",
				map[string]any{
					"error_type":        errType,
					"available_targets": s.delegationTargets(callerProjectID, parent),
				},
			)
		}
		if errType == "" {
			return tools.NewErrorResult(err.Error())
		}
		return tools.NewSuccessResultWithData(
			fmt.Sprintf("Delegation was refused (%s): %s", errType, err.Error()),
			map[string]any{"error_type": errType, "hint": delegationHintFor(errType)},
		)
	}
	return tools.NewSuccessResultWithData(
		fmt.Sprintf(
			"Started a %s delegation in project %s. It runs in the background; "+
				"poll it with delegate action=\"status\" run_id=%q.",
			run.Kind, run.ToProjectID, run.ID),
		map[string]any{
			"run_id": run.ID, "kind": run.Kind,
			"project_id": run.ToProjectID, "session_id": run.ToSessionID,
			"status": run.Status,
		},
	)
}

func delegationHintFor(errType string) string {
	switch errType {
	case DelegationErrorBusy:
		return "Wait and retry, or do the work here."
	case DelegationErrorBudget:
		return "The target project is out of budget; tell the user rather than retrying."
	case DelegationErrorDepthLimit, DelegationErrorCycle:
		return "Report back to whoever asked you instead of delegating further."
	case DelegationErrorPolicy:
		return "That project does not accept delegation from here."
	default:
		return "Report the failure rather than retrying blindly."
	}
}

// delegationRunForCaller is the capability boundary for model-facing run IDs.
// The Wails bindings intentionally remain administrative (the user can inspect
// every run in the panel), but an agent may address only work started by its
// exact project and chat. Return the same error for a foreign and a missing ID
// so the check does not become a cross-chat existence oracle.
func delegationRunForCaller(callerProjectID, callerSessionID, runID string) (DelegationRun, error) {
	runID = strings.TrimSpace(runID)
	run, ok, err := loadDelegationRun(runID)
	if err != nil {
		return DelegationRun{}, fmt.Errorf("read delegation run store: %w", err)
	}
	if callerSessionID == "" {
		callerSessionID = "default"
	}
	if !ok || run.FromProjectID != callerProjectID || run.FromSessionID != callerSessionID {
		return DelegationRun{}, fmt.Errorf("delegation run not found: %s", runID)
	}
	return run, nil
}

func (s *Studio) delegateStatus(callerProjectID, callerSessionID string, args map[string]any) tools.ToolResult {
	run, err := delegationRunForCaller(callerProjectID, callerSessionID, stringArg(args, "run_id"))
	if err != nil {
		return tools.NewErrorResult(err.Error())
	}
	summary := truncateUTF8(run.Answer, delegationSummaryMaxBytes)
	text := fmt.Sprintf("Delegation %s is %s.", run.ID, run.Status)
	if run.ErrorType != "" {
		text += fmt.Sprintf(" (%s: %s)", run.ErrorType, run.Error)
	}
	if len(run.DeniedTools) > 0 {
		text += fmt.Sprintf(" The target finished but %d tool call(s) were blocked, so the answer may be incomplete: %s.",
			len(run.DeniedTools), strings.Join(run.DeniedTools, ", "))
	}
	if summary != "" {
		text += "\n\n" + summary
	}
	return tools.NewSuccessResultWithData(text, map[string]any{
		"run_id": run.ID, "status": run.Status, "kind": run.Kind,
		"project_id": run.ToProjectID, "error_type": run.ErrorType,
		"answer_bytes": run.AnswerBytes, "truncated": run.Truncated,
		"denied_tools": run.DeniedTools, "cost_usd": run.EstimatedCostUSD,
		"progress": run.ProgressTail,
	})
}

func (s *Studio) delegateFetch(callerProjectID, callerSessionID string, args map[string]any) tools.ToolResult {
	runID := strings.TrimSpace(stringArg(args, "run_id"))
	if _, err := delegationRunForCaller(callerProjectID, callerSessionID, runID); err != nil {
		return tools.NewErrorResult(err.Error())
	}
	offset, _ := tools.GetInt(args, "offset")
	maxBytes, _ := tools.GetInt(args, "max_bytes")
	page, err := s.FetchDelegationAnswer(runID, offset, maxBytes)
	if err != nil {
		return tools.NewErrorResult(err.Error())
	}
	return tools.NewSuccessResultWithData(page.Text, map[string]any{
		"run_id": page.RunID, "offset": page.Offset,
		"full_size": page.FullSize, "truncated": page.Truncated,
		"next_offset": page.Offset + len(page.Text),
	})
}

func (s *Studio) delegateCancel(callerProjectID, callerSessionID string, args map[string]any) tools.ToolResult {
	runID := strings.TrimSpace(stringArg(args, "run_id"))
	if _, err := delegationRunForCaller(callerProjectID, callerSessionID, runID); err != nil {
		return tools.NewErrorResult(err.Error())
	}
	if _, _, cancelErr := s.cancelDelegationRun(runID, true); cancelErr != nil {
		if DelegationErrorType(cancelErr) == DelegationErrorStorage {
			mutated := delegationErrorMutatedBeforeStop(cancelErr)
			return tools.NewSuccessResultWithData(cancelErr.Error(), map[string]any{
				"run_id": runID, "status": "stopped", "error_type": DelegationErrorStorage,
				"mutated_before_stop": mutated,
			})
		}
		return tools.NewErrorResult(cancelErr.Error())
	}
	run, readErr := s.GetDelegationRun(runID)
	if readErr != nil {
		return tools.NewSuccessResultWithData(
			fmt.Sprintf("Delegation %s was cancelled, but its final durable state could not be read: %v", runID, readErr),
			map[string]any{
				"run_id": runID, "status": "stopped", "error_type": DelegationErrorStorage,
				"mutation_state": "unknown",
			},
		)
	}
	text := fmt.Sprintf("Delegation %s was cancelled.", runID)
	if run.MutatedBeforeStop {
		text += " It had already written changes in the target project; a cancelled delegation is not a rolled-back delegation."
	}
	return tools.NewSuccessResultWithData(text, map[string]any{
		"run_id": runID, "status": "stopped",
		"mutated_before_stop": run.MutatedBeforeStop,
	})
}

func (s *Studio) delegateBatch(callerProjectID, callerSessionID string, parent *delegationStamp, args map[string]any) tools.ToolResult {
	raw, _ := args["targets"].([]any)
	targets := make([]DelegationTarget, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return tools.NewErrorResult("each target must be an object with project_id and task")
		}
		targets = append(targets, DelegationTarget{
			ProjectID: strings.TrimSpace(stringArg(entry, "project_id")),
			Task:      strings.TrimSpace(stringArg(entry, "task")),
		})
	}
	// A fan-out started from a delegated turn would multiply the chain, so the
	// same depth and cycle rules apply per target inside StartDelegationBatch.
	if parent.depth() >= maxDelegationDepth {
		return tools.NewSuccessResultWithData(
			"Delegation was refused (depth_limit): this turn is already at the delegation depth limit.",
			map[string]any{"error_type": DelegationErrorDepthLimit, "hint": delegationHintFor(DelegationErrorDepthLimit)},
		)
	}
	result, err := s.startDelegationBatch(callerProjectID, callerSessionID,
		targets, strings.TrimSpace(stringArg(args, "goal")), strings.TrimSpace(stringArg(args, "task")), parent)
	if err != nil {
		errType := DelegationErrorType(err)
		if errType == "" {
			return tools.NewErrorResultWithData(err.Error(), map[string]any{
				"available_targets": s.delegationTargets(callerProjectID, parent),
			})
		}
		return tools.NewSuccessResultWithData(
			fmt.Sprintf("Fan-out was refused (%s): %s", errType, err.Error()),
			map[string]any{"error_type": errType, "hint": delegationHintFor(errType)},
		)
	}
	runIDs := make([]string, 0, len(result.Runs))
	for _, run := range result.Runs {
		runIDs = append(runIDs, run.ID)
	}
	text := fmt.Sprintf("Started %d delegations (batch %s). They run in the background.",
		len(result.Runs), result.BatchID)
	if result.AggregateQueued {
		text += " A synthesis turn is queued in this chat and will run once they all finish."
	}
	return tools.NewSuccessResultWithData(text, map[string]any{
		"batch_id": result.BatchID, "run_ids": runIDs,
		"aggregate_queued": result.AggregateQueued,
	})
}

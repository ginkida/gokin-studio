package studio

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// Durable record of cross-project delegations.
//
// This deliberately CLONES the shape of the scheduled-task run store rather
// than sharing an abstraction with it. The two have different lifecycle owners,
// different reconciliation rules and different retention policies; a shared
// store would have to satisfy both, and duplication is the cheaper risk.

const (
	maxDelegationRuns        = 200
	maxDelegationRunFile     = 4 << 20
	maxDelegationAnswerBytes = 16 << 10
	maxDelegationTailLines   = 20
	maxDelegationTailLine    = 300
	// DelegationGoalMaxBytes bounds the "why" a caller attaches to a delegation.
	DelegationGoalMaxBytes = tools.DelegateGoalMaxBytes
)

// Closed set of machine-readable failure reasons. Callers branch on these;
// the human message is separate and never parsed.
const (
	DelegationErrorUnknownTarget = "unknown_target"
	DelegationErrorPolicy        = "policy"
	DelegationErrorBusy          = "busy"
	DelegationErrorDepthLimit    = delegationErrorDepth
	DelegationErrorCycle         = delegationErrorCycle
	DelegationErrorBudget        = "budget"
	DelegationErrorTimeout       = "timeout"
	DelegationErrorCancelled     = "cancelled"
	DelegationErrorDenied        = "denied"
	DelegationErrorProvider      = "provider_error"
	DelegationErrorStorage       = "storage_error"
)

// DelegationRun is one cross-project delegation attempt. Each run owns a real
// child ChatSession in the target project, so its transcript, approvals, tool
// calls and usage stay inspectable after the run finishes.
type DelegationRun struct {
	ID      string `json:"id"`
	BatchID string `json:"batchID,omitempty"`
	ChainID string `json:"chainID,omitempty"`
	// Kind is "ask" (bounded question, no tools, auto-archived) or "run"
	// (real work in the target's own worktree with its own tools).
	Kind  string   `json:"kind"`
	Depth int      `json:"depth"`
	Chain []string `json:"chain,omitempty"`

	FromProjectID string `json:"fromProjectID"`
	FromSessionID string `json:"fromSessionID"`
	ToProjectID   string `json:"toProjectID"`
	ToSessionID   string `json:"toSessionID"`
	GroupID       string `json:"groupID,omitempty"`

	Goal string `json:"goal,omitempty"`
	Task string `json:"task"`

	Status       string   `json:"status"` // running | completed | stopped | error
	Answer       string   `json:"answer,omitempty"`
	AnswerBytes  int      `json:"answerBytes,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	ProgressTail []string `json:"progressTail,omitempty"`
	DeniedTools  []string `json:"deniedTools,omitempty"`
	// MutatedBeforeStop records that the target had already written something
	// when the run was cancelled. A cancelled delegation is not a rolled-back
	// delegation, and the UI must be able to say so.
	MutatedBeforeStop bool `json:"mutatedBeforeStop,omitempty"`

	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`

	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	InputTokens      int     `json:"inputTokens,omitempty"`
	OutputTokens     int     `json:"outputTokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimatedCostUSD,omitempty"`

	// LegacyDispatch marks runs started through the old Dispatch binding so the
	// monitor also emits the dispatch:complete payload the current frontend
	// still listens for.
	LegacyDispatch bool `json:"legacyDispatch,omitempty"`

	StartedAt   int64 `json:"startedAt"`
	CompletedAt int64 `json:"completedAt,omitempty"`
}

// DelegationAnswerPage is a bounded window into a stored answer, so a large
// result never has to cross the bridge (or enter a model's context) whole.
type DelegationAnswerPage struct {
	RunID     string `json:"runID"`
	Offset    int    `json:"offset"`
	Text      string `json:"text"`
	FullSize  int    `json:"fullSize"`
	Truncated bool   `json:"truncated"`
}

func delegationRunTerminal(status string) bool {
	switch status {
	case "completed", "stopped", "error":
		return true
	}
	return false
}

// Lock order: delegationMu may acquire delegationRunsMu to atomically publish
// a batch reservation, never the reverse. No helper in this file may reach for
// Studio.delegationMu while holding this process-wide store lock.
var delegationRunsMu sync.Mutex

func delegationRunsPath() string {
	return filepath.Join(configDir(), "delegation_runs.json")
}

func loadDelegationRunsRaw() ([]DelegationRun, error) {
	f, err := os.Open(delegationRunsPath())
	if os.IsNotExist(err) {
		return []DelegationRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDelegationRunFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDelegationRunFile {
		return nil, fmt.Errorf("delegation run file exceeds the %d MiB limit", maxDelegationRunFile>>20)
	}
	if len(data) == 0 {
		return []DelegationRun{}, nil
	}
	var runs []DelegationRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("parse delegation runs: %w", err)
	}
	return runs, nil
}

func saveDelegationRunsRaw(runs []DelegationRun) ([]DelegationRun, error) {
	_, evicted, data, err := fitDelegationRuns(runs)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return nil, err
	}
	if err := atomicWriteFile(delegationRunsPath(), append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return evicted, nil
}

// fitDelegationRuns applies both retention limits without ever deleting a live
// row. Every removed row is returned so Studio can reap the child session and
// worktree that the durable record owns. Older save logic trimmed inside the
// serializer and discarded that information, leaking those children forever.
func fitDelegationRuns(runs []DelegationRun) ([]DelegationRun, []DelegationRun, []byte, error) {
	kept := append([]DelegationRun(nil), runs...)
	// Persistence is newest-first. Enforce the order here rather than relying
	// on every mutation caller (or an older on-disk version) to preserve it.
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].StartedAt > kept[j].StartedAt })
	evicted := make([]DelegationRun, 0)
	dropOldestTerminal := func() bool {
		for i := len(kept) - 1; i >= 0; i-- {
			if !delegationRunTerminal(kept[i].Status) {
				continue
			}
			evicted = append(evicted, kept[i])
			kept = append(kept[:i], kept[i+1:]...)
			return true
		}
		return false
	}

	for len(kept) > maxDelegationRuns {
		if !dropOldestTerminal() {
			return nil, nil, nil, fmt.Errorf(
				"delegation store has %d live rows, exceeding the %d-row limit",
				len(kept), maxDelegationRuns)
		}
	}
	for {
		data, err := json.MarshalIndent(kept, "", "  ")
		if err != nil {
			return nil, nil, nil, err
		}
		// The newline is part of the on-disk file and therefore part of the
		// reader's hard cap too.
		if len(data)+1 <= maxDelegationRunFile {
			return kept, evicted, data, nil
		}
		if !dropOldestTerminal() {
			return nil, nil, nil, fmt.Errorf(
				"live delegation rows exceed the %d MiB storage limit",
				maxDelegationRunFile>>20)
		}
	}
}

// appendDelegationRun stores a new run and returns the rows retention dropped.
// Like the scheduled-task store, an evicted row is the only link to the child
// session it created, so the caller must reap those sessions.
func appendDelegationRun(run DelegationRun) ([]DelegationRun, error) {
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return nil, err
	}
	for _, existing := range runs {
		if existing.ID == run.ID {
			return nil, fmt.Errorf("delegation run ID already exists: %s", run.ID)
		}
		if !delegationRunTerminal(existing.Status) && existing.ToProjectID != "" &&
			existing.ToProjectID == run.ToProjectID {
			return nil, fmt.Errorf("target project %s already has live delegation run %s",
				run.ToProjectID, existing.ID)
		}
	}
	runs = append(runs, run)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
	evicted, err := saveDelegationRunsRaw(runs)
	if err != nil {
		return nil, err
	}
	return evicted, nil
}

// finishDelegationRun moves a run to a terminal state. It REFUSES a row that is
// already terminal, which is what makes "mark terminal, then cancel" safe: a
// completion racing a user's cancel cannot overwrite the cancel.
func finishDelegationRun(id, status, errorType string, runErr error, patch func(*DelegationRun)) (DelegationRun, bool, []DelegationRun, error) {
	if !delegationRunTerminal(status) {
		return DelegationRun{}, false, nil, fmt.Errorf("invalid terminal delegation status %q", status)
	}
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return DelegationRun{}, false, nil, err
	}
	for i := range runs {
		if runs[i].ID != id {
			continue
		}
		if delegationRunTerminal(runs[i].Status) {
			return runs[i], false, nil, nil
		}
		runs[i].Status = status
		runs[i].CompletedAt = time.Now().UnixMilli()
		if errorType != "" {
			runs[i].ErrorType = errorType
		}
		if runErr != nil {
			runs[i].Error = truncateUTF8(runErr.Error(), 2000)
		}
		if patch != nil {
			patch(&runs[i])
		}
		updated := runs[i]
		evicted, err := saveDelegationRunsRaw(runs)
		if err != nil {
			// Return the attempted terminal snapshot even when persistence fails.
			// Live callers need its exact identity to stop the child and surface a
			// storage_error without inventing or guessing project/session fields.
			return updated, false, nil, err
		}
		return updated, true, evicted, nil
	}
	return DelegationRun{}, false, nil, fmt.Errorf("delegation run not found: %s", id)
}

// updateDelegationRunProgress refreshes the live fields of a running row.
// Terminal rows are left alone.
func updateDelegationRunProgress(id string, patch func(*DelegationRun)) (bool, []DelegationRun, error) {
	if patch == nil {
		return false, nil, nil
	}
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return false, nil, err
	}
	for i := range runs {
		if runs[i].ID != id || delegationRunTerminal(runs[i].Status) {
			continue
		}
		patch(&runs[i])
		evicted, err := saveDelegationRunsRaw(runs)
		return err == nil, evicted, err
	}
	return false, nil, nil
}

// loadDelegationRun distinguishes an absent row from an unreadable store.
// Collapsing both to found=false is unsafe for live callers: monitors would
// treat corruption as cancellation and abandon a still-running paid turn.
func loadDelegationRun(id string) (DelegationRun, bool, error) {
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return DelegationRun{}, false, err
	}
	for _, run := range runs {
		if run.ID == id {
			return run, true, nil
		}
	}
	return DelegationRun{}, false, nil
}

// reconcileInterruptedDelegationRuns flips rows left "running" by a previous
// process. Without it a crashed run is polled forever and never collectable.
func reconcileInterruptedDelegationRuns() (int, []DelegationRun, error) {
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return 0, nil, err
	}
	changed := 0
	now := time.Now().UnixMilli()
	for i := range runs {
		if delegationRunTerminal(runs[i].Status) {
			continue
		}
		runs[i].Status = "stopped"
		runs[i].ErrorType = DelegationErrorCancelled
		runs[i].Error = "Gokin Studio closed before this delegation finished"
		runs[i].CompletedAt = now
		changed++
	}
	if changed == 0 {
		return 0, nil, nil
	}
	evicted, err := saveDelegationRunsRaw(runs)
	return changed, evicted, err
}

// listDelegationRunsOlderThan returns terminal rows past the cutoff without
// mutating the store. Cleanup uses this first because the child chat is the
// valuable payload: its durable row must stay in place until that chat has
// either been deleted safely or is already gone.
func listDelegationRunsOlderThan(cutoff int64) ([]DelegationRun, error) {
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return nil, err
	}
	old := make([]DelegationRun, 0)
	for _, run := range runs {
		if delegationRunTerminal(run.Status) && run.CompletedAt > 0 && run.CompletedAt < cutoff {
			old = append(old, run)
		}
	}
	return old, nil
}

func splitDelegationRunsByID(runs []DelegationRun, ids map[string]struct{}) (kept, removed []DelegationRun) {
	kept = make([]DelegationRun, 0, len(runs))
	removed = make([]DelegationRun, 0, len(ids))
	for _, run := range runs {
		if _, ok := ids[run.ID]; ok {
			removed = append(removed, run)
			continue
		}
		kept = append(kept, run)
	}
	return kept, removed
}

// estimateDelegationRunRemoval reports the current rows and durable-store bytes
// that removing ids would save. It uses the same pretty-printed representation
// as saveDelegationRunsRaw, so cleanup previews don't claim zero bytes for the
// records they include.
func estimateDelegationRunRemoval(ids map[string]struct{}) (int, int64, error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return 0, 0, err
	}
	kept, removed := splitDelegationRunsByID(runs, ids)
	if len(removed) == 0 {
		return 0, 0, nil
	}
	before, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	_, _, after, err := fitDelegationRuns(kept)
	if err != nil {
		return 0, 0, err
	}
	freed := int64(len(before) - len(after))
	if freed < 0 {
		freed = 0
	}
	return len(removed), freed, nil
}

// removeDelegationRunsByID commits an already-vetted cleanup plan. Callers
// delete (or prove the absence of) each child chat first; if a chat is protected
// or deletion fails, its ID is deliberately omitted and its durable link stays.
func removeDelegationRunsByID(ids map[string]struct{}) ([]DelegationRun, []DelegationRun, int64, error) {
	if len(ids) == 0 {
		return nil, nil, 0, nil
	}
	delegationRunsMu.Lock()
	defer delegationRunsMu.Unlock()
	runs, err := loadDelegationRunsRaw()
	if err != nil {
		return nil, nil, 0, err
	}
	kept, removed := splitDelegationRunsByID(runs, ids)
	if len(removed) == 0 {
		return nil, nil, 0, nil
	}
	before, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return nil, nil, 0, err
	}
	_, _, after, err := fitDelegationRuns(kept)
	if err != nil {
		return nil, nil, 0, err
	}
	evicted, err := saveDelegationRunsRaw(kept)
	if err != nil {
		return nil, nil, 0, err
	}
	freed := int64(len(before) - len(after))
	if freed < 0 {
		freed = 0
	}
	return removed, evicted, freed, nil
}

// boundDelegationTail keeps a progress tail small enough to persist, render and
// carry in an event. The content is another agent's output, so it is redacted
// at the same chokepoint the event log uses.
func boundDelegationTail(lines []string) []string {
	if len(lines) > maxDelegationTailLines {
		lines = lines[len(lines)-maxDelegationTailLines:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = sanitizeLogMessage(truncateUTF8(line, maxDelegationTailLine))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

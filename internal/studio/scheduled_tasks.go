package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

const (
	maxScheduledTasks       = 64
	maxScheduledTaskName    = 120
	maxScheduledTaskError   = 2000
	maxScheduledTaskFile    = 8 << 20
	maxScheduledTaskRuns    = 512
	maxRunsPerScheduledTask = 50
	maxScheduledRunFile     = 8 << 20
	minScheduleIntervalMins = 15
	maxScheduleIntervalMins = 7 * 24 * 60
)

// ScheduledTask is a locally persisted recurring prompt. Times are Unix
// milliseconds; TimeOfDay and Weekday are interpreted in the machine's local
// timezone so "09:00" behaves like a desktop reminder after timezone changes.
type ScheduledTask struct {
	ID              string `json:"id"`
	ProjectID       string `json:"projectID"`
	SessionID       string `json:"sessionID"`
	Name            string `json:"name"`
	Prompt          string `json:"prompt"`
	Schedule        string `json:"schedule"` // interval | daily | weekdays | weekly | manual
	IntervalMinutes int    `json:"intervalMinutes,omitempty"`
	TimeOfDay       string `json:"timeOfDay,omitempty"` // HH:MM
	Weekday         int    `json:"weekday,omitempty"`   // 0=Sunday
	Enabled         bool   `json:"enabled"`
	CreatedAt       int64  `json:"createdAt"`
	NextRunAt       int64  `json:"nextRunAt,omitempty"`
	LastRunAt       int64  `json:"lastRunAt,omitempty"`
	LastRunID       string `json:"lastRunID,omitempty"`
	LastStatus      string `json:"lastStatus,omitempty"` // dispatching | running | completed | stopped | error
	LastError       string `json:"lastError,omitempty"`
	Provider        string `json:"provider,omitempty"`     // glm | kimi
	Model           string `json:"model,omitempty"`        // provider catalog model
	ApprovalMode    string `json:"approvalMode,omitempty"` // manual | accept_edits | auto | skip
}

// ScheduledTaskRun is one immutable execution attempt. Each run owns a
// separate chat session so its full transcript, tools, approvals, and usage
// remain inspectable after later runs.
type ScheduledTaskRun struct {
	ID           string `json:"id"`
	TaskID       string `json:"taskID"`
	ProjectID    string `json:"projectID"`
	SessionID    string `json:"sessionID"`
	StartedAt    int64  `json:"startedAt"`
	CompletedAt  int64  `json:"completedAt,omitempty"`
	Status       string `json:"status"` // running | completed | stopped | error
	Error        string `json:"error,omitempty"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	ApprovalMode string `json:"approvalMode"`
}

func normalizeScheduledApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask":
		return "manual"
	case "acceptedits", "accept_edits", "accept-edits":
		return "accept_edits"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// ScheduledTaskDeletionPreview describes the local data affected by deleting
// a task. RunChatCount includes only sessions that are authoritatively linked
// to this task and are not its source chat. ProtectedRunChats covers malformed
// or stale run records that are deliberately never treated as deletion proof.
type ScheduledTaskDeletionPreview struct {
	ProjectID         string `json:"projectID"`
	TaskID            string `json:"taskID"`
	RunCount          int    `json:"runCount"`
	RunChatCount      int    `json:"runChatCount"`
	ActiveRunCount    int    `json:"activeRunCount"`
	MissingRunChats   int    `json:"missingRunChats"`
	ProtectedRunChats int    `json:"protectedRunChats"`
}

var scheduledTasksMu sync.Mutex

func scheduledTasksPath() string {
	return filepath.Join(configDir(), "scheduled_tasks.json")
}

func scheduledTaskRunsPath() string {
	return filepath.Join(configDir(), "scheduled_task_runs.json")
}

func loadScheduledTaskRunsRaw() ([]ScheduledTaskRun, error) {
	f, err := os.Open(scheduledTaskRunsPath())
	if os.IsNotExist(err) {
		return []ScheduledTaskRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScheduledRunFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScheduledRunFile {
		return nil, fmt.Errorf("scheduled task run file exceeds the 8 MiB limit")
	}
	var runs []ScheduledTaskRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("parse scheduled task runs: %w", err)
	}
	if len(runs) > maxScheduledTaskRuns {
		return nil, fmt.Errorf("scheduled task run file exceeds the %d-run limit", maxScheduledTaskRuns)
	}
	return runs, nil
}

func saveScheduledTaskRunsRaw(runs []ScheduledTaskRun) error {
	if len(runs) > maxScheduledTaskRuns {
		return fmt.Errorf("at most %d scheduled task runs are retained", maxScheduledTaskRuns)
	}
	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxScheduledRunFile {
		return fmt.Errorf("scheduled task runs exceed the 8 MiB storage limit")
	}
	return atomicWriteFile(scheduledTaskRunsPath(), append(data, '\n'), 0o600)
}

func loadScheduledTasksRaw() ([]ScheduledTask, error) {
	f, err := os.Open(scheduledTasksPath())
	if os.IsNotExist(err) {
		return []ScheduledTask{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScheduledTaskFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxScheduledTaskFile {
		return nil, fmt.Errorf("scheduled task file exceeds the 8 MiB limit")
	}
	var tasks []ScheduledTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parse scheduled tasks: %w", err)
	}
	if len(tasks) > maxScheduledTasks {
		return nil, fmt.Errorf("scheduled task file exceeds the %d-task limit", maxScheduledTasks)
	}
	return tasks, nil
}

func saveScheduledTasksRaw(tasks []ScheduledTask) error {
	if len(tasks) > maxScheduledTasks {
		return fmt.Errorf("at most %d scheduled tasks are allowed", maxScheduledTasks)
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxScheduledTaskFile {
		return fmt.Errorf("scheduled tasks exceed the 8 MiB storage limit")
	}
	data = append(data, '\n')
	return atomicWriteFile(scheduledTasksPath(), data, 0o600)
}

func validateScheduledTask(task ScheduledTask, now time.Time) (ScheduledTask, error) {
	task.ID = strings.TrimSpace(task.ID)
	task.ProjectID = strings.TrimSpace(task.ProjectID)
	task.SessionID = strings.TrimSpace(task.SessionID)
	task.Name = strings.TrimSpace(task.Name)
	task.Prompt = strings.TrimSpace(task.Prompt)
	task.Schedule = strings.ToLower(strings.TrimSpace(task.Schedule))
	task.TimeOfDay = strings.TrimSpace(task.TimeOfDay)
	task.Provider = strings.ToLower(strings.TrimSpace(task.Provider))
	task.Model = strings.TrimSpace(task.Model)
	task.ApprovalMode = normalizeScheduledApprovalMode(task.ApprovalMode)
	if len(task.ID) > 128 || strings.ContainsRune(task.ID, 0) {
		return task, fmt.Errorf("invalid scheduled task ID")
	}
	if task.ProjectID == "" {
		return task, fmt.Errorf("project is required")
	}
	if task.SessionID == "" {
		task.SessionID = "default"
	}
	if task.Name == "" {
		task.Name = scheduledTaskTitle(task.Prompt, 60)
	}
	if task.Name == "" || len([]rune(task.Name)) > maxScheduledTaskName {
		return task, fmt.Errorf("task name must contain 1-%d characters", maxScheduledTaskName)
	}
	if err := validateRPCText("prompt", task.Prompt, ChatMessageMaxBytes, true); err != nil {
		return task, err
	}
	switch task.Schedule {
	case "interval":
		if task.IntervalMinutes < minScheduleIntervalMins || task.IntervalMinutes > maxScheduleIntervalMins {
			return task, fmt.Errorf("interval must be between %d and %d minutes", minScheduleIntervalMins, maxScheduleIntervalMins)
		}
		task.TimeOfDay = ""
		task.Weekday = 0
	case "daily":
		if _, _, err := parseLocalTime(task.TimeOfDay); err != nil {
			return task, err
		}
		task.IntervalMinutes = 0
		task.Weekday = 0
	case "weekdays":
		if _, _, err := parseLocalTime(task.TimeOfDay); err != nil {
			return task, err
		}
		task.IntervalMinutes = 0
		task.Weekday = 0
	case "weekly":
		if _, _, err := parseLocalTime(task.TimeOfDay); err != nil {
			return task, err
		}
		if task.Weekday < 0 || task.Weekday > 6 {
			return task, fmt.Errorf("weekday must be between 0 and 6")
		}
		task.IntervalMinutes = 0
	case "manual":
		task.IntervalMinutes = 0
		task.TimeOfDay = ""
		task.Weekday = 0
	default:
		return task, fmt.Errorf("schedule must be interval, daily, weekdays, weekly, or manual")
	}
	if (task.Provider == "") != (task.Model == "") {
		return task, fmt.Errorf("provider and model must be selected together")
	}
	if task.Provider != "" {
		if err := validateStudioProviderModelRuntime(task.Provider, task.Model); err != nil {
			return task, err
		}
	}
	if task.ApprovalMode == "" {
		task.ApprovalMode = "manual"
	}
	if task.ApprovalMode != "manual" && task.ApprovalMode != "accept_edits" && task.ApprovalMode != "auto" && task.ApprovalMode != "skip" {
		return task, fmt.Errorf("approval mode must be manual, accept_edits, auto, or skip")
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.CreatedAt == 0 {
		task.CreatedAt = now.UnixMilli()
	}
	if task.Schedule == "manual" {
		task.NextRunAt = 0
	} else {
		task.NextRunAt = nextScheduledRun(task, now).UnixMilli()
	}
	return task, nil
}

func scheduledTaskTitle(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		value = value[:i]
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}

func parseLocalTime(value string) (hour, minute int, err error) {
	parsed, parseErr := time.Parse("15:04", value)
	if parseErr != nil {
		return 0, 0, fmt.Errorf("time must use HH:MM (24-hour local time)")
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func nextScheduledRun(task ScheduledTask, after time.Time) time.Time {
	switch task.Schedule {
	case "interval":
		return after.Add(time.Duration(task.IntervalMinutes) * time.Minute)
	case "daily", "weekdays", "weekly":
		hour, minute, _ := parseLocalTime(task.TimeOfDay)
		local := after.In(time.Local)
		candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, time.Local)
		if task.Schedule == "daily" {
			if !candidate.After(local) {
				candidate = candidate.AddDate(0, 0, 1)
			}
			return candidate
		}
		if task.Schedule == "weekdays" {
			if !candidate.After(local) {
				nextDay := local.AddDate(0, 0, 1)
				candidate = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), hour, minute, 0, 0, time.Local)
			}
			for candidate.Weekday() == time.Saturday || candidate.Weekday() == time.Sunday {
				nextDay := candidate.AddDate(0, 0, 1)
				candidate = time.Date(nextDay.Year(), nextDay.Month(), nextDay.Day(), hour, minute, 0, 0, time.Local)
			}
			return candidate
		}
		days := (task.Weekday - int(local.Weekday()) + 7) % 7
		candidate = candidate.AddDate(0, 0, days)
		if !candidate.After(local) {
			candidate = candidate.AddDate(0, 0, 7)
		}
		return candidate
	case "manual":
		return time.Time{}
	default:
		return after.Add(24 * time.Hour)
	}
}

// ListScheduledTasks returns project-scoped tasks in next-run order.
func (s *Studio) ListScheduledTasks(projectID string) ([]ScheduledTask, error) {
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	scheduledTasksMu.Unlock()
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	project := s.projects[projectID]
	var projectProvider, projectModel string
	if project != nil {
		project.mu.RLock()
		projectProvider, projectModel = project.Provider, project.Model
		project.mu.RUnlock()
	}
	s.mu.RUnlock()
	out := make([]ScheduledTask, 0, len(tasks))
	for _, task := range tasks {
		if projectID == "" || task.ProjectID == projectID {
			if task.Provider == "" && task.Model == "" && project != nil {
				task.Provider, task.Model = projectProvider, projectModel
			}
			task.ApprovalMode = normalizeScheduledApprovalMode(task.ApprovalMode)
			if task.ApprovalMode == "" {
				task.ApprovalMode = "manual"
			}
			out = append(out, task)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		return out[i].NextRunAt < out[j].NextRunAt
	})
	return out, nil
}

// ListScheduledTaskRuns returns newest-first bounded run history for one task.
func (s *Studio) ListScheduledTaskRuns(projectID, taskID string) ([]ScheduledTaskRun, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledTaskRun, 0, len(runs))
	for _, run := range runs {
		if run.ProjectID == projectID && (taskID == "" || run.TaskID == taskID) {
			out = append(out, run)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

// SaveScheduledTask creates or replaces a recurring prompt.
func (s *Studio) SaveScheduledTask(input ScheduledTask) (ScheduledTask, error) {
	now := time.Now()
	task, err := validateScheduledTask(input, now)
	if err != nil {
		return ScheduledTask{}, err
	}
	s.mu.RLock()
	project := s.projects[task.ProjectID]
	s.mu.RUnlock()
	if project == nil {
		return ScheduledTask{}, fmt.Errorf("project not found: %s", task.ProjectID)
	}
	project.mu.RLock()
	projectProvider, projectModel := project.Provider, project.Model
	project.mu.RUnlock()
	if task.Provider == "" {
		task.Provider = projectProvider
		task.Model = projectModel
	}
	if err := s.validateAvailableStudioProviderModel(task.Provider, task.Model); err != nil {
		return ScheduledTask{}, err
	}
	session := project.GetSession(task.SessionID)
	if session == nil || session.ID != task.SessionID {
		return ScheduledTask{}, fmt.Errorf("session not found: %s", task.SessionID)
	}

	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		scheduledTasksMu.Unlock()
		return ScheduledTask{}, err
	}
	found := false
	for i := range tasks {
		if tasks[i].ID != task.ID {
			continue
		}
		if tasks[i].ProjectID != task.ProjectID {
			scheduledTasksMu.Unlock()
			return ScheduledTask{}, fmt.Errorf("scheduled task belongs to another project")
		}
		// Preserve run history across edits.
		task.CreatedAt = tasks[i].CreatedAt
		task.LastRunAt = tasks[i].LastRunAt
		task.LastRunID = tasks[i].LastRunID
		task.LastStatus = tasks[i].LastStatus
		task.LastError = tasks[i].LastError
		tasks[i] = task
		found = true
		break
	}
	if !found {
		if len(tasks) >= maxScheduledTasks {
			scheduledTasksMu.Unlock()
			return ScheduledTask{}, fmt.Errorf("at most %d scheduled tasks are allowed", maxScheduledTasks)
		}
		// Run summaries are scheduler-owned output, never client input. A new
		// Wails/tool caller must not be able to fabricate a live owner, terminal
		// history, or error banner that has no corresponding durable run row.
		task.LastRunAt = 0
		task.LastRunID = ""
		task.LastStatus = ""
		task.LastError = ""
		tasks = append(tasks, task)
	}
	err = saveScheduledTasksRaw(tasks)
	scheduledTasksMu.Unlock()
	if err != nil {
		return ScheduledTask{}, err
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		s.LogEvent("warn", "wake", fmt.Sprintf("refresh scheduled wake state: %v", err))
	}
	s.wakeScheduledTasks()
	return task, nil
}

// DeleteScheduledTask removes a task while preserving its run chats. It is
// kept as the safe default for tool calls and older frontends.
func (s *Studio) DeleteScheduledTask(projectID, taskID string) error {
	return s.DeleteScheduledTaskWithData(projectID, taskID, false)
}

// GetScheduledTaskDeletionPreview returns counts for the native deletion
// sheet without exposing prompts, transcript content, or filesystem paths.
func (s *Studio) GetScheduledTaskDeletionPreview(projectID, taskID string) (ScheduledTaskDeletionPreview, error) {
	task, runs, err := scheduledTaskDeletionSnapshot(projectID, taskID)
	if err != nil {
		return ScheduledTaskDeletionPreview{}, err
	}
	preview := ScheduledTaskDeletionPreview{
		ProjectID: projectID,
		TaskID:    taskID,
		RunCount:  len(runs),
	}
	sessions, missing, protected, active := s.scheduledTaskRunSessions(task, runs)
	preview.RunChatCount = len(sessions)
	preview.MissingRunChats = missing
	preview.ProtectedRunChats = protected
	preview.ActiveRunCount = active
	return preview, nil
}

// DeleteScheduledTaskWithData always stops active scheduled runs. When
// deleteRunData is true, it also deletes every authoritatively linked run chat
// through the same guarded cleanup path as a user-initiated chat deletion.
// The source chat is never eligible, even if a persisted run row is corrupt.
func (s *Studio) DeleteScheduledTaskWithData(projectID, taskID string, deleteRunData bool) error {
	var sessionIDs []string
	var expectedRunIDs map[string]bool
	if deleteRunData {
		for attempt := 0; attempt < 3; attempt++ {
			task, runs, err := scheduledTaskDeletionSnapshot(projectID, taskID)
			if err != nil {
				return err
			}
			sessions, _, _, _ := s.scheduledTaskRunSessions(task, runs)
			sessionIDs = sortedScheduledSessionIDs(sessions)
			if err := s.preflightScheduledRunSessionDeletion(projectID, sessionIDs); err != nil {
				return err
			}
			expectedRunIDs = scheduledRunIDSet(runs)
			removedRuns, changed, err := removeScheduledTaskRecords(projectID, taskID, expectedRunIDs)
			if err != nil {
				return err
			}
			if changed {
				// A run was created between preview and commit. Re-run the
				// complete preflight so that new chat cannot be orphaned.
				continue
			}
			return s.finishScheduledTaskDeletion(projectID, task, removedRuns, sessionIDs, true)
		}
		return fmt.Errorf("scheduled task changed while it was being deleted; try again")
	}

	task, _, err := scheduledTaskDeletionSnapshot(projectID, taskID)
	if err != nil {
		return err
	}
	removedRuns, _, err := removeScheduledTaskRecords(projectID, taskID, nil)
	if err != nil {
		return err
	}
	return s.finishScheduledTaskDeletion(projectID, task, removedRuns, nil, false)
}

func scheduledTaskDeletionSnapshot(projectID, taskID string) (ScheduledTask, []ScheduledTaskRun, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return ScheduledTask{}, nil, err
	}
	var selected ScheduledTask
	for _, task := range tasks {
		if task.ID == taskID && task.ProjectID == projectID {
			selected = task
			break
		}
	}
	if selected.ID == "" {
		return ScheduledTask{}, nil, fmt.Errorf("scheduled task not found")
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return ScheduledTask{}, nil, err
	}
	selectedRuns := make([]ScheduledTaskRun, 0)
	for _, run := range runs {
		if run.ProjectID == projectID && run.TaskID == taskID {
			selectedRuns = append(selectedRuns, run)
		}
	}
	return selected, selectedRuns, nil
}

func scheduledRunIDSet(runs []ScheduledTaskRun) map[string]bool {
	ids := make(map[string]bool, len(runs))
	for _, run := range runs {
		ids[run.ID] = true
	}
	return ids
}

// removeScheduledTaskRecords commits both scheduler files under one lock. Run
// rows are written first; if the task write fails, the original rows are
// restored so a retry still knows which chats belong to the task. changed is
// true only when an expected run-ID set no longer matches.
func removeScheduledTaskRecords(projectID, taskID string, expectedRunIDs map[string]bool) ([]ScheduledTaskRun, bool, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return nil, false, err
	}
	taskOut := make([]ScheduledTask, 0, len(tasks))
	found := false
	for _, task := range tasks {
		if task.ID == taskID && task.ProjectID == projectID {
			found = true
			continue
		}
		taskOut = append(taskOut, task)
	}
	if !found {
		return nil, false, fmt.Errorf("scheduled task not found")
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return nil, false, err
	}
	removed := make([]ScheduledTaskRun, 0)
	runOut := make([]ScheduledTaskRun, 0, len(runs))
	for _, run := range runs {
		if run.ProjectID == projectID && run.TaskID == taskID {
			removed = append(removed, run)
			continue
		}
		runOut = append(runOut, run)
	}
	if expectedRunIDs != nil {
		currentIDs := scheduledRunIDSet(removed)
		if len(currentIDs) != len(expectedRunIDs) {
			return nil, true, nil
		}
		for id := range expectedRunIDs {
			if !currentIDs[id] {
				return nil, true, nil
			}
		}
	}
	if len(removed) > 0 {
		if err := saveScheduledTaskRunsRaw(runOut); err != nil {
			return nil, false, err
		}
	}
	if err := saveScheduledTasksRaw(taskOut); err != nil {
		if len(removed) > 0 {
			if restoreErr := saveScheduledTaskRunsRaw(runs); restoreErr != nil {
				return nil, false, fmt.Errorf("delete scheduled task: %v; restoring run index also failed: %w", err, restoreErr)
			}
		}
		return nil, false, err
	}
	return removed, false, nil
}

func (s *Studio) scheduledTaskRunSessions(task ScheduledTask, runs []ScheduledTaskRun) (map[string]*ChatSession, int, int, int) {
	owned := make(map[string]*ChatSession)
	missing := 0
	protected := 0
	activeIDs := make(map[string]bool)
	s.mu.RLock()
	project := s.projects[task.ProjectID]
	s.mu.RUnlock()
	for _, run := range runs {
		if run.SessionID == "" || run.SessionID == task.SessionID {
			protected++
			continue
		}
		if project == nil {
			missing++
			continue
		}
		session := project.GetSession(run.SessionID)
		// GetSession intentionally falls back to the default chat for several
		// legacy call sites. That convenience must never become deletion proof.
		if session == nil || session.ID != run.SessionID {
			missing++
			continue
		}
		session.mu.RLock()
		parentID := session.ParentID
		session.mu.RUnlock()
		if parentID != task.SessionID {
			protected++
			continue
		}
		owned[run.SessionID] = session
		if !scheduledTaskRunTerminal(run.Status) {
			activeIDs[run.SessionID] = true
		}
	}
	return owned, missing, protected, len(activeIDs)
}

func sortedScheduledSessionIDs(sessions map[string]*ChatSession) []string {
	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Studio) preflightScheduledRunSessionDeletion(projectID string, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()
	project.mu.RLock()
	activeCount := 0
	deleteActiveCount := 0
	type deletionCandidate struct {
		session *ChatSession
		name    string
	}
	sessions := make([]deletionCandidate, 0, len(sessionIDs))
	deleteIDs := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		deleteIDs[id] = true
	}
	for id, session := range project.sessions {
		session.mu.RLock()
		archived := session.ArchivedAt > 0
		session.mu.RUnlock()
		if !archived {
			activeCount++
			if deleteIDs[id] {
				deleteActiveCount++
			}
		}
	}
	for _, id := range sessionIDs {
		if session := project.sessions[id]; session != nil {
			session.mu.RLock()
			name := session.Name
			session.mu.RUnlock()
			sessions = append(sessions, deletionCandidate{session: session, name: name})
		}
	}
	project.mu.RUnlock()
	if activeCount-deleteActiveCount < 1 {
		return fmt.Errorf("cannot delete run data because it would remove the last remaining chat")
	}
	for _, candidate := range sessions {
		status := sessionWorktreeStatus(candidate.session)
		if status.Error != "" {
			return fmt.Errorf("cannot delete run chat %q: its isolated worktree is unavailable: %s", candidate.name, status.Error)
		}
		if status.Dirty {
			return fmt.Errorf("cannot delete run chat %q: its worktree has %d uncommitted file change(s)", candidate.name, status.ChangedFiles)
		}
	}
	return nil
}

func (s *Studio) finishScheduledTaskDeletion(projectID string, task ScheduledTask, runs []ScheduledTaskRun, sessionIDs []string, deleteRunData bool) error {
	owned, _, _, _ := s.scheduledTaskRunSessions(task, runs)
	if deleteRunData {
		deleted := 0
		for _, sessionID := range sessionIDs {
			if _, ok := owned[sessionID]; !ok {
				continue
			}
			if err := s.DeleteChatSession(projectID, sessionID); err != nil {
				return fmt.Errorf("scheduled task was deleted, but only %d of %d run chat(s) were removed: %w", deleted, len(sessionIDs), err)
			}
			deleted++
		}
	} else {
		for _, run := range runs {
			if scheduledTaskRunTerminal(run.Status) {
				continue
			}
			if session := owned[run.SessionID]; session != nil {
				session.Stop()
			}
		}
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		s.LogEvent("warn", "wake", fmt.Sprintf("refresh scheduled wake state: %v", err))
	}
	s.wakeScheduledTasks()
	return nil
}

// removeScheduledTasksFor removes source-owned task and run rows as one
// recoverable transaction and returns the removed rows so the caller can stop
// their exact live child sessions before forgetting them in memory too.
func removeScheduledTasksFor(projectID, sessionID string) ([]ScheduledTaskRun, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledTask, 0, len(tasks))
	changed := false
	removedIDs := make(map[string]bool)
	for _, task := range tasks {
		if task.ProjectID == projectID && (sessionID == "" || task.SessionID == sessionID) {
			changed = true
			removedIDs[task.ID] = true
			continue
		}
		out = append(out, task)
	}
	if !changed {
		if sessionID == "" {
			return removeScheduledTaskRunsLocked(projectID, nil)
		}
		return nil, nil
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return nil, err
	}
	runOut := make([]ScheduledTaskRun, 0, len(runs))
	removedRuns := make([]ScheduledTaskRun, 0)
	for _, run := range runs {
		if run.ProjectID == projectID && removedIDs[run.TaskID] {
			removedRuns = append(removedRuns, run)
			continue
		}
		runOut = append(runOut, run)
	}
	if len(removedRuns) > 0 {
		if err := saveScheduledTaskRunsRaw(runOut); err != nil {
			return nil, err
		}
	}
	if err := saveScheduledTasksRaw(out); err != nil {
		if len(removedRuns) > 0 {
			if restoreErr := saveScheduledTaskRunsRaw(runs); restoreErr != nil {
				return nil, fmt.Errorf("remove scheduled tasks: %v; restoring run index also failed: %w", err, restoreErr)
			}
		}
		return nil, err
	}
	return removedRuns, nil
}

// removeScheduledTaskRunsLocked requires scheduledTasksMu. A nil taskIDs map
// removes every run for the project.
func removeScheduledTaskRunsLocked(projectID string, taskIDs map[string]bool) ([]ScheduledTaskRun, error) {
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return nil, err
	}
	out := make([]ScheduledTaskRun, 0, len(runs))
	removed := make([]ScheduledTaskRun, 0)
	for _, run := range runs {
		if run.ProjectID == projectID && (taskIDs == nil || taskIDs[run.TaskID]) {
			removed = append(removed, run)
			continue
		}
		out = append(out, run)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := saveScheduledTaskRunsRaw(out); err != nil {
		return nil, err
	}
	return removed, nil
}

func (s *Studio) RunScheduledTaskNow(projectID, taskID string) (ScheduledTaskRun, error) {
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		scheduledTasksMu.Unlock()
		return ScheduledTaskRun{}, err
	}
	var task *ScheduledTask
	for i := range tasks {
		if tasks[i].ID == taskID && tasks[i].ProjectID == projectID {
			copy := tasks[i]
			task = &copy
			break
		}
	}
	scheduledTasksMu.Unlock()
	if task == nil {
		return ScheduledTaskRun{}, fmt.Errorf("scheduled task not found")
	}
	run, dispatchErr := s.dispatchScheduledTask(*task)
	return run, dispatchErr
}

func (s *Studio) wakeScheduledTasks() {
	if s.scheduleWake == nil {
		return
	}
	select {
	case s.scheduleWake <- struct{}{}:
	default:
	}
}

func (s *Studio) runScheduledTasks() {
	if s.ctx == nil {
		return
	}
	s.reconcileInterruptedScheduledRuns()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.dispatchDueScheduledTasks(time.Now())
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.scheduleWake:
		}
	}
}

// A process restart cannot resume a provider stream or a pending approval.
// Convert persisted "running" rows to an explicit stopped state before the
// scheduler begins dispatching new work.
func (s *Studio) reconcileInterruptedScheduledRuns() {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		s.LogEvent("error", "scheduler", err.Error())
		return
	}
	changed := false
	now := time.Now().UnixMilli()
	for i := range runs {
		if scheduledTaskRunTerminal(runs[i].Status) {
			continue
		}
		oldStatus := runs[i].Status
		if oldStatus == "running" {
			runs[i].Status = "stopped"
		} else {
			runs[i].Status = "error"
		}
		runs[i].CompletedAt = now
		if oldStatus == "running" {
			runs[i].Error = "Gokin Studio closed before this run finished"
		} else {
			runs[i].Error = truncateUTF8(fmt.Sprintf(
				"invalid scheduled run status %q recovered at startup", oldStatus), maxScheduledTaskError)
		}
		changed = true
	}
	if changed {
		if err := saveScheduledTaskRunsRaw(runs); err != nil {
			s.LogEvent("error", "scheduler", err.Error())
			return
		}
	}
	// Repair a crash between the two durable commits too: the run row is the
	// authoritative execution record, while LastStatus is only a denormalized
	// card summary. Previously an already-terminal row was skipped here, so a
	// failed task-summary write left "running" in the UI forever after restart.
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		s.LogEvent("error", "scheduler", err.Error())
		return
	}
	latest := make(map[string]ScheduledTaskRun)
	terminalByID := make(map[string]ScheduledTaskRun)
	for _, run := range runs {
		if !scheduledTaskRunTerminal(run.Status) {
			continue
		}
		terminalByID[run.ID] = run
		current, exists := latest[run.TaskID]
		if !exists || run.StartedAt > current.StartedAt {
			latest[run.TaskID] = run
		}
	}
	taskChanged := false
	for i := range tasks {
		run, ok := ScheduledTaskRun{}, false
		if tasks[i].LastRunID != "" {
			run, ok = terminalByID[tasks[i].LastRunID]
			ok = ok && run.TaskID == tasks[i].ID && run.ProjectID == tasks[i].ProjectID
			if !ok {
				if tasks[i].LastStatus == "running" || tasks[i].LastStatus == "dispatching" {
					tasks[i].LastStatus = "error"
					tasks[i].LastError = truncateUTF8(fmt.Sprintf(
						"scheduled run %s lost durable tracking before startup reconciliation",
						tasks[i].LastRunID), maxScheduledTaskError)
					taskChanged = true
				}
				continue
			}
		} else {
			run, ok = latest[tasks[i].ID]
		}
		terminalAt := int64(0)
		if ok {
			terminalAt = run.CompletedAt
			if terminalAt <= 0 {
				terminalAt = run.StartedAt
			}
		}
		// dispatchDueScheduledTasks persists this state before it creates a
		// child/run. If the process dies in that gap there is intentionally no
		// row to reconcile, so convert the stale marker explicitly instead of
		// showing "dispatching" forever (or reverting to an older success).
		if tasks[i].LastStatus == "dispatching" && tasks[i].LastRunID == "" && (!ok || terminalAt < tasks[i].LastRunAt) {
			tasks[i].LastStatus = "stopped"
			tasks[i].LastError = "Gokin Studio closed before this scheduled run started"
			taskChanged = true
			continue
		}
		if !ok {
			if tasks[i].LastStatus == "running" {
				tasks[i].LastStatus = "error"
				tasks[i].LastError = "scheduled run lost durable tracking before startup reconciliation"
				taskChanged = true
			}
			continue
		}
		if tasks[i].LastRunID == "" && tasks[i].LastStatus == "running" && terminalAt < tasks[i].LastRunAt {
			tasks[i].LastStatus = "error"
			tasks[i].LastError = "scheduled run lost durable tracking before startup reconciliation"
			taskChanged = true
			continue
		}
		// A task may have a newer dispatch failure with no run row (for
		// example an unavailable model). Preserve that more recent summary.
		staleLiveSummary := tasks[i].LastStatus == "running" || tasks[i].LastStatus == "dispatching"
		if tasks[i].LastRunID == "" && !staleLiveSummary && tasks[i].LastRunAt >= terminalAt {
			continue
		}
		tasks[i].LastRunAt = terminalAt
		tasks[i].LastRunID = run.ID
		tasks[i].LastStatus = run.Status
		tasks[i].LastError = truncateUTF8(run.Error, maxScheduledTaskError)
		taskChanged = true
	}
	if taskChanged {
		if err := saveScheduledTasksRaw(tasks); err != nil {
			s.LogEvent("error", "scheduler", err.Error())
		}
	}
}

func (s *Studio) dispatchDueScheduledTasks(now time.Time) {
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		scheduledTasksMu.Unlock()
		s.LogEvent("error", "scheduler", err.Error())
		return
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		scheduledTasksMu.Unlock()
		s.LogEvent("error", "scheduler", err.Error())
		return
	}
	liveTasks := make(map[string]bool)
	for _, run := range runs {
		if !scheduledTaskRunTerminal(run.Status) {
			liveTasks[run.TaskID] = true
		}
	}
	var due []ScheduledTask
	changed := false
	for i := range tasks {
		if !tasks[i].Enabled || tasks[i].Schedule == "manual" || s.isProjectArchived(tasks[i].ProjectID) ||
			tasks[i].NextRunAt <= 0 || tasks[i].NextRunAt > now.UnixMilli() {
			continue
		}
		tasks[i].NextRunAt = nextScheduledRun(tasks[i], now).UnixMilli()
		changed = true
		if liveTasks[tasks[i].ID] {
			// Never overlap executions of one routine. Advance cadence so a long
			// run does not cause a tight catch-up loop, but preserve the exact live
			// summary until its correlated terminal callback arrives.
			continue
		}
		due = append(due, tasks[i])
		tasks[i].LastRunAt = now.UnixMilli()
		tasks[i].LastRunID = ""
		tasks[i].LastStatus = "dispatching"
		tasks[i].LastError = ""
	}
	if changed {
		err = saveScheduledTasksRaw(tasks)
	}
	scheduledTasksMu.Unlock()
	if err != nil {
		s.LogEvent("error", "scheduler", err.Error())
		return
	}
	for _, task := range due {
		if _, dispatchErr := s.dispatchScheduledTask(task); dispatchErr != nil {
			s.LogEvent("error", "scheduler", dispatchErr.Error())
		}
	}
}

func (s *Studio) dispatchScheduledTask(task ScheduledTask) (ScheduledTaskRun, error) {
	s.mu.RLock()
	project := s.projects[task.ProjectID]
	if project == nil {
		s.mu.RUnlock()
		if s.isProjectArchived(task.ProjectID) {
			err := fmt.Errorf("project was archived before the scheduled run started")
			s.updateScheduledTaskResult(task.ID, time.Now(), "stopped", err)
			return ScheduledTaskRun{}, err
		}
		err := fmt.Errorf("project not found: %s", task.ProjectID)
		s.updateScheduledTaskResult(task.ID, time.Now(), "error", err)
		return ScheduledTaskRun{}, err
	}
	// Keep archive's write lock out until the child session exists and
	// startMessage has synchronously claimed queueWorker. Archive then sees an
	// active project and refuses instead of leaving an orphan child history.
	defer s.mu.RUnlock()
	source := project.GetSession(task.SessionID)
	if source == nil || source.ID != task.SessionID {
		err := fmt.Errorf("session not found: %s", task.SessionID)
		s.updateScheduledTaskResult(task.ID, time.Now(), "error", err)
		return ScheduledTaskRun{}, err
	}
	// Migrate tasks written by the original scheduler, which inherited the
	// project model implicitly and had no explicit approval policy.
	if task.Provider == "" && task.Model == "" {
		project.mu.RLock()
		task.Provider, task.Model = project.Provider, project.Model
		project.mu.RUnlock()
	}
	task.ApprovalMode = normalizeScheduledApprovalMode(task.ApprovalMode)
	if task.ApprovalMode == "" {
		task.ApprovalMode = "manual"
	}
	if err := validateStudioProviderModelRuntime(task.Provider, task.Model); err != nil {
		s.updateScheduledTaskResult(task.ID, time.Now(), "error", err)
		return ScheduledTaskRun{}, err
	}
	if task.ApprovalMode != "manual" && task.ApprovalMode != "accept_edits" && task.ApprovalMode != "auto" && task.ApprovalMode != "skip" {
		err := fmt.Errorf("approval mode must be manual, accept_edits, auto, or skip")
		s.updateScheduledTaskResult(task.ID, time.Now(), "error", err)
		return ScheduledTaskRun{}, err
	}

	now := time.Now()
	session, err := createScheduledRunSession(project, task, now)
	if err != nil {
		s.updateScheduledTaskResult(task.ID, now, "error", err)
		return ScheduledTaskRun{}, err
	}
	run := ScheduledTaskRun{
		ID: uuid.NewString(), TaskID: task.ID, ProjectID: task.ProjectID,
		SessionID: session.ID, StartedAt: now.UnixMilli(), Status: "running",
		Provider: task.Provider, Model: task.Model, ApprovalMode: task.ApprovalMode,
	}
	evictedRuns, err := appendScheduledTaskRunForTask(task, run)
	if err != nil {
		persistErr := fmt.Errorf("persist scheduled run: %w", err)
		if cleanupErr := discardScheduledRunSession(project, session); cleanupErr != nil {
			// A failed rollback must be visible: the child was never published in
			// the run index, so the sessions event is now its only UI discovery
			// path. Keep it inspectable instead of pretending it disappeared.
			project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
				"projectID": task.ProjectID, "sessionID": session.ID,
			})
			persistErr = fmt.Errorf("%w; discard unpublished child session: %v", persistErr, cleanupErr)
		}
		if summaryErr := s.updateScheduledTaskResult(task.ID, now, "error", persistErr); summaryErr != nil {
			persistErr = fmt.Errorf("%w; persist scheduled task error summary: %v", persistErr, summaryErr)
		}
		return ScheduledTaskRun{}, persistErr
	}
	// Retention just dropped these rows; their chats and worktrees would be
	// unreachable from any UI path if we did not reap them here.
	s.reapEvictedScheduledRunSessions(task, evictedRuns)
	project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
		"projectID": task.ProjectID, "sessionID": session.ID,
	})
	// The Locked variant: this function holds s.mu.RLock for its whole body
	// (see the defer above), and read locks are not reentrant — calling the
	// ordinary startMessage here would block forever behind any pending writer.
	if err := s.claimScheduledTaskRunStart(task, run, now); err != nil {
		if finishErr := s.finalizeScheduledTaskRun(task, run, now, "error", err); finishErr != nil {
			return run, fmt.Errorf("start scheduled run: %v; %w", err, finishErr)
		}
		return run, err
	}
	if !s.startBackground("scheduled-task-run", func() {
		s.monitorScheduledTaskRun(task, run, project, session)
	}) {
		err := fmt.Errorf("studio is shutting down")
		session.Stop()
		if finishErr := s.finalizeScheduledTaskRun(task, run, now, "stopped", err); finishErr != nil {
			return run, fmt.Errorf("%v; %w", err, finishErr)
		}
		return run, err
	}
	return run, nil
}

func createScheduledRunSession(project *Project, task ScheduledTask, now time.Time) (*ChatSession, error) {
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()
	name := fmt.Sprintf("Scheduled · %s · %s", scheduledTaskTitle(task.Name, 52), now.Format("Jan 02 15:04"))
	session := NewChatSession(name)
	session.ParentID = task.SessionID
	session.executionProvider = task.Provider
	session.executionModel = task.Model
	session.executionPermissionMode = task.ApprovalMode

	project.mu.RLock()
	for {
		if _, exists := project.sessions[session.ID]; !exists {
			break
		}
		session = NewChatSession(name)
		session.ParentID = task.SessionID
		session.executionProvider = task.Provider
		session.executionModel = task.Model
		session.executionPermissionMode = task.ApprovalMode
	}
	project.mu.RUnlock()
	startDir, err := worktreeStartDirForParent(project, task.SessionID)
	if err != nil {
		return nil, err
	}
	if err := provisionSessionWorktree(project, session, startDir); err != nil {
		return nil, err
	}
	if err := SaveNewHistoryWithMetadata(
		projectSessionStorageKey(project.ID, session.ID), name, task.SessionID, nil,
	); err != nil {
		_ = removeSessionWorktree(project, session)
		return nil, fmt.Errorf("persist scheduled run session: %w", err)
	}
	project.mu.Lock()
	if _, exists := project.sessions[session.ID]; exists {
		project.mu.Unlock()
		_ = removeSessionWorktree(project, session)
		_ = deleteHistoryChecked(projectSessionStorageKey(project.ID, session.ID))
		return nil, fmt.Errorf("scheduled run session ID collision: %s", session.ID)
	}
	project.sessions[session.ID] = session
	project.mu.Unlock()
	return session, nil
}

// discardScheduledRunSession rolls back the unpublished child created before
// appendScheduledTaskRun. The run index is the ownership link used by history,
// deletion and retention; leaving a session behind when that append fails
// creates an unreachable chat (and, for Git projects, a full orphan worktree).
//
// The rollback mirrors DeleteChatSession's durable ordering while avoiding its
// scheduler cleanup side effects. The caller still holds s.mu.RLock, so it
// cannot invoke the public deletion path without risking a recursive read-lock
// deadlock behind a pending writer.
func discardScheduledRunSession(project *Project, session *ChatSession) error {
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()

	project.mu.Lock()
	stored, exists := project.sessions[session.ID]
	if !exists || stored != session {
		project.mu.Unlock()
		return fmt.Errorf("scheduled run session ownership changed: %s", session.ID)
	}
	session.mu.RLock()
	name := session.Name
	parentID := session.ParentID
	history := append([]*genai.Content(nil), session.history...)
	var usage *SessionUsage
	if session.usage != nil {
		copyUsage := *session.usage
		usage = &copyUsage
	}
	session.mu.RUnlock()
	storageKey := projectSessionStorageKey(project.ID, session.ID)
	if err := deleteHistoryChecked(storageKey); err != nil {
		project.mu.Unlock()
		return fmt.Errorf("delete unpublished scheduled run history: %w", err)
	}
	if err := removeSessionWorktreeAt(project, session, project.Directory); err != nil {
		restoreErr := SaveHistoryWithUsage(storageKey, name, parentID, usage, history)
		project.mu.Unlock()
		if restoreErr != nil {
			return fmt.Errorf("discard unpublished scheduled worktree: %v; restoring its history also failed: %w", err, restoreErr)
		}
		return fmt.Errorf("discard unpublished scheduled worktree: %w", err)
	}
	delete(project.sessions, session.ID)
	project.mu.Unlock()
	DiscardReplay(project.ID, session.ID)
	return nil
}

// appendScheduledTaskRun records a run and returns the rows evicted by the
// retention caps. Those rows are the ONLY authoritative link between a run and
// the chat session plus Git worktree it created, so the caller must clean them
// up: once a row is gone, no UI path can reach its session any more, and an
// interval schedule would otherwise leave one orphaned chat and one full repo
// checkout behind on every firing, forever.
func appendScheduledTaskRun(run ScheduledTaskRun) ([]ScheduledTaskRun, error) {
	return appendScheduledTaskRunOwned(nil, run)
}

// appendScheduledTaskRunForTask proves that the task still exists in the same
// critical section that publishes its child run. Run-now and deletion may race
// after either side has taken a snapshot; this makes the winner unambiguous:
// deletion first means dispatch rolls its unpublished child back, append first
// means deletion sees the durable row and stops the exact child.
func appendScheduledTaskRunForTask(task ScheduledTask, run ScheduledTaskRun) ([]ScheduledTaskRun, error) {
	return appendScheduledTaskRunOwned(&task, run)
}

func appendScheduledTaskRunOwned(task *ScheduledTask, run ScheduledTaskRun) ([]ScheduledTaskRun, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	if task != nil {
		tasks, err := loadScheduledTasksRaw()
		if err != nil {
			return nil, err
		}
		found := false
		for _, current := range tasks {
			if current.ID == task.ID && current.ProjectID == task.ProjectID {
				if !sameScheduledTaskExecution(current, *task) {
					return nil, fmt.Errorf("scheduled task changed before its run started")
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("scheduled task was deleted before its run started")
		}
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return nil, err
	}
	for _, current := range runs {
		if current.ID == run.ID {
			return nil, fmt.Errorf("scheduled run ID already exists: %s", run.ID)
		}
		if task != nil && current.ProjectID == task.ProjectID && current.TaskID == task.ID &&
			!scheduledTaskRunTerminal(current.Status) {
			return nil, fmt.Errorf("scheduled task is already running (run %s)", current.ID)
		}
	}
	runs = append(runs, run)
	kept, evicted, err := fitScheduledTaskRuns(runs)
	if err != nil {
		return nil, err
	}
	if err := saveScheduledTaskRunsRaw(kept); err != nil {
		return nil, err
	}
	return evicted, nil
}

func scheduledApprovalModeForExecution(mode string) string {
	mode = normalizeScheduledApprovalMode(mode)
	if mode == "" {
		return "manual"
	}
	return mode
}

func sameScheduledTaskExecution(current, expected ScheduledTask) bool {
	if current.ID != expected.ID || current.ProjectID != expected.ProjectID ||
		current.SessionID != expected.SessionID || current.Prompt != expected.Prompt ||
		current.Enabled != expected.Enabled ||
		scheduledApprovalModeForExecution(current.ApprovalMode) != scheduledApprovalModeForExecution(expected.ApprovalMode) {
		return false
	}
	// Original scheduler rows inherited the project model and persisted both
	// fields empty. dispatchScheduledTask resolves that legacy pair before it
	// reaches this check; accept the migration, but require exact equality once
	// either stored field is explicit so an edit cannot launch a stale model.
	if current.Provider == "" && current.Model == "" {
		return true
	}
	return current.Provider == expected.Provider && current.Model == expected.Model
}

// claimScheduledTaskRunStart closes the last gap between durable publication
// and paid work. Deletion holds the same lock while removing task/run rows, so
// it can occur entirely before this claim (which then refuses) or after the
// queueWorker is visible (which then stops it), never in between.
func (s *Studio) claimScheduledTaskRunStart(task ScheduledTask, run ScheduledTaskRun, now time.Time) error {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return fmt.Errorf("read scheduled tasks before run start: %w", err)
	}
	taskIndex := -1
	for i, current := range tasks {
		if current.ID == task.ID && current.ProjectID == task.ProjectID {
			if !sameScheduledTaskExecution(current, task) {
				return fmt.Errorf("scheduled task changed before its run started")
			}
			taskIndex = i
			break
		}
	}
	if taskIndex < 0 {
		return fmt.Errorf("scheduled task was deleted before its run started")
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return fmt.Errorf("read scheduled runs before start: %w", err)
	}
	runFound := false
	for _, current := range runs {
		if current.ID == run.ID && current.TaskID == task.ID && current.ProjectID == task.ProjectID &&
			current.SessionID == run.SessionID && current.Status == "running" {
			runFound = true
			break
		}
	}
	if !runFound {
		return fmt.Errorf("scheduled run lost durable ownership before it started")
	}
	// Commit the denormalized owner summary before opening the worker gate.
	// Deletion uses this same lock, so it cannot remove the task between this
	// durable write and the synchronous queueWorker transition below.
	tasks[taskIndex].LastRunAt = now.UnixMilli()
	tasks[taskIndex].LastRunID = run.ID
	tasks[taskIndex].LastStatus = "running"
	tasks[taskIndex].LastError = ""
	if err := saveScheduledTasksRaw(tasks); err != nil {
		return fmt.Errorf("persist scheduled task running state: %w", err)
	}
	return s.startMessageWithQueueEventPermissionLocked(
		task.ProjectID, task.Prompt, nil, run.SessionID, nil, "", nil,
	)
}

func loadScheduledTaskRunState(task ScheduledTask, runID string) (ScheduledTaskRun, bool, bool, error) {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return ScheduledTaskRun{}, false, false, err
	}
	taskFound := false
	for _, current := range tasks {
		if current.ID == task.ID && current.ProjectID == task.ProjectID {
			taskFound = true
			break
		}
	}
	if !taskFound {
		return ScheduledTaskRun{}, false, false, nil
	}
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return ScheduledTaskRun{}, false, true, err
	}
	for _, run := range runs {
		if run.ID == runID {
			return run, true, true, nil
		}
	}
	return ScheduledTaskRun{}, false, true, nil
}

// fitScheduledTaskRuns applies history caps without ever evicting a live row.
// A run row is the only durable owner of its child chat/worktree; dropping a
// running row would turn retention into an untracked provider turn as soon as
// guarded cleanup refuses a dirty checkout. Terminal history is expendable,
// live ownership is not. If live work alone exceeds a cap, the new append is
// rejected and dispatch rolls its unpublished child back.
func fitScheduledTaskRuns(runs []ScheduledTaskRun) ([]ScheduledTaskRun, []ScheduledTaskRun, error) {
	sorted := append([]ScheduledTaskRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartedAt > sorted[j].StartedAt })

	livePerTask := make(map[string]int)
	liveTotal := 0
	for _, run := range sorted {
		if scheduledTaskRunTerminal(run.Status) {
			continue
		}
		liveTotal++
		livePerTask[run.TaskID]++
		if livePerTask[run.TaskID] > maxRunsPerScheduledTask {
			return nil, nil, fmt.Errorf(
				"task %s already has %d live scheduled runs (retention limit %d)",
				run.TaskID, livePerTask[run.TaskID], maxRunsPerScheduledTask)
		}
	}
	if liveTotal > maxScheduledTaskRuns {
		return nil, nil, fmt.Errorf(
			"%d live scheduled runs exceed the global retention limit %d",
			liveTotal, maxScheduledTaskRuns)
	}

	perTask := make(map[string]int, len(livePerTask))
	kept := make([]ScheduledTaskRun, 0, min(len(sorted), maxScheduledTaskRuns))
	evicted := make([]ScheduledTaskRun, 0)
	// Reserve capacity concretely by keeping every live owner first. We sort the
	// result again below, so this does not alter the externally visible order.
	for _, candidate := range sorted {
		if !scheduledTaskRunTerminal(candidate.Status) {
			kept = append(kept, candidate)
			perTask[candidate.TaskID]++
		}
	}
	for _, candidate := range sorted {
		if !scheduledTaskRunTerminal(candidate.Status) {
			continue
		}
		if len(kept) >= maxScheduledTaskRuns || perTask[candidate.TaskID] >= maxRunsPerScheduledTask {
			evicted = append(evicted, candidate)
			continue
		}
		kept = append(kept, candidate)
		perTask[candidate.TaskID]++
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].StartedAt > kept[j].StartedAt })
	return kept, evicted, nil
}

// reapEvictedScheduledRunSessions removes the chats and worktrees belonging to
// run rows that retention just dropped. It runs in the background because
// DeleteChatSession takes the studio write lock while the dispatcher still
// holds it for reading. Deletion is best-effort by design: DeleteChatSession
// refuses a session that is still running or whose worktree is dirty, and
// those are exactly the ones that must be kept.
func (s *Studio) reapEvictedScheduledRunSessions(task ScheduledTask, evicted []ScheduledTaskRun) {
	if len(evicted) == 0 {
		return
	}
	s.startBackground("scheduled-run-reap", func() {
		owned, _, _, _ := s.scheduledTaskRunSessions(task, evicted)
		for _, sessionID := range sortedScheduledSessionIDs(owned) {
			if err := s.DeleteChatSession(task.ProjectID, sessionID); err != nil {
				s.LogEvent("warn", "scheduler", fmt.Sprintf(
					"retained scheduled run chat %s: %v", sessionID, err))
			}
		}
	})
}

func scheduledTaskRunTerminal(status string) bool {
	switch status {
	case "completed", "stopped", "error":
		return true
	}
	return false
}

// finishScheduledTaskRun durably claims a terminal transition. It returns the
// attempted snapshot on a write failure so callers can report the exact run,
// and refuses to revive or overwrite a row that is already terminal.
func finishScheduledTaskRun(runID, status string, runErr error) (ScheduledTaskRun, bool, error) {
	if !scheduledTaskRunTerminal(status) {
		return ScheduledTaskRun{}, false, fmt.Errorf("invalid terminal scheduled run status %q", status)
	}
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	runs, err := loadScheduledTaskRunsRaw()
	if err != nil {
		return ScheduledTaskRun{}, false, err
	}
	for i := range runs {
		if runs[i].ID != runID {
			continue
		}
		if scheduledTaskRunTerminal(runs[i].Status) {
			return runs[i], false, nil
		}
		runs[i].Status = status
		runs[i].CompletedAt = time.Now().UnixMilli()
		if runErr != nil {
			runs[i].Error = truncateUTF8(runErr.Error(), maxScheduledTaskError)
		} else {
			runs[i].Error = ""
		}
		updated := runs[i]
		if err := saveScheduledTaskRunsRaw(runs); err != nil {
			return updated, false, err
		}
		return updated, true, nil
	}
	return ScheduledTaskRun{}, false, fmt.Errorf("scheduled task run not found: %s", runID)
}

// finalizeScheduledTaskRun keeps the task summary subordinate to its durable
// run row. A failed terminal write is surfaced as an error on the task and in
// Diagnostics; it must never be flattened into a misleading "completed".
func (s *Studio) finalizeScheduledTaskRun(
	task ScheduledTask, run ScheduledTaskRun, now time.Time,
	status string, runErr error,
) error {
	_, changed, err := finishScheduledTaskRun(run.ID, status, runErr)
	if err != nil {
		persistErr := fmt.Errorf("persist scheduled run %s terminal state: %w", run.ID, err)
		s.LogEvent("error", "scheduler", persistErr.Error())
		if summaryErr := s.updateScheduledTaskRunResult(task.ID, run.ID, now, "error", persistErr); summaryErr != nil {
			return fmt.Errorf("%w; persist scheduled task error summary: %v", persistErr, summaryErr)
		}
		return persistErr
	}
	if !changed {
		return nil
	}
	if err := s.updateScheduledTaskRunResult(task.ID, run.ID, now, status, runErr); err != nil {
		summaryErr := fmt.Errorf("persist scheduled task %s result: %w", task.ID, err)
		s.LogEvent("error", "scheduler", summaryErr.Error())
		return summaryErr
	}
	return nil
}

func (s *Studio) monitorScheduledTaskRun(task ScheduledTask, run ScheduledTaskRun, project *Project, session *ChatSession) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	lastOwnershipCheck := time.Time{}
	for {
		select {
		case <-ctx.Done():
			err := fmt.Errorf("scheduled run stopped during shutdown")
			_ = s.finalizeScheduledTaskRun(task, run, time.Now(), "stopped", err)
			return
		case <-ticker.C:
			session.mu.RLock()
			active := session.queueWorker
			hasModel := false
			for _, content := range session.history {
				if content == nil || content.Role != "model" {
					continue
				}
				for _, part := range content.Parts {
					if part != nil && strings.TrimSpace(part.Text) != "" {
						hasModel = true
						break
					}
				}
				if hasModel {
					break
				}
			}
			session.mu.RUnlock()
			// A run index may be several MiB. Poll ownership at a bounded cadence
			// while the provider is active, but always re-check immediately once
			// the child becomes idle before committing a terminal result.
			if !active || lastOwnershipCheck.IsZero() || time.Since(lastOwnershipCheck) >= time.Second {
				lastOwnershipCheck = time.Now()
				stored, found, taskFound, storeErr := loadScheduledTaskRunState(task, run.ID)
				if !taskFound && storeErr == nil {
					// Intentional task deletion removes task and run rows under the same
					// scheduler lock, then stops the child. Stop here too so even a race
					// between durable deletion and in-memory cancellation fails closed.
					session.Stop()
					return
				}
				if storeErr != nil || !found {
					if storeErr == nil {
						storeErr = fmt.Errorf("durable scheduled run row disappeared")
					}
					session.Stop()
					err := fmt.Errorf("scheduled run %s lost durable tracking: %w", run.ID, storeErr)
					s.LogEvent("error", "scheduler", err.Error())
					if summaryErr := s.updateScheduledTaskRunResult(task.ID, run.ID, time.Now(), "error", err); summaryErr != nil {
						s.LogEvent("error", "scheduler", fmt.Sprintf("%v; persist error summary: %v", err, summaryErr))
					}
					project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
						"projectID": task.ProjectID, "sessionID": session.ID,
					})
					return
				}
				if scheduledTaskRunTerminal(stored.Status) {
					// A deletion/cancellation path won the durable state transition.
					// Never let the old monitor continue a paid child behind it.
					session.Stop()
					return
				}
				if stored.Status != "running" {
					session.Stop()
					err := fmt.Errorf("scheduled run %s has invalid durable status %q", run.ID, stored.Status)
					s.LogEvent("error", "scheduler", err.Error())
					if summaryErr := s.updateScheduledTaskRunResult(task.ID, run.ID, time.Now(), "error", err); summaryErr != nil {
						s.LogEvent("error", "scheduler", fmt.Sprintf("%v; persist error summary: %v", err, summaryErr))
					}
					project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
						"projectID": task.ProjectID, "sessionID": session.ID,
					})
					return
				}
			}
			if active {
				continue
			}
			if !hasModel {
				err := fmt.Errorf("run ended before a model response was saved")
				_ = s.finalizeScheduledTaskRun(task, run, time.Now(), "error", err)
				return
			}
			if err := s.finalizeScheduledTaskRun(task, run, time.Now(), "completed", nil); err != nil {
				return
			}
			project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
				"projectID": task.ProjectID, "sessionID": session.ID,
			})
			return
		}
	}
}

func (s *Studio) updateScheduledTaskResult(id string, now time.Time, status string, dispatchErr error) error {
	return s.updateScheduledTaskRunResult(id, "", now, status, dispatchErr)
}

func (s *Studio) updateScheduledTaskRunResult(id, runID string, now time.Time, status string, dispatchErr error) error {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		s.LogEvent("error", "scheduler", err.Error())
		return err
	}
	for i := range tasks {
		if tasks[i].ID != id {
			continue
		}
		if runID != "" {
			// A terminal callback belongs to one exact run. Once a newer run has
			// claimed the summary, the older callback remains valid in run history
			// but must not replace the card's current "running"/terminal status.
			if tasks[i].LastRunID != "" && tasks[i].LastRunID != runID {
				return nil
			}
			if tasks[i].LastRunID == "" && tasks[i].LastRunAt > now.UnixMilli() {
				return nil
			}
			tasks[i].LastRunID = runID
		} else {
			if tasks[i].LastRunID != "" &&
				(tasks[i].LastStatus == "running" || tasks[i].LastStatus == "dispatching") {
				return nil
			}
			tasks[i].LastRunID = ""
		}
		tasks[i].LastRunAt = now.UnixMilli()
		tasks[i].LastStatus = status
		if dispatchErr != nil {
			tasks[i].LastError = truncateUTF8(dispatchErr.Error(), maxScheduledTaskError)
		} else {
			tasks[i].LastError = ""
		}
		if err := saveScheduledTasksRaw(tasks); err != nil {
			s.LogEvent("error", "scheduler", err.Error())
			return err
		}
		return nil
	}
	return fmt.Errorf("scheduled task not found: %s", id)
}

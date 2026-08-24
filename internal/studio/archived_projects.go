package studio

import (
	"encoding/json"
	"fmt"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const archivedProjectsMaxBytes = 4 << 20

// ArchivedProjectRecord keeps the complete project configuration outside the
// active runtime map. Conversation history, memory, knowledge, artifacts,
// drafts, pins, and scheduled routines remain in their existing stores.
type ArchivedProjectRecord struct {
	Project    ProjectConfig `json:"project"`
	ArchivedAt int64         `json:"archivedAt"`
}

type ArchivedProjectInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Directory   string `json:"directory"`
	DirectoryOK bool   `json:"directoryOK"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	ArchivedAt  int64  `json:"archivedAt"`
}

func archivedProjectsPath() string {
	return filepath.Join(configDir(), "archived-projects.json")
}

func loadArchivedProjectsRaw() (map[string]ArchivedProjectRecord, error) {
	f, err := os.Open(archivedProjectsPath())
	if os.IsNotExist(err) {
		return make(map[string]ArchivedProjectRecord), nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, archivedProjectsMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > archivedProjectsMaxBytes {
		return nil, fmt.Errorf("archived project file exceeds the 4 MiB limit")
	}
	var records []ArchivedProjectRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse archived projects: %w", err)
	}
	if len(records) > StudioConfigMaxProjects {
		return nil, fmt.Errorf("archived project file exceeds the %d-project limit", StudioConfigMaxProjects)
	}
	out := make(map[string]ArchivedProjectRecord, len(records))
	for _, record := range records {
		if record.Project.ID == "" || record.Project.Directory == "" || record.ArchivedAt <= 0 {
			continue
		}
		record.Project.Name = truncateRunes(record.Project.Name, DisplayNameMaxRunes)
		if record.Project.Name == "" {
			record.Project.Name = filepath.Base(record.Project.Directory)
		}
		record.Project.Provider, record.Project.Model = normalizeStudioProviderModel(
			record.Project.Provider, record.Project.Model,
		)
		if record.Project.PermissionMode == "ask" {
			record.Project.PermissionMode = "manual"
		}
		if record.Project.PermissionMode == "acceptEdits" || record.Project.PermissionMode == "accept-edits" {
			record.Project.PermissionMode = "accept_edits"
		}
		if record.Project.PermissionMode != "" && record.Project.PermissionMode != "manual" &&
			record.Project.PermissionMode != "auto" && record.Project.PermissionMode != "accept_edits" &&
			record.Project.PermissionMode != "skip" {
			record.Project.PermissionMode = ""
		}
		out[record.Project.ID] = record
	}
	return out, nil
}

func saveArchivedProjectsRaw(records map[string]ArchivedProjectRecord) error {
	if len(records) == 0 {
		if err := os.Remove(archivedProjectsPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if len(records) > StudioConfigMaxProjects {
		return fmt.Errorf("at most %d projects can be archived", StudioConfigMaxProjects)
	}
	list := make([]ArchivedProjectRecord, 0, len(records))
	for _, record := range records {
		list = append(list, record)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ArchivedAt != list[j].ArchivedAt {
			return list[i].ArchivedAt > list[j].ArchivedAt
		}
		return list[i].Project.ID < list[j].Project.ID
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > archivedProjectsMaxBytes {
		return fmt.Errorf("archived projects exceed the 4 MiB storage limit")
	}
	return atomicWriteFile(archivedProjectsPath(), append(data, '\n'), 0o600)
}

func cloneArchivedProjects(records map[string]ArchivedProjectRecord) map[string]ArchivedProjectRecord {
	out := make(map[string]ArchivedProjectRecord, len(records))
	for id, record := range records {
		record.Project.ComputerAllowedApps = append([]string(nil), record.Project.ComputerAllowedApps...)
		record.Project.ComputerBlockedApps = append([]string(nil), record.Project.ComputerBlockedApps...)
		out[id] = record
	}
	return out
}

func (s *Studio) syncArchivedIDsLocked() {
	ids := make(map[string]bool, len(s.archived))
	for id := range s.archived {
		ids[id] = true
	}
	s.archivedIDs.Store(ids)
}

func (s *Studio) isProjectArchived(id string) bool {
	value := s.archivedIDs.Load()
	if value == nil {
		return false
	}
	ids, _ := value.(map[string]bool)
	return ids[id]
}

func activeProjectConfigsExcept(projects map[string]*Project, except string) []ProjectConfig {
	out := make([]ProjectConfig, 0, len(projects))
	for id, project := range projects {
		if id != except {
			out = append(out, project.ToConfig())
		}
	}
	return out
}

func projectHasActiveSession(project *Project) bool {
	project.mu.RLock()
	defer project.mu.RUnlock()
	for _, session := range project.sessions {
		session.mu.RLock()
		active := session.active || session.queueWorker
		session.mu.RUnlock()
		if active {
			return true
		}
	}
	return false
}

// ArchiveProject hides an idle project without deleting any project-owned
// data. Automatic routines are suspended by the scheduler's archived-ID gate.
func (s *Studio) ArchiveProject(id string) error {
	s.mu.Lock()
	project, ok := s.projects[id]
	if !ok {
		s.mu.Unlock()
		if s.isProjectArchived(id) {
			return fmt.Errorf("project is already archived")
		}
		return fmt.Errorf("project not found: %s", id)
	}
	if projectHasActiveSession(project) {
		s.mu.Unlock()
		return fmt.Errorf("stop the running project before archiving it")
	}
	project.mu.RLock()
	memoryStore := project.memoryStore
	project.mu.RUnlock()
	if memoryStore != nil {
		if err := memoryStore.Flush(); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("flush project memory before archive: %w", err)
		}
	}

	record := ArchivedProjectRecord{Project: project.ToConfig(), ArchivedAt: time.Now().UnixMilli()}
	nextArchived := cloneArchivedProjects(s.archived)
	nextArchived[id] = record
	nextActive := activeProjectConfigsExcept(s.projects, id)

	s.configSaveMu.Lock()
	archiveErr := saveArchivedProjectsRaw(nextArchived)
	var configErr error
	if archiveErr == nil {
		configErr = (&StudioConfig{Projects: nextActive, Groups: s.config.Groups, Settings: s.config.Settings}).Save()
	}
	if configErr != nil {
		_ = saveArchivedProjectsRaw(s.archived)
	}
	s.configSaveMu.Unlock()
	if archiveErr != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist project archive: %w", archiveErr)
	}
	if configErr != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist active projects after archive: %w", configErr)
	}

	for terminalID, terminal := range s.terminals {
		if terminal.ProjectID == id {
			delete(s.terminals, terminalID)
			terminal.Close()
		}
	}
	project.Close()
	delete(s.projects, id)
	s.archived = nextArchived
	s.config.Projects = nextActive
	s.syncArchivedIDsLocked()
	s.mu.Unlock()

	_ = s.refreshScheduledWakeNeed()
	s.wakeScheduledTasks()
	s.LogEvent("info", "projects", fmt.Sprintf("archived project %q", record.Project.Name))
	return nil
}

func (s *Studio) ListArchivedProjects() []ArchivedProjectInfo {
	s.mu.RLock()
	out := make([]ArchivedProjectInfo, 0, len(s.archived))
	for _, record := range s.archived {
		info := ArchivedProjectInfo{
			ID: record.Project.ID, Name: record.Project.Name, Directory: record.Project.Directory,
			Provider: record.Project.Provider, Model: record.Project.Model, ArchivedAt: record.ArchivedAt,
		}
		if stat, err := os.Stat(record.Project.Directory); err == nil && stat.IsDir() {
			info.DirectoryOK = true
		}
		out = append(out, info)
	}
	s.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ArchivedAt != out[j].ArchivedAt {
			return out[i].ArchivedAt > out[j].ArchivedAt
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RestoreProject reactivates an archived project. Existing automatic schedules
// are rebased from now so missed archive-time occurrences do not burst-run.
func (s *Studio) RestoreProject(id string) (*ProjectInfo, error) {
	s.mu.Lock()
	record, ok := s.archived[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("archived project not found: %s", id)
	}
	if len(s.projects)+len(s.archived) > StudioConfigMaxProjects {
		s.mu.Unlock()
		return nil, fmt.Errorf("project limit reached (%d)", StudioConfigMaxProjects)
	}
	for _, active := range s.projects {
		if sameProjectDirectory(active.Directory, record.Project.Directory) {
			s.mu.Unlock()
			return nil, fmt.Errorf("project directory is already active: %s", record.Project.Directory)
		}
	}

	project := NewProject(record.Project)
	project.studio = s
	nextActive := activeProjectConfigsExcept(s.projects, "")
	nextActive = append(nextActive, project.ToConfig())
	nextArchived := cloneArchivedProjects(s.archived)
	delete(nextArchived, id)

	s.configSaveMu.Lock()
	configErr := (&StudioConfig{Projects: nextActive, Groups: s.config.Groups, Settings: s.config.Settings}).Save()
	var archiveErr error
	if configErr == nil {
		archiveErr = saveArchivedProjectsRaw(nextArchived)
	}
	if archiveErr != nil {
		_ = (&StudioConfig{Projects: activeProjectConfigsExcept(s.projects, ""), Groups: s.config.Groups, Settings: s.config.Settings}).Save()
	}
	s.configSaveMu.Unlock()
	if configErr != nil {
		project.Close()
		s.mu.Unlock()
		return nil, fmt.Errorf("persist restored project: %w", configErr)
	}
	if archiveErr != nil {
		project.Close()
		s.mu.Unlock()
		return nil, fmt.Errorf("remove restored project from archive: %w", archiveErr)
	}

	s.projects[id] = project
	s.archived = nextArchived
	s.config.Projects = nextActive
	s.syncArchivedIDsLocked()
	s.mu.Unlock()

	if err := rebaseScheduledTasksForProject(id, time.Now()); err != nil {
		s.LogEvent("warn", "scheduler", fmt.Sprintf("rebase restored project schedules: %v", err))
	}
	_ = s.refreshScheduledWakeNeed()
	s.wakeScheduledTasks()
	s.LogEvent("info", "projects", fmt.Sprintf("restored project %q", record.Project.Name))
	return project.Info(), nil
}

// sameProjectDirectory reports whether two paths name the same project.
//
// For WSL paths the comparison is the canonical key, so the two UNC spellings
// and a differently-cased distro all collapse to one project — otherwise one
// repository could register twice and end up with two separate histories.
// The os.SameFile arm is deliberately skipped there: a network redirector is
// not required to supply unique file identifiers, and two zero IDs would make
// every second WSL project look like a duplicate of the first.
func sameProjectDirectory(left, right string) bool {
	if remoteProjectDirectory(left) || remoteProjectDirectory(right) {
		return wsl.CanonicalKey(left) == wsl.CanonicalKey(right)
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func rebaseScheduledTasksForProject(projectID string, now time.Time) error {
	scheduledTasksMu.Lock()
	defer scheduledTasksMu.Unlock()
	tasks, err := loadScheduledTasksRaw()
	if err != nil {
		return err
	}
	changed := false
	for i := range tasks {
		if tasks[i].ProjectID != projectID {
			continue
		}
		if tasks[i].Schedule == "manual" || !tasks[i].Enabled {
			tasks[i].NextRunAt = 0
		} else {
			tasks[i].NextRunAt = nextScheduledRun(tasks[i], now).UnixMilli()
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return saveScheduledTasksRaw(tasks)
}

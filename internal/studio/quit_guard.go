package studio

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	quitAndStopButton = "Quit and Stop"
	keepRunningButton = "Keep Running"
)

// QuitWorkSummary is an immutable, bounded-description snapshot. It contains
// counts only: no prompts, file paths, project names, or provider content enter
// the native dialog or logs.
type QuitWorkSummary struct {
	Projects        int
	RunningSessions int
	QueuedTurns     int
	SideQuestions   int
	Delegations     int
}

func (summary QuitWorkSummary) hasWork() bool {
	return summary.RunningSessions > 0 || summary.QueuedTurns > 0 ||
		summary.SideQuestions > 0 || summary.Delegations > 0
}

func (s *Studio) quitWorkSummary() QuitWorkSummary {
	summary := QuitWorkSummary{}
	projectIDs := make(map[string]bool)

	runningSessionKeys := make(map[string]bool)
	s.mu.RLock()
	for projectID, project := range s.projects {
		project.mu.RLock()
		for _, session := range project.sessions {
			session.mu.RLock()
			queued := len(session.queuedTurns)
			running := session.active || session.queueWorker || queued > 0
			session.mu.RUnlock()
			if running {
				summary.RunningSessions++
				projectIDs[projectID] = true
				runningSessionKeys[projectID+"_"+session.ID] = true
			}
			summary.QueuedTurns += queued
		}
		project.mu.RUnlock()
	}
	s.mu.RUnlock()

	// Side questions stream independently of the main ChatSession.active flag
	// and are also cancelled by process shutdown, so include them explicitly.
	s.sideChatMu.Lock()
	summary.SideQuestions = len(s.sideChatRuns)
	for _, run := range s.sideChatRuns {
		projectIDs[run.projectID] = true
	}
	s.sideChatMu.Unlock()

	// A delegation whose child session is already counted above must not be
	// counted twice. Only the genuine gap is added: the window between the run
	// record existing and the child turn going active, plus the monitor.
	s.delegationMu.Lock()
	for _, handle := range s.delegations {
		if runningSessionKeys[handle.toProjectID+"_"+handle.toSessionID] {
			continue
		}
		summary.Delegations++
		projectIDs[handle.toProjectID] = true
	}
	s.delegationMu.Unlock()

	summary.Projects = len(projectIDs)
	return summary
}

func countPhrase(count int, singular, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func quitWarningMessage(summary QuitWorkSummary) string {
	parts := make([]string, 0, 4)
	if summary.RunningSessions > 0 {
		parts = append(parts, countPhrase(summary.RunningSessions, "chat is still running", "chats are still running"))
	}
	if summary.QueuedTurns > 0 {
		parts = append(parts, countPhrase(summary.QueuedTurns, "follow-up is queued", "follow-ups are queued"))
	}
	if summary.SideQuestions > 0 {
		parts = append(parts, countPhrase(summary.SideQuestions, "side question is still running", "side questions are still running"))
	}
	if summary.Delegations > 0 {
		parts = append(parts, countPhrase(summary.Delegations, "delegation is still starting", "delegations are still starting"))
	}
	lead := strings.Join(parts, ", ") + "."
	projectLine := ""
	if summary.Projects > 0 {
		projectLine = " This work belongs to " + countPhrase(summary.Projects, "project", "projects") + "."
	}
	return lead + projectLine + "\n\nQuitting now stops active agents and clears queued follow-ups. Transcripts and recovery data already written to disk are preserved."
}

func (s *Studio) confirmQuit(summary QuitWorkSummary) (bool, error) {
	if s.testQuitConfirmation != nil {
		return s.testQuitConfirmation(summary)
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	options := wailsRuntime.MessageDialogOptions{
		Type:    wailsRuntime.QuestionDialog,
		Title:   "Quit Gokin Studio and stop active work?",
		Message: quitWarningMessage(summary),
	}
	// Wails v2 supports custom labels on macOS. Its Windows/Linux backends use
	// native Yes/No question dialogs, so keep No as the safe default there.
	if runtime.GOOS == "darwin" {
		options.Buttons = []string{quitAndStopButton, keepRunningButton}
		options.DefaultButton = keepRunningButton
		options.CancelButton = keepRunningButton
	} else {
		options.DefaultButton = "No"
		options.CancelButton = "No"
	}
	answer, err := wailsRuntime.MessageDialog(ctx, options)
	if err != nil {
		return false, err
	}
	return answer == quitAndStopButton || strings.EqualFold(answer, "yes"), nil
}

// BeforeClose is wired to Wails OnBeforeClose. Returning true prevents Quit.
// macOS HideWindowOnClose does not enter this callback, so closing the last
// window still quietly leaves Quick Entry and schedules alive in background.
func (s *Studio) BeforeClose(_ context.Context) (prevent bool) {
	summary := s.quitWorkSummary()
	if !summary.hasWork() {
		return false
	}

	// Repeated Cmd+Q/menu events while the native sheet is visible must not
	// create stacked dialogs. The already-open prompt owns the decision.
	s.quitPromptMu.Lock()
	if s.quitPromptOpen {
		s.quitPromptMu.Unlock()
		return true
	}
	s.quitPromptOpen = true
	s.quitPromptMu.Unlock()
	defer func() {
		s.quitPromptMu.Lock()
		s.quitPromptOpen = false
		s.quitPromptMu.Unlock()
	}()

	shouldQuit, err := s.confirmQuit(summary)
	if err != nil {
		// A dialog backend failure must never silently cancel active work.
		s.LogEvent("error", "quit-guard", "could not show active-work confirmation; quit was cancelled")
		return true
	}
	return !shouldQuit
}

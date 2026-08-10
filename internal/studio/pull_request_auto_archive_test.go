package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func setupPRAutoArchiveTest(t *testing.T) (*Studio, *Project, *ChatSession) {
	t.Helper()
	s := newStudioForTest(t)
	s.config.Settings.AutoArchivePRAfterClose = true
	project := NewProject(ProjectConfig{ID: "pr-auto", Name: "PR auto", Directory: t.TempDir()})
	project.studio = s
	project.testEmitter = func(string, any) {}
	target := NewChatSession("PR work")
	target.ID = "pr-work"
	target.history = []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("finish the pull request")}}}
	project.sessions[target.ID] = target
	s.projects[project.ID] = project
	return s, project, target
}

func TestPRAutoArchiveArchivesMergedOrClosedIdleSession(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			s, project, target := setupPRAutoArchiveTest(t)
			var archiveEvent map[string]any
			project.testEmitter = func(name string, data any) {
				if name == EventSessionsChanged {
					archiveEvent, _ = data.(map[string]any)
				}
			}
			archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{
				HasPullRequest: true, Number: 42, State: state,
			})
			if !archived || blocked != "" {
				t.Fatalf("auto archive = %v, %q", archived, blocked)
			}
			active, _ := s.ListChatSessions(project.ID)
			stored, _ := s.ListArchivedChatSessions(project.ID)
			if len(active) != 1 || len(stored) != 1 || stored[0].ID != target.ID {
				t.Fatalf("active=%#v archived=%#v", active, stored)
			}
			if archiveEvent["reason"] != "pull_request" || archiveEvent["pullRequestNumber"] != 42 || archiveEvent["pullRequestState"] != state {
				t.Fatalf("auto-archive event = %#v", archiveEvent)
			}
		})
	}
}

func TestPRAutoArchiveRetainsRunningDirtyUnavailableAndLastActive(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		s, project, target := setupPRAutoArchiveTest(t)
		target.mu.Lock()
		target.queueWorker = true
		target.mu.Unlock()
		archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "MERGED"})
		if archived || !strings.Contains(blocked, "finish") {
			t.Fatalf("running = %v, %q", archived, blocked)
		}
	})

	t.Run("dirty shared checkout", func(t *testing.T) {
		s, project, target := setupPRAutoArchiveTest(t)
		initGitRepo(t, project.Directory)
		if err := os.WriteFile(filepath.Join(project.Directory, "dirty.txt"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "CLOSED"})
		if archived || !strings.Contains(blocked, "uncommitted") {
			t.Fatalf("dirty = %v, %q", archived, blocked)
		}
	})

	t.Run("unavailable worktree", func(t *testing.T) {
		s, project, target := setupPRAutoArchiveTest(t)
		target.mu.Lock()
		target.WorktreePath = filepath.Join(t.TempDir(), "missing")
		target.WorktreeWorkDir = target.WorktreePath
		target.WorktreeBranch = "gokin/test/missing"
		target.mu.Unlock()
		archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "MERGED"})
		if archived || !strings.Contains(blocked, "unavailable") {
			t.Fatalf("unavailable = %v, %q", archived, blocked)
		}
	})

	t.Run("last active", func(t *testing.T) {
		s, project, target := setupPRAutoArchiveTest(t)
		project.sessions["default"].ArchivedAt = 1
		archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "MERGED"})
		if archived || !strings.Contains(blocked, "last active") {
			t.Fatalf("last active = %v, %q", archived, blocked)
		}
	})
}

func TestPRAutoArchiveRequiresOptInAndTerminalPRState(t *testing.T) {
	s, project, target := setupPRAutoArchiveTest(t)
	s.config.Settings.AutoArchivePRAfterClose = false
	if archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "MERGED"}); archived || blocked != "" {
		t.Fatalf("disabled = %v, %q", archived, blocked)
	}
	s.config.Settings.AutoArchivePRAfterClose = true
	if archived, blocked := s.autoArchiveSessionForPullRequest(project.ID, target.ID, project.Directory, &PullRequestStatus{HasPullRequest: true, State: "OPEN"}); archived || blocked != "" {
		t.Fatalf("open = %v, %q", archived, blocked)
	}
}

func TestPRAutoArchiveCandidateBatchesRotate(t *testing.T) {
	s, project, target := setupPRAutoArchiveTest(t)
	target.lastUsedAt = 100
	all := map[string]bool{target.ID: true}
	for index := 0; index < 24; index++ {
		session := NewChatSession("PR work")
		session.ID = fmt.Sprintf("pr-work-%02d", index)
		session.lastUsedAt = int64(99 - index)
		session.history = []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("finish the pull request")}}}
		project.sessions[session.ID] = session
		all[session.ID] = true
	}

	seen := make(map[string]bool)
	for _, batch := range [][]pullRequestArchiveCandidate{s.pullRequestArchiveCandidates(), s.pullRequestArchiveCandidates()} {
		if len(batch) != pullRequestArchivePollLimit {
			t.Fatalf("batch length = %d, want %d", len(batch), pullRequestArchivePollLimit)
		}
		for _, candidate := range batch {
			seen[candidate.sessionID] = true
		}
	}
	for id := range all {
		if !seen[id] {
			t.Errorf("session %q was starved across rotating batches", id)
		}
	}
}

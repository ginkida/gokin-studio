package studio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	pullRequestArchivePollInterval = 60 * time.Second
	pullRequestArchivePollLimit    = 20
)

type pullRequestArchiveCandidate struct {
	projectID string
	sessionID string
	directory string
	lastUsed  int64
}

func (s *Studio) pullRequestAutoArchiveEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config != nil && s.config.Settings.AutoArchivePRAfterClose
}

func (s *Studio) ensurePullRequestArchiveMonitor() {
	if s.ctx == nil {
		return
	}
	s.pullRequestArchiveOnce.Do(func() {
		s.startBackground("pull-request-auto-archive", s.runPullRequestArchiveMonitor)
	})
}

func (s *Studio) runPullRequestArchiveMonitor() {
	s.pollPullRequestArchives(s.ctx)
	ticker := time.NewTicker(pullRequestArchivePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.pollPullRequestArchives(s.ctx)
		}
	}
}

func (s *Studio) pullRequestArchiveCandidates() []pullRequestArchiveCandidate {
	s.mu.RLock()
	projects := make([]*Project, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	s.mu.RUnlock()

	candidates := make([]pullRequestArchiveCandidate, 0)
	for _, project := range projects {
		project.mu.RLock()
		projectID := project.ID
		sessions := make([]*ChatSession, 0, len(project.sessions))
		for _, session := range project.sessions {
			sessions = append(sessions, session)
		}
		project.mu.RUnlock()
		for _, session := range sessions {
			session.mu.RLock()
			eligible := session.ArchivedAt == 0 && !session.active && !session.queueWorker &&
				session.executionProvider == "" && !session.pluginAgentChild && len(session.history) > 0
			lastUsed := session.lastUsedAt
			if lastUsed == 0 {
				lastUsed = session.CreatedAt.UnixMilli()
			}
			sessionID := session.ID
			session.mu.RUnlock()
			if !eligible {
				continue
			}
			dir, err := sessionWorkingDirectory(project, session)
			if err != nil {
				continue
			}
			candidates = append(candidates, pullRequestArchiveCandidate{
				projectID: projectID, sessionID: sessionID, directory: dir, lastUsed: lastUsed,
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].lastUsed > candidates[j].lastUsed })
	if len(candidates) <= pullRequestArchivePollLimit {
		return candidates
	}
	// Bound each minute's GitHub CLI work without permanently starving older
	// sessions when a user has more than one batch of open local chats.
	start := int(s.pullRequestArchiveNext.Add(pullRequestArchivePollLimit)-pullRequestArchivePollLimit) % len(candidates)
	batch := make([]pullRequestArchiveCandidate, 0, pullRequestArchivePollLimit)
	for offset := 0; offset < pullRequestArchivePollLimit; offset++ {
		batch = append(batch, candidates[(start+offset)%len(candidates)])
	}
	return batch
}

func (s *Studio) pollPullRequestArchives(ctx context.Context) {
	if !s.pullRequestAutoArchiveEnabled() {
		return
	}
	for _, candidate := range s.pullRequestArchiveCandidates() {
		if ctx.Err() != nil || !s.pullRequestAutoArchiveEnabled() {
			return
		}
		status, err := s.pullRequestStatusCore(ctx, candidate.directory, false)
		if err != nil || !status.HasPullRequest || (status.State != "MERGED" && status.State != "CLOSED") {
			continue
		}
		if archived, _ := s.autoArchiveSessionForPullRequest(candidate.projectID, candidate.sessionID, candidate.directory, status); archived {
			s.logf("info", "pull-request", "auto-archived session %s after PR #%d became %s", candidate.sessionID, status.Number, strings.ToLower(status.State))
		}
	}
}

func (s *Studio) autoArchiveSessionForPullRequest(projectID, sessionID, directory string, status *PullRequestStatus) (bool, string) {
	if !s.pullRequestAutoArchiveEnabled() || status == nil || !status.HasPullRequest ||
		(status.State != "MERGED" && status.State != "CLOSED") {
		return false, ""
	}
	_, session, err := s.exactStudioSession(projectID, sessionID)
	if err != nil {
		return false, "session is no longer available"
	}
	session.mu.RLock()
	running := session.active || session.queueWorker
	archived := session.ArchivedAt > 0
	unattended := session.executionProvider != "" || session.pluginAgentChild
	session.mu.RUnlock()
	if archived {
		return false, ""
	}
	if running {
		return false, "waiting for the session to finish"
	}
	if unattended {
		return false, "unattended run sessions are retained"
	}
	worktree := sessionWorktreeStatus(session)
	if worktree.Error != "" {
		return false, "worktree unavailable: " + worktree.Error
	}
	if worktree.Dirty {
		return false, fmt.Sprintf("%d uncommitted worktree change(s)", worktree.ChangedFiles)
	}
	if !worktree.Isolated && strings.TrimSpace(directory) != "" && runGitBool(directory, "rev-parse", "--is-inside-work-tree") {
		if output := strings.TrimSpace(runGit(directory, "status", "--porcelain=v1", "--untracked-files=all")); output != "" {
			return false, "shared checkout has uncommitted changes"
		}
	}
	if err := s.archiveChatSession(projectID, sessionID, map[string]any{
		"reason":            "pull_request",
		"pullRequestNumber": status.Number,
		"pullRequestState":  status.State,
	}); err != nil {
		if strings.Contains(err.Error(), "last active") {
			return false, "last active chat is retained"
		}
		if strings.Contains(err.Error(), "running") {
			return false, "waiting for the session to finish"
		}
		return false, err.Error()
	}
	return true, ""
}

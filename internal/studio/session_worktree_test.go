package studio

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func prepareSessionWorktreeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := writeFile(filepath.Join(dir, "tracked.txt"), "root-v1\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, ".gitignore"), "secret.env\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, ".worktreeinclude"), "secret.env\nnotignored.txt\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "secret.env"), "TOKEN=test-only\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "notignored.txt"), "do not copy\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, dir, "add", "tracked.txt", ".gitignore", ".worktreeinclude")
	gitMust(t, dir, "commit", "-m", "seed worktree fixture")
	return dir
}

func TestGitSessionsUsePersistentIndependentWorktrees(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	marker := filepath.Join(repo, "post-checkout-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := writeFile(hook, "#!/bin/sh\ntouch \""+marker+"\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatal(err)
	}

	projectInfo, err := s.AddProject("isolated", repo)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	p := s.projects[projectInfo.ID]
	first := p.sessions["default"]
	firstInfo := first.Info()
	if !firstInfo.WorktreeIsolated || firstInfo.WorktreePath == "" || firstInfo.WorktreeBranch == "" {
		t.Fatalf("first Git chat was not isolated: %+v", firstInfo)
	}
	if firstInfo.WorktreePath == repo {
		t.Fatal("worktree unexpectedly aliases the project root")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("post-checkout hook ran despite hook suppression: %v", err)
	}
	secret, err := os.ReadFile(filepath.Join(firstInfo.WorktreePath, "secret.env"))
	if err != nil || string(secret) != "TOKEN=test-only\n" {
		t.Fatalf(".worktreeinclude secret was not copied: %q, %v", secret, err)
	}
	if _, err := os.Stat(filepath.Join(firstInfo.WorktreePath, "notignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("non-ignored include match must not be copied: %v", err)
	}

	secondInfo, err := s.CreateChatSession(projectInfo.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	if !secondInfo.WorktreeIsolated || secondInfo.WorktreePath == firstInfo.WorktreePath {
		t.Fatalf("second chat did not receive an independent checkout: %+v", secondInfo)
	}
	termID, err := s.OpenSessionTerminal(projectInfo.ID, secondInfo.ID)
	if err != nil {
		t.Fatalf("OpenSessionTerminal: %v", err)
	}
	s.mu.RLock()
	terminalDir := s.terminals[termID].cmd.Dir
	s.mu.RUnlock()
	if terminalDir != secondInfo.WorktreePath {
		t.Fatalf("terminal opened in %q, want %q", terminalDir, secondInfo.WorktreePath)
	}
	if err := s.CloseTerminal(termID); err != nil {
		t.Fatalf("CloseTerminal: %v", err)
	}
	if err := writeFile(filepath.Join(firstInfo.WorktreePath, "tracked.txt"), "first-only\n"); err != nil {
		t.Fatal(err)
	}
	sessionFile, err := s.ReadSessionFileContent(projectInfo.ID, "default", "tracked.txt")
	if err != nil || sessionFile != "first-only\n" {
		t.Fatalf("session file preview used the wrong checkout: %q, %v", sessionFile, err)
	}
	sessionFiles, err := s.ListSessionFiles(projectInfo.ID, "default")
	if err != nil || !slices.Contains(sessionFiles, "tracked.txt") {
		t.Fatalf("session autocomplete files missing tracked.txt: %v, %v", sessionFiles, err)
	}
	gitContext, err := s.GetSessionGitContext(projectInfo.ID, "default")
	if err != nil || gitContext.Branch != firstInfo.WorktreeBranch || len(gitContext.ChangedFiles) != 1 {
		t.Fatalf("session Git context used the wrong checkout: %+v, %v", gitContext, err)
	}
	p.registry = tools.DefaultRegistry(repo)
	registry, registryDir, err := p.registryForSession(first, "glm")
	if err != nil || registryDir != firstInfo.WorktreePath {
		t.Fatalf("resolve session tool registry: %s, %v", registryDir, err)
	}
	readTool, ok := registry.Get("read")
	if !ok {
		t.Fatal("isolated registry has no read tool")
	}
	readResult, err := readTool.Execute(context.Background(), map[string]any{"file_path": filepath.Join(firstInfo.WorktreePath, "tracked.txt")})
	if err != nil || !readResult.Success || !strings.Contains(readResult.Content, "first-only") {
		t.Fatalf("read tool was not rooted in the session checkout: %+v, %v", readResult, err)
	}
	secondTracked, err := os.ReadFile(filepath.Join(secondInfo.WorktreePath, "tracked.txt"))
	if err != nil || string(secondTracked) != "root-v1\n" {
		t.Fatalf("first chat leaked into second checkout: %q, %v", secondTracked, err)
	}
	rootTracked, _ := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if string(rootTracked) != "root-v1\n" {
		t.Fatalf("isolated edit leaked into project root: %q", rootTracked)
	}

	status, err := s.GetSessionWorktreeStatus(projectInfo.ID, "default")
	if err != nil || !status.Dirty || status.ChangedFiles != 1 {
		t.Fatalf("dirty status mismatch: %+v, %v", status, err)
	}
	if err := s.DeleteChatSession(projectInfo.ID, "default"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree deletion should be blocked, got %v", err)
	}
	if p.sessions["default"] == nil {
		t.Fatal("blocked deletion removed the in-memory session")
	}
	if _, err := LoadHistory(projectSessionStorageKey(projectInfo.ID, "default")); err != nil {
		t.Fatalf("blocked deletion did not restore history: %v", err)
	}

	if err := writeFile(filepath.Join(firstInfo.WorktreePath, "tracked.txt"), "root-v1\n"); err != nil {
		t.Fatal(err)
	}
	checkout := firstInfo.WorktreePath
	branch := firstInfo.WorktreeBranch
	if err := s.DeleteChatSession(projectInfo.ID, "default"); err != nil {
		t.Fatalf("delete clean isolated chat: %v", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("clean checkout still exists: %v", err)
	}
	if out, err := runGitWorktreeCommand(repo, "show-ref", "--verify", "refs/heads/"+branch); err == nil {
		t.Fatalf("unused managed branch still exists: %s", out)
	}
}

func TestSideQuestionUsesSessionWorktreeContext(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	projectInfo, err := s.AddProject("side-worktree", repo)
	if err != nil {
		t.Fatal(err)
	}
	p := s.projects[projectInfo.ID]
	session := p.sessions["default"]
	wantDir := session.Info().WorktreePath
	var capturedDir string
	p.testExecutionClientFactory = func(
		_ Settings, _, _, _, _, workDir string, _ map[string]bool, _ bool,
	) (client.Client, *tools.Registry, error) {
		capturedDir = workDir
		return &mockClient{responses: []mockResp{{text: "side answer"}}}, tools.NewRegistry(), nil
	}
	events := make(chan string, 2)
	s.testSideChatEmitter = func(name string, _ SideChatEvent) { events <- name }
	if err := s.StartSideQuestion(projectInfo.ID, "default", "worktree-side", "Where am I?"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event == EventSideChatComplete {
				goto completed
			}
			if event == EventSideChatError {
				t.Fatal("side chat failed")
			}
		case <-deadline:
			t.Fatal("side chat did not complete")
		}
	}

completed:
	if capturedDir != wantDir {
		t.Fatalf("side chat used %q, want session worktree %q", capturedDir, wantDir)
	}
}

func TestSessionWorktreeRestoresAndFailsClosed(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	projectInfo, err := s.AddProject("restore", repo)
	if err != nil {
		t.Fatal(err)
	}
	p := s.projects[projectInfo.ID]
	original := p.sessions["default"].Info()
	reloaded := NewProject(p.ToConfig())
	restored := reloaded.sessions["default"].Info()
	if !restored.WorktreeIsolated || restored.WorktreePath != original.WorktreePath || restored.WorktreeError != "" {
		t.Fatalf("worktree metadata did not restore: %+v", restored)
	}
	if err := writeFile(sessionWorktreeRecordPath(projectInfo.ID, "default"), "{not-json"); err != nil {
		t.Fatal(err)
	}
	corruptReload := NewProject(p.ToConfig())
	corruptSession := corruptReload.sessions["default"]
	if info := corruptSession.Info(); info.WorktreeError == "" || info.WorktreeIsolated {
		t.Fatalf("corrupt metadata did not fail closed: %+v", info)
	}
	if err := removeSessionWorktree(corruptReload, corruptSession); err == nil {
		t.Fatal("corrupt worktree metadata allowed destructive session cleanup")
	}

	if err := os.Rename(original.WorktreePath, original.WorktreePath+"-moved"); err != nil {
		t.Fatal(err)
	}
	status := sessionWorktreeStatus(reloaded.sessions["default"])
	if status.Error == "" {
		t.Fatalf("missing checkout was reported as healthy: %+v", status)
	}
	if _, err := sessionWorkingDirectory(reloaded, reloaded.sessions["default"]); err == nil {
		t.Fatal("missing recorded checkout silently fell back to project root")
	}
}

func TestSessionWorktreePreservesSelectedProjectSubdirectory(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	subdir := filepath.Join(repo, "packages", "app")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(subdir, "app.txt"), "app\n"); err != nil {
		t.Fatal(err)
	}
	gitMust(t, repo, "add", "packages/app/app.txt")
	gitMust(t, repo, "commit", "-m", "add selected subdirectory")

	projectInfo, err := s.AddProject("subdir", subdir)
	if err != nil {
		t.Fatal(err)
	}
	sessionInfo := s.projects[projectInfo.ID].sessions["default"].Info()
	if filepath.Base(sessionInfo.WorktreePath) != "app" || filepath.Base(filepath.Dir(sessionInfo.WorktreePath)) != "packages" {
		t.Fatalf("session workdir did not preserve selected subdirectory: %s", sessionInfo.WorktreePath)
	}
	if _, err := os.Stat(filepath.Join(sessionInfo.WorktreePath, "app.txt")); err != nil {
		t.Fatalf("selected subdirectory file unavailable: %v", err)
	}
}

func TestPruneEmptySessionsKeepsWorktreeChanges(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	projectInfo, err := s.AddProject("prune", repo)
	if err != nil {
		t.Fatal(err)
	}
	p := s.projects[projectInfo.ID]
	defaultInfo := p.sessions["default"].Info()
	secondInfo, err := s.CreateChatSession(projectInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(defaultInfo.WorktreePath, "tracked.txt"), "valuable\n"); err != nil {
		t.Fatal(err)
	}
	p.pruneAbandonedEmptySessions()
	if p.GetSession("default") == nil {
		t.Fatal("empty conversation with worktree changes was pruned")
	}
	p.mu.RLock()
	_, secondStillPresent := p.sessions[secondInfo.ID]
	p.mu.RUnlock()
	if secondStillPresent {
		t.Fatal("clean empty worktree session was not pruned")
	}
	if _, err := os.Stat(secondInfo.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("pruned worktree checkout still exists: %v", err)
	}
}

func TestBackgroundChildSessionsAlsoReceiveWorktrees(t *testing.T) {
	s := newStudioForTest(t)
	repo := prepareSessionWorktreeRepo(t)
	projectInfo, err := s.AddProject("children", repo)
	if err != nil {
		t.Fatal(err)
	}
	p := s.projects[projectInfo.ID]
	pluginChild, err := createPluginAgentSession(
		p, "default", "reviewer", "glm", "glm-5.2", "auto", "Review", map[string]bool{"read": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if info := pluginChild.Info(); !info.WorktreeIsolated || info.WorktreePath == p.sessions["default"].Info().WorktreePath {
		t.Fatalf("plugin agent child is not independently isolated: %+v", info)
	}

	scheduledChild, err := createScheduledRunSession(p, ScheduledTask{
		ID: "scheduled", ProjectID: p.ID, SessionID: "default",
		Provider: "glm", Model: "glm-5.2", ApprovalMode: "auto",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if info := scheduledChild.Info(); !info.WorktreeIsolated || info.WorktreePath == pluginChild.Info().WorktreePath {
		t.Fatalf("scheduled child is not independently isolated: %+v", info)
	}
}

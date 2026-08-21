package studio

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tasks"
)

func TestShutdownClosesProviderAndMCPClients(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := newStudioForTest(t)
	provider := &mockClient{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mc, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatalf("connectMCP: %v", err)
	}

	p := NewProject(ProjectConfig{ID: "shutdown-project", Name: "Shutdown", Directory: t.TempDir()})
	p.studio = s
	p.client = provider
	p.mcpClients = []*mcpClient{mc}
	s.projects[p.ID] = p

	s.Shutdown(context.Background())
	// Repeated framework callbacks must be harmless and must not double-close
	// transports whose Close methods are not necessarily idempotent.
	s.Shutdown(context.Background())

	provider.mu.Lock()
	closeCalls := provider.closeCalls
	provider.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("provider Close called %d times, want 1", closeCalls)
	}
	if p.client != nil || len(p.mcpClients) != 0 {
		t.Fatalf("project retained clients after shutdown: provider=%v mcp=%d", p.client != nil, len(p.mcpClients))
	}
	// A process terminated with Process.Kill has a reaped ProcessState but
	// Exited reports false on Unix because it was signalled, not exited
	// normally.
	if mc.cmd.ProcessState == nil {
		t.Fatal("MCP child process was not reaped during shutdown")
	}
}

func TestShutdownWaitsForTrackedBackgroundAndClosesGate(t *testing.T) {
	s := newStudioForTest(t)
	started := make(chan struct{})
	release := make(chan struct{})
	if !s.startBackground("lifecycle-test", func() {
		close(started)
		<-release
	}) {
		t.Fatal("background task rejected before shutdown")
	}
	<-started

	done := make(chan struct{})
	go func() {
		s.Shutdown(context.Background())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Shutdown returned while tracked work was still running")
	case <-time.After(50 * time.Millisecond):
	}
	if s.startBackground("too-late", func() {}) {
		t.Fatal("background task accepted after shutdown began")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not finish after tracked work completed")
	}
}

func TestShutdownCancelsRootContextAndRejectsSend(t *testing.T) {
	s := newStudioForTest(t)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	p := NewProject(ProjectConfig{ID: "late-send", Name: "Late", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	s.Shutdown(context.Background())
	select {
	case <-s.ctx.Done():
	default:
		t.Fatal("Shutdown did not cancel the Studio root context")
	}
	if err := s.SendMessage(p.ID, "must not run", "default"); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("SendMessage after shutdown error = %v, want shutting-down error", err)
	}
}

func TestShutdownPermanentlyClosesBackgroundTaskManagers(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "shutdown-tasks", Name: "Tasks", Directory: t.TempDir()})
	p.studio = s
	projectManager := tasks.NewManager(p.Directory)
	sessionManager := tasks.NewManager(p.Directory)
	p.taskManager = projectManager
	p.sessions["default"].taskManager = sessionManager
	s.projects[p.ID] = p

	s.Shutdown(context.Background())
	for name, manager := range map[string]*tasks.Manager{
		"project": projectManager,
		"session": sessionManager,
	} {
		if _, err := manager.Start(context.Background(), "unused"); !errors.Is(err, tasks.ErrManagerClosed) {
			t.Fatalf("%s manager Start after Shutdown = %v, want ErrManagerClosed", name, err)
		}
	}
}

func TestStopCancelsWorkWithoutClosingCachedClient(t *testing.T) {
	provider := &mockClient{}
	p := NewProject(ProjectConfig{ID: "stop-project", Name: "Stop", Directory: t.TempDir()})
	p.client = provider

	p.Stop()

	provider.mu.Lock()
	closeCalls := provider.closeCalls
	provider.mu.Unlock()
	if closeCalls != 0 {
		t.Fatalf("Stop closed reusable provider client %d times", closeCalls)
	}
	if p.client != provider {
		t.Fatal("Stop discarded reusable provider client")
	}

	p.Close()
	provider.mu.Lock()
	closeCalls = provider.closeCalls
	provider.mu.Unlock()
	if closeCalls != 1 || p.client != nil {
		t.Fatalf("Close did not tear down provider: calls=%d retained=%v", closeCalls, p.client != nil)
	}
}

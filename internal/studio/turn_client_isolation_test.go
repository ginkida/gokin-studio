package studio

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

type cloningStudioClient struct {
	*mockClient
	mu     sync.Mutex
	clones []*mockClient
}

func (c *cloningStudioClient) WithModel(string) client.Client {
	clone := &mockClient{responses: []mockResp{{text: "isolated response"}}}
	c.mu.Lock()
	c.clones = append(c.clones, clone)
	c.mu.Unlock()
	return clone
}

func TestProjectUsesPerTurnClientCloneForEphemeralContext(t *testing.T) {
	_ = withTempHistoryDir(t)
	base := &cloningStudioClient{mockClient: &mockClient{}}
	p := &Project{
		ID: "isolated", Name: "Isolated", Directory: t.TempDir(),
		Provider: "glm", Model: "glm-5.1",
		sessions: map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		client:   base, registry: tools.NewRegistry(), pinnedContext: "session-specific snapshot",
		testEmitter: func(string, any) {}, retryInitialDelay: time.Millisecond,
	}
	p.SendMessage(context.Background(), "question", Settings{DefaultProvider: "glm", DefaultModel: "glm-5.1"})

	base.mu.Lock()
	if len(base.clones) != 1 {
		base.mu.Unlock()
		t.Fatalf("WithModel clone count = %d, want 1", len(base.clones))
	}
	clone := base.clones[0]
	base.mu.Unlock()
	base.mockClient.mu.Lock()
	baseContext := base.lastTurnContext
	base.mockClient.mu.Unlock()
	clone.mu.Lock()
	cloneContext := clone.lastTurnContext
	clone.mu.Unlock()
	if baseContext != "" || cloneContext != "session-specific snapshot" {
		t.Fatalf("turn context was not isolated: base=%q clone=%q", baseContext, cloneContext)
	}
}

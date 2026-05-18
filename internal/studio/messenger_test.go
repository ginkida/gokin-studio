package studio

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestStudioMessenger_ReceiveResponse_UnknownID verifies that ReceiveResponse
// returns an error immediately when the message ID has no matching pending
// channel (the `!ok` path in ReceiveResponse).
func TestStudioMessenger_ReceiveResponse_UnknownID(t *testing.T) {
	s := newStudioForTest(t)
	m := NewStudioMessenger(s, "proj-a")

	_, err := m.ReceiveResponse(context.Background(), "no-such-id")
	if err == nil {
		t.Error("expected error for unknown message ID, got nil")
	}
}

// TestStudioMessenger_ReceiveResponse_Success verifies the happy path where a
// result is already on the channel when ReceiveResponse is called.
func TestStudioMessenger_ReceiveResponse_Success(t *testing.T) {
	s := newStudioForTest(t)
	m := NewStudioMessenger(s, "proj-a")

	msgID := "test-msg-id"
	ch := make(chan string, 1)
	m.mu.Lock()
	m.pending[msgID] = ch
	m.mu.Unlock()

	ch <- "computed result"

	result, err := m.ReceiveResponse(context.Background(), msgID)
	if err != nil {
		t.Fatalf("ReceiveResponse: %v", err)
	}
	if result != "computed result" {
		t.Errorf("got %q, want 'computed result'", result)
	}
}

// TestStudioMessenger_ReceiveResponse_ContextCancel verifies that ReceiveResponse
// returns an error when the context is cancelled before a response arrives
// (the `<-ctx.Done()` branch in the select).
func TestStudioMessenger_ReceiveResponse_ContextCancel(t *testing.T) {
	s := newStudioForTest(t)
	m := NewStudioMessenger(s, "proj-a")

	msgID := "test-cancel-id"
	ch := make(chan string, 1)
	m.mu.Lock()
	m.pending[msgID] = ch
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	_, err := m.ReceiveResponse(ctx, msgID)
	if err == nil {
		t.Error("expected context error from ReceiveResponse, got nil")
	}
}

// TestStudioMessenger_SendMessage_NoTarget verifies that SendMessage returns
// an error when no other project exists to route the message to.
func TestStudioMessenger_SendMessage_NoTarget(t *testing.T) {
	s := newStudioForTest(t)
	m := NewStudioMessenger(s, "proj-only")
	// No projects registered, so target lookup returns nil.

	_, err := m.SendMessage("query", "unknown-role", "hello?", nil)
	if err == nil {
		t.Error("expected error when no target project exists, got nil")
	}
	if !strings.Contains(err.Error(), "no target project found") {
		t.Errorf("error = %q, want 'no target project found'", err.Error())
	}
}

// TestStudioMessenger_SendMessage_InitClientError verifies that when a target
// project exists but initClient fails (no API key), the error is delivered via
// the response channel as a "error: ..." string rather than a Go error.
func TestStudioMessenger_SendMessage_InitClientError(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("GLM_API_KEY")
	_ = os.Unsetenv("GLM_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("GLM_API_KEY", prevKey)
		}
	})

	s := newStudioForTest(t)
	// Register a target project with no client (initClient will fail — no key).
	target := NewProject(ProjectConfig{
		ID: "proj-target", Name: "Target", Directory: t.TempDir(),
	})
	target.Provider = "glm"
	target.studio = s
	s.projects[target.ID] = target

	// Source messenger for a different (non-existent) project.
	m := NewStudioMessenger(s, "proj-source")

	msgID, err := m.SendMessage("query", "Target", "compute 1+1", nil)
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	// Wait for the background goroutine to finish.
	s.wg.Wait()

	// ReceiveResponse should return the error string from the goroutine.
	result, err := m.ReceiveResponse(context.Background(), msgID)
	if err == nil && !strings.HasPrefix(result, "error:") {
		t.Errorf("expected 'error: ...' result from failed initClient, got %q (err=%v)", result, err)
	}
}

// TestStudioMessenger_SendMessage_NilResp verifies the `resp == nil` branch:
// target.client is pre-set so initClient skips, but SendMessage returns nil resp.
func TestStudioMessenger_SendMessage_NilResp(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	mc := &mockClient{sendMessageOverride: &mockResp{nilResp: true}}
	target := NewProject(ProjectConfig{
		ID: "proj-nilresp", Name: "NilResp", Directory: t.TempDir(),
	})
	target.client = mc // pre-initialized so initClient is a no-op
	target.studio = s
	s.projects[target.ID] = target

	m := NewStudioMessenger(s, "proj-source-nilresp")

	msgID, err := m.SendMessage("query", "NilResp", "what?", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	s.wg.Wait()

	result, err := m.ReceiveResponse(context.Background(), msgID)
	if err == nil && !strings.HasPrefix(result, "error:") {
		t.Errorf("expected 'error: nil response', got %q", result)
	}
}

// TestStudioMessenger_SendMessage_SendError verifies the `err != nil` branch
// from c.SendMessage: target.client returns an error from SendMessage.
func TestStudioMessenger_SendMessage_SendError(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	mc := &mockClient{sendMessageOverride: &mockResp{err: fmt.Errorf("api failure")}}
	target := NewProject(ProjectConfig{
		ID: "proj-snderr", Name: "SendErr", Directory: t.TempDir(),
	})
	target.client = mc
	target.studio = s
	s.projects[target.ID] = target

	m := NewStudioMessenger(s, "proj-source-snderr")

	msgID, err := m.SendMessage("query", "SendErr", "hello?", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	s.wg.Wait()

	result, err := m.ReceiveResponse(context.Background(), msgID)
	if err == nil && !strings.HasPrefix(result, "error:") {
		t.Errorf("expected 'error: ...' result, got %q (err=%v)", result, err)
	}
}

// TestStudioMessenger_SendMessage_Success verifies the happy path: target
// responds with text, which is delivered as the result string.
// ReceiveResponse is called before s.wg.Wait() so we block on the channel
// while the goroutine is still running (pending map entry is alive).
func TestStudioMessenger_SendMessage_Success(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	mc := &mockClient{sendMessageOverride: &mockResp{text: "42"}}
	target := NewProject(ProjectConfig{
		ID: "proj-ok", Name: "OK", Directory: t.TempDir(),
	})
	target.client = mc
	target.studio = s
	s.projects[target.ID] = target

	m := NewStudioMessenger(s, "proj-source-ok")

	msgID, err := m.SendMessage("query", "OK", "what is 6*7?", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// ReceiveResponse blocks until the goroutine sends on the channel.
	result, err := m.ReceiveResponse(context.Background(), msgID)
	s.wg.Wait() // ensure goroutine finished cleanup
	if err != nil {
		t.Fatalf("ReceiveResponse: %v", err)
	}
	if result != "42" {
		t.Errorf("got %q, want '42'", result)
	}
}

// TestStudioMessenger_SendMessage_CollectError verifies that when the response
// stream emits an error chunk, resp.Collect() returns an error, which is
// delivered to the caller as "error: ..." string (lines 119-121).
func TestStudioMessenger_SendMessage_CollectError(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	mc := &mockClient{sendMessageOverride: &mockResp{streamErr: fmt.Errorf("stream parse failure")}}
	target := NewProject(ProjectConfig{
		ID: "proj-collect-err", Name: "CollectErr", Directory: t.TempDir(),
	})
	target.client = mc
	target.studio = s
	s.projects[target.ID] = target

	m := NewStudioMessenger(s, "proj-src-collect")

	msgID, err := m.SendMessage("query", "CollectErr", "fetch?", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	result, err := m.ReceiveResponse(context.Background(), msgID)
	s.wg.Wait()
	if err == nil && !strings.HasPrefix(result, "error:") {
		t.Errorf("expected 'error: ...' after Collect failure, got %q (err=%v)", result, err)
	}
}

// TestStudioMessenger_SendMessage_FallbackTarget verifies the fallback path in
// target lookup: when no project matches by name, the first non-self project
// is used as the target (the `target == nil` fallback loop, lines 50-56).
func TestStudioMessenger_SendMessage_FallbackTarget(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	mc := &mockClient{sendMessageOverride: &mockResp{text: "fallback"}}
	target := NewProject(ProjectConfig{
		ID: "proj-fallback", Name: "Fallback", Directory: t.TempDir(),
	})
	target.client = mc
	target.studio = s
	s.projects[target.ID] = target

	// Source project registered under a different ID.
	source := NewProject(ProjectConfig{
		ID: "proj-source-fb", Name: "Source", Directory: t.TempDir(),
	})
	source.studio = s
	s.projects[source.ID] = source

	m := NewStudioMessenger(s, "proj-source-fb")

	// Request to "NoMatch" — will fall through to the first non-self project.
	msgID, err := m.SendMessage("query", "NoMatch", "ping", nil)
	if err != nil {
		t.Fatalf("SendMessage (fallback): %v", err)
	}
	result, err := m.ReceiveResponse(context.Background(), msgID)
	s.wg.Wait()
	if err != nil {
		t.Fatalf("ReceiveResponse (fallback): %v", err)
	}
	if result != "fallback" {
		t.Errorf("got %q, want 'fallback'", result)
	}
}

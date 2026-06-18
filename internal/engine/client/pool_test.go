package client

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// fakeClient is a minimal Client stub for pool-identity tests.
type fakeClient struct {
	id     string
	closed bool
}

func (f *fakeClient) Close() error                  { f.closed = true; return nil }
func (f *fakeClient) SetTools(_ []*genai.Tool)      {}
func (f *fakeClient) SetSystemInstruction(_ string) {}
func (f *fakeClient) SetThinkingBudget(_ int32)     {}
func (f *fakeClient) SetRateLimiter(_ interface{})  {}
func (f *fakeClient) GetModel() string              { return f.id }
func (f *fakeClient) SetModel(_ string)             {}
func (f *fakeClient) WithModel(_ string) Client     { return f }
func (f *fakeClient) GetRawClient() interface{}     { return nil }
func (f *fakeClient) SendMessage(_ context.Context, _ string) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) SendMessageWithHistory(_ context.Context, _ []*genai.Content, _ string) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) SendFunctionResponse(_ context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*StreamingResponse, error) {
	return nil, nil
}
func (f *fakeClient) CountTokens(_ context.Context, _ []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{}, nil
}

// Regression: the pool keys by (provider, model) only — not by API key or
// any other config field. When a new key is saved to config.yaml and the
// factory asks the pool for a cached entry, without Invalidate it returns
// the OLD client built with the OLD key — requests go out with stale
// credentials and the user sees 401 despite a valid key on disk.
func TestClientPool_InvalidateEvictsAndClosesEntry(t *testing.T) {
	pool := NewClientPool(5)
	oldClient := &fakeClient{id: "old-key-client"}
	pool.Put("kimi", "kimi-for-coding", oldClient)

	got, ok := pool.Get("kimi", "kimi-for-coding")
	if !ok || got != oldClient {
		t.Fatalf("pre-check: pool.Get should return the put client, got %+v ok=%v", got, ok)
	}

	pool.Invalidate("kimi", "kimi-for-coding")

	got, ok = pool.Get("kimi", "kimi-for-coding")
	if ok || got != nil {
		t.Errorf("after Invalidate, pool.Get should miss; got %+v ok=%v", got, ok)
	}
	if !oldClient.closed {
		t.Error("Invalidate must Close() the evicted client to release its HTTP resources")
	}

	newClient := &fakeClient{id: "new-key-client"}
	pool.Put("kimi", "kimi-for-coding", newClient)
	got, ok = pool.Get("kimi", "kimi-for-coding")
	if !ok || got != newClient {
		t.Errorf("after re-Put, should see new client; got %+v ok=%v", got, ok)
	}
	if newClient.closed {
		t.Error("fresh client must not be closed")
	}
}

// Invalidating a non-existent entry must be a silent no-op.
func TestClientPool_InvalidateMissingEntryIsNoOp(t *testing.T) {
	pool := NewClientPool(5)
	pool.Invalidate("kimi", "kimi-for-coding")
	pool.Invalidate("", "")
	pool.Close()
}

// Invalidate on a closed pool must short-circuit.
func TestClientPool_InvalidateOnClosedPool(t *testing.T) {
	pool := NewClientPool(5)
	c := &fakeClient{id: "x"}
	pool.Put("kimi", "kimi-for-coding", c)
	pool.Close()
	pool.Invalidate("kimi", "kimi-for-coding")
}

// Invalidate must be scoped to (provider, model) — unrelated entries survive.
func TestClientPool_InvalidateIsScoped(t *testing.T) {
	pool := NewClientPool(5)
	kimi := &fakeClient{id: "kimi"}
	deepseek := &fakeClient{id: "deepseek"}
	pool.Put("kimi", "kimi-for-coding", kimi)
	pool.Put("deepseek", "deepseek-v4-pro", deepseek)

	pool.Invalidate("kimi", "kimi-for-coding")

	if _, ok := pool.Get("kimi", "kimi-for-coding"); ok {
		t.Error("kimi entry should be gone")
	}
	if !kimi.closed {
		t.Error("kimi client should be closed")
	}

	got, ok := pool.Get("deepseek", "deepseek-v4-pro")
	if !ok || got != deepseek {
		t.Errorf("deepseek entry should remain; got %+v ok=%v", got, ok)
	}
	if deepseek.closed {
		t.Error("deepseek client must not be closed by an unrelated Invalidate")
	}
}

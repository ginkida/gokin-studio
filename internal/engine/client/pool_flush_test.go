package client

import (
	"testing"
)

func TestParsePoolKey(t *testing.T) {
	tests := []struct {
		input        string
		wantProvider string
		wantModel    string
		wantOK       bool
	}{
		{"deepseek:deepseek-v4-pro", "deepseek", "deepseek-v4-pro", true},
		{"glm:glm-4-flash", "glm", "glm-4-flash", true},
		{"empty:", "empty", "", true},
		{":start", "", "start", true},
		{"nocolon", "", "", false},
		{"", "", "", false},
		{"a:b:c", "a", "b:c", true}, // split on first ':' only
	}

	for _, tt := range tests {
		p, m, ok := parsePoolKey(tt.input)
		if ok != tt.wantOK || p != tt.wantProvider || m != tt.wantModel {
			t.Errorf("parsePoolKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, p, m, ok, tt.wantProvider, tt.wantModel, tt.wantOK)
		}
	}
}

func TestFlushProvider_RemovesOnlyMatchingProvider(t *testing.T) {
	pool := NewClientPool(10)

	pool.Put("deepseek", "deepseek-v4-pro", &fakeClient{id: "ds-pro"})
	pool.Put("deepseek", "deepseek-chat", &fakeClient{id: "ds-chat"})
	pool.Put("glm", "glm-4-flash", &fakeClient{id: "glm-flash"})
	pool.Put("kimi", "kimi-latest", &fakeClient{id: "kimi"})

	pool.FlushProvider("deepseek")

	if _, ok := pool.Get("deepseek", "deepseek-v4-pro"); ok {
		t.Error("deepseek:deepseek-v4-pro should have been flushed")
	}
	if _, ok := pool.Get("deepseek", "deepseek-chat"); ok {
		t.Error("deepseek:deepseek-chat should have been flushed")
	}

	if _, ok := pool.Get("glm", "glm-4-flash"); !ok {
		t.Error("glm:glm-4-flash should survive FlushProvider")
	}
	if _, ok := pool.Get("kimi", "kimi-latest"); !ok {
		t.Error("kimi:kimi-latest should survive FlushProvider")
	}
}

func TestFlushProvider_DoesNotAffectClosedPool(t *testing.T) {
	pool := NewClientPool(10)
	pool.Put("deepseek", "deepseek-chat", &fakeClient{id: "ds"})
	pool.Close()
	pool.FlushProvider("deepseek")
}

func TestFlushProvider_NilPool(t *testing.T) {
	var pool *ClientPool
	pool.FlushProvider("deepseek")
}

package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testMCPHTTPRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

func writeMCPJSON(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func decodeTestMCPRequest(t *testing.T, r *http.Request) testMCPHTTPRequest {
	t.Helper()
	defer r.Body.Close()
	var request testMCPHTTPRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatalf("decode MCP request: %v", err)
	}
	return request
}

func TestRemoteMCPModernJSONDiscoveryCallAndHeaders(t *testing.T) {
	var listCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeTestMCPRequest(t, r)
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcpHTTPProtocolVersion {
			t.Errorf("protocol header = %q", got)
		}
		if got := r.Header.Get("Mcp-Method"); got != request.Method {
			t.Errorf("method header = %q, body = %q", got, request.Method)
		}
		meta, _ := request.Params["_meta"].(map[string]any)
		if meta["io.modelcontextprotocol/protocolVersion"] != mcpHTTPProtocolVersion {
			t.Errorf("modern metadata = %#v", meta)
		}
		clientCapabilities, _ := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
		extensions, _ := clientCapabilities["extensions"].(map[string]any)
		if _, ok := extensions[mcpAppExtensionID]; !ok {
			t.Errorf("modern metadata lacks MCP Apps capability: %#v", meta)
		}
		switch request.Method {
		case "tools/list":
			listCalls.Add(1)
			writeMCPJSON(t, w, request.ID, map[string]any{
				"tools": []any{map[string]any{
					"name": "weather", "description": "Weather by region",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"region": map[string]any{
								"type": "string", "x-mcp-header": "Region",
							},
						},
					},
				}},
			})
		case "tools/call":
			if got := r.Header.Get("Mcp-Name"); got != "weather" {
				t.Errorf("Mcp-Name = %q", got)
			}
			wantRegion := "=?base64?" + base64.StdEncoding.EncodeToString([]byte("Алматы")) + "?="
			if got := r.Header.Get("Mcp-Param-Region"); got != wantRegion {
				t.Errorf("Mcp-Param-Region = %q, want %q", got, wantRegion)
			}
			writeMCPJSON(t, w, request.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "sunny"}},
			})
		default:
			t.Errorf("unexpected method %q", request.Method)
			writeMCPJSON(t, w, request.ID, map[string]any{})
		}
	}))
	defer server.Close()

	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "remote", Transport: "http", URL: server.URL,
		Headers: map[string]string{"Authorization": "Bearer test-secret"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatalf("connectMCP: %v", err)
	}
	defer client.Close()
	if listCalls.Load() != 1 || len(client.tools) != 1 {
		t.Fatalf("discovery calls=%d tools=%d", listCalls.Load(), len(client.tools))
	}

	var result struct {
		Content []map[string]any `json:"content"`
	}
	if err := client.request(ctx, "tools/call", map[string]any{
		"name": "weather", "arguments": map[string]any{"region": "Алматы"},
	}, &result); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0]["text"] != "sunny" {
		t.Fatalf("tool result = %#v", result)
	}
}

func TestRemoteMCPModernSSEDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeTestMCPRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		response, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"result": map[string]any{"tools": []any{map[string]any{
				"name": "echo", "inputSchema": map[string]any{"type": "object"},
			}}},
		})
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", response)
	}))
	defer server.Close()
	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "sse", Transport: "http", URL: server.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatalf("connectMCP SSE: %v", err)
	}
	defer client.Close()
	if len(client.tools) != 1 || client.tools[0].Name != "echo" {
		t.Fatalf("SSE tools = %#v", client.tools)
	}
}

func TestRemoteMCPLegacyHandshakeSessionAndClose(t *testing.T) {
	const sessionID = "test-session-123"
	var initialized atomic.Bool
	deleted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != sessionID {
				t.Errorf("DELETE session = %q", r.Header.Get("Mcp-Session-Id"))
			}
			w.WriteHeader(http.StatusNoContent)
			deleted <- struct{}{}
			return
		}
		request := decodeTestMCPRequest(t, r)
		if request.Method != "initialize" && initialized.Load() &&
			r.Header.Get("Mcp-Session-Id") != sessionID {
			t.Errorf("%s missing session header", request.Method)
		}
		switch request.Method {
		case "tools/list":
			if !initialized.Load() {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32002, "message": "Server not initialized"},
				})
				return
			}
			writeMCPJSON(t, w, request.ID, map[string]any{"tools": []any{map[string]any{
				"name": "legacy", "inputSchema": map[string]any{"type": "object"},
			}}})
		case "initialize":
			if r.Header.Get("MCP-Protocol-Version") != mcpHTTPLegacyVersion {
				t.Errorf("legacy initialize version = %q", r.Header.Get("MCP-Protocol-Version"))
			}
			initialized.Store(true)
			w.Header().Set("Mcp-Session-Id", sessionID)
			writeMCPJSON(t, w, request.ID, map[string]any{
				"protocolVersion": mcpHTTPLegacyVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "legacy"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected legacy method %q", request.Method)
		}
	}))
	defer server.Close()
	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "legacy", Transport: "http", URL: server.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatalf("connect legacy: %v", err)
	}
	if len(client.tools) != 1 || client.http.modern {
		t.Fatalf("legacy negotiation = modern:%v tools:%#v", client.http.modern, client.tools)
	}
	_ = client.Close()
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("legacy MCP session was not terminated with DELETE")
	}
}

func TestRemoteMCPConfigSecurityValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  MCPServerConfig
		want string
	}{
		{
			name: "plain external HTTP",
			cfg:  MCPServerConfig{Name: "x", Transport: "http", URL: "http://example.com/mcp"},
			want: "localhost",
		},
		{
			name: "credentials in URL",
			cfg:  MCPServerConfig{Name: "x", Transport: "http", URL: "https://user:pass@example.com/mcp"},
			want: "credentials",
		},
		{
			name: "token query",
			cfg:  MCPServerConfig{Name: "x", Transport: "http", URL: "https://example.com/mcp?access_token=secret"},
			want: "Authorization header",
		},
		{
			name: "header injection",
			cfg: MCPServerConfig{Name: "x", Transport: "http", URL: "https://example.com/mcp",
				Headers: map[string]string{"Authorization": "Bearer ok\r\nX-Evil: yes"}},
			want: "invalid value",
		},
		{
			name: "reserved header",
			cfg: MCPServerConfig{Name: "x", Transport: "http", URL: "https://example.com/mcp",
				Headers: map[string]string{"Mcp-Session-Id": "override"}},
			want: "reserved",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateMCPConfig(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRemoteMCPInvalidHeaderAnnotationExcludesOnlyThatTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeTestMCPRequest(t, r)
		writeMCPJSON(t, w, request.ID, map[string]any{"tools": []any{
			map[string]any{
				"name": "bad",
				"inputSchema": map[string]any{
					"type":  "object",
					"items": map[string]any{"type": "string", "x-mcp-header": "Bad"},
				},
			},
			map[string]any{"name": "good", "inputSchema": map[string]any{"type": "object"}},
		}})
	}))
	defer server.Close()
	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "headers", Transport: "http", URL: server.URL, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.tools) != 1 || client.tools[0].Name != "good" {
		t.Fatalf("filtered tools = %#v", client.tools)
	}
}

func TestRemoteMCPRedirectDoesNotForwardCredentials(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("connector credential reached redirect target")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	cfg, err := validateMCPConfig(MCPServerConfig{
		Name: "redirect", Transport: "http", URL: redirector.URL,
		Headers: map[string]string{"Authorization": "Bearer must-not-leak"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := connectMCP(ctx, cfg); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetHits.Load())
	}
}

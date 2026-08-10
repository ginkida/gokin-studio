package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/config"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
)

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		id, hasID := req["id"]
		method, _ := req["method"].(string)
		if !hasID {
			continue
		}
		switch method {
		case "initialize":
			if os.Getenv("GO_MCP_APP") == "1" {
				params, _ := req["params"].(map[string]any)
				capabilities, _ := params["capabilities"].(map[string]any)
				extensions, _ := capabilities["extensions"].(map[string]any)
				if _, ok := extensions[mcpAppExtensionID]; !ok {
					_ = encoder.Encode(map[string]any{
						"jsonrpc": "2.0", "id": id,
						"error": map[string]any{"code": -32602, "message": "MCP Apps capability missing"},
					})
					continue
				}
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": mcpProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "test-mcp", "version": "0.0.1"},
				},
			})
		case "tools/list":
			if os.Getenv("GO_MCP_LARGE_DECL_PAGES") == "1" {
				params, _ := req["params"].(map[string]any)
				cursor, _ := params["cursor"].(string)
				page := 0
				if cursor == "page-1" {
					page = 1
				} else if cursor == "page-2" {
					page = 2
				}
				largeTools := make([]map[string]any, 32)
				for i := range largeTools {
					largeTools[i] = map[string]any{
						"name": fmt.Sprintf("large_%d_%d", page, i), "description": strings.Repeat("d", 45000),
						"inputSchema": map[string]any{"type": "object"},
					}
				}
				next := ""
				if page < 2 {
					next = fmt.Sprintf("page-%d", page+1)
				}
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"result": map[string]any{"tools": largeTools, "nextCursor": next},
				})
				continue
			}
			nextCursor := ""
			if os.Getenv("GO_MCP_PAGINATION_LOOP") == "1" {
				nextCursor = "same-cursor"
			}
			toolsList := []map[string]any{{
				"name":        "echo",
				"description": "Echo text",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string", "description": "Text"}},
					"required":   []string{"text"},
				},
			}}
			if os.Getenv("GO_MCP_APP") == "1" {
				toolsList[0]["_meta"] = map[string]any{"ui": map[string]any{
					"resourceUri": "ui://test/dashboard", "visibility": []any{"model", "app"},
				}}
				toolsList = append(toolsList, map[string]any{
					"name": "refresh_ui", "inputSchema": map[string]any{"type": "object"},
					"_meta": map[string]any{"ui": map[string]any{
						"resourceUri": "ui://test/dashboard", "visibility": []any{"app"},
					}},
				})
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools":      toolsList,
					"nextCursor": nextCursor,
				},
			})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			if text == "rpc-error" {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{"code": -32001, "message": "remote tool rejected input"},
				})
				continue
			}
			content := []map[string]any{{"type": "text", "text": "echo: " + text}}
			if text == "oversized" {
				content = []map[string]any{{"type": "text", "text": strings.Repeat("Ж", 40000)}}
			}
			if text == "mixed" {
				content = append(content, map[string]any{"type": "image", "mimeType": "image/png", "data": "abc123"})
			}
			result := map[string]any{
				"content": content,
			}
			if os.Getenv("GO_MCP_APP") == "1" {
				result["structuredContent"] = map[string]any{"message": text, "count": 3}
				result["_meta"] = map[string]any{"source": "test"}
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			})
		case "resources/read":
			params, _ := req["params"].(map[string]any)
			resourceURI, _ := params["uri"].(string)
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"contents": []any{map[string]any{
					"uri": resourceURI, "mimeType": mcpAppMIMEType,
					"text": "<!doctype html><div id=\"app\">MCP App</div>",
					"_meta": map[string]any{"ui": map[string]any{
						"prefersBorder": true,
						"csp": map[string]any{
							"connectDomains":  []any{"https://api.example.com", "wss://stream.example.com"},
							"resourceDomains": []any{"https://cdn.example.com"},
						},
					}},
				}}},
			})
		default:
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
	os.Exit(0)
}

func testMCPConfig(t *testing.T) MCPServerConfig {
	t.Helper()
	return MCPServerConfig{
		Name:    "local",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestMCPHelperProcess"},
		Env:     map[string]string{"GO_WANT_MCP_HELPER": "1"},
		Enabled: true,
	}
}

func TestMCPToolNamesRemainUniqueAfterNormalizationAndTruncation(t *testing.T) {
	first := mcpToolName("docs server", "read-file")
	second := mcpToolName("docs-server", "read file")
	if first == second {
		t.Fatalf("normalized names collided: %q", first)
	}
	long := mcpToolName(strings.Repeat("server", 20), strings.Repeat("tool", 30))
	if len(long) > 63 {
		t.Fatalf("tool name exceeds provider limit: %d bytes (%q)", len(long), long)
	}
}

func TestMCPRemoteToolMetadataIsBounded(t *testing.T) {
	remote := mcpRemoteTool{
		Name: "  echo  ", Description: strings.Repeat("Ж", maxMCPDescriptionRunes+100),
		InputSchema: map[string]any{"type": "object"},
	}
	normalized, size, err := normalizeMCPRemoteTool(remote)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != "echo" || len([]rune(normalized.Description)) != maxMCPDescriptionRunes+1 || size <= 0 {
		t.Fatalf("unexpected normalized metadata: name=%q descRunes=%d size=%d", normalized.Name, len([]rune(normalized.Description)), size)
	}
	if !utf8.ValidString(normalized.Description) || !strings.HasSuffix(normalized.Description, "…") {
		t.Fatal("description truncation was not Unicode-safe")
	}
	remote.Name = ""
	if _, _, err := normalizeMCPRemoteTool(remote); err == nil {
		t.Fatal("expected empty remote tool name rejection")
	}
}

func TestMCPRemoteToolRejectsOversizedAndDeepSchemas(t *testing.T) {
	oversized := mcpRemoteTool{
		Name: "large", Description: strings.Repeat("x", maxMCPToolDeclarationBytes+1),
		InputSchema: map[string]any{"type": "object"},
	}
	if _, _, err := normalizeMCPRemoteTool(oversized); err == nil || !strings.Contains(err.Error(), "64 KB") {
		t.Fatalf("expected declaration-size rejection, got %v", err)
	}

	deep := map[string]any{"type": "string"}
	for i := 0; i < maxMCPSchemaDepth+2; i++ {
		deep = map[string]any{"type": "object", "properties": map[string]any{"nested": deep}}
	}
	if _, _, err := normalizeMCPRemoteTool(mcpRemoteTool{Name: "deep", InputSchema: deep}); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("expected schema-depth rejection, got %v", err)
	}
	// Conversion remains defensive even when called directly outside discovery.
	converted := schemaFromMCP(deep)
	for i := 0; i <= maxMCPSchemaDepth && converted != nil; i++ {
		converted = converted.Properties["nested"]
	}
}

func TestMCPSchemaNodeBudget(t *testing.T) {
	props := make(map[string]any, maxMCPSchemaNodes+1)
	for i := 0; i <= maxMCPSchemaNodes; i++ {
		props[fmt.Sprintf("p%d", i)] = map[string]any{"type": "string"}
	}
	nodes := 0
	err := validateMCPSchema(map[string]any{"type": "object", "properties": props}, 0, &nodes)
	if err == nil || !strings.Contains(err.Error(), "nodes") {
		t.Fatalf("expected schema-node rejection, got %v (nodes=%d)", err, nodes)
	}
}

func TestMCPDiagnosticBufferIsBoundedAndKeepsTail(t *testing.T) {
	var buf boundedDiagnosticBuffer
	prefix := strings.Repeat("old-logs-", maxMCPDiagnosticBytes)
	tail := "FINAL-DIAGNOSTIC"
	if n, err := buf.Write([]byte(prefix + tail)); err != nil || n != len(prefix)+len(tail) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got := buf.String()
	if !strings.Contains(got, "truncated") || !strings.HasSuffix(got, tail) {
		t.Fatalf("bounded diagnostics lost truncation marker or tail: len=%d suffix=%q", len(got), got[len(got)-len(tail):])
	}
	if len(got) > maxMCPDiagnosticBytes+128 {
		t.Fatalf("diagnostic buffer grew past bound: %d", len(got))
	}
}

func TestMCPDiagnosticBufferSupportsConcurrentWriteAndRead(t *testing.T) {
	var buf boundedDiagnosticBuffer
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_, _ = buf.Write([]byte("concurrent diagnostic line\n"))
				_ = buf.String()
			}
		}()
	}
	wg.Wait()
	if got := buf.String(); len(got) == 0 || len(got) > maxMCPDiagnosticBytes+128 {
		t.Fatalf("unexpected concurrent buffer size: %d", len(got))
	}
}

func TestMCPDiscoveryRejectsRepeatedPaginationCursor(t *testing.T) {
	cfg := testMCPConfig(t)
	cfg.Env["GO_MCP_PAGINATION_LOOP"] = "1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "pagination cursor repeated") {
		t.Fatalf("expected repeated-cursor rejection, got client=%v err=%v", client != nil, err)
	}
}

func TestMCPDiscoveryEnforcesAggregateDeclarationBudget(t *testing.T) {
	cfg := testMCPConfig(t)
	cfg.Env["GO_MCP_LARGE_DECL_PAGES"] = "1"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "4 MB total limit") {
		t.Fatalf("expected aggregate declaration budget error, got client=%v err=%v", client != nil, err)
	}
}

func TestMCPClientDiscoversAndCallsTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatalf("connectMCP returned error: %v", err)
	}
	defer client.Close()
	if len(client.tools) != 1 || client.tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %#v", client.tools)
	}

	tool := &mcpTool{
		client:     client,
		remoteName: "echo",
		name:       mcpToolName("local", "echo"),
		desc:       "Echo text",
		schema:     schemaFromMCP(client.tools[0].InputSchema),
	}
	result, err := tool.Execute(ctx, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success || result.Content != "echo: hello" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestMCPAppNegotiationVisibilityResourceAndToolPayload(t *testing.T) {
	cfg := testMCPConfig(t)
	cfg.Env["GO_MCP_APP"] = "1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatalf("connect MCP App server: %v", err)
	}
	defer client.Close()
	// refresh_ui is app-only and must never enter GLM/Kimi declarations.
	if len(client.tools) != 1 || client.tools[0].Name != "echo" {
		t.Fatalf("model-visible MCP tools = %#v", client.tools)
	}
	if len(client.appTools) != 2 || client.appTools["echo"].Name != "echo" ||
		client.appTools["refresh_ui"].Name != "refresh_ui" {
		t.Fatalf("app-visible MCP tools = %#v", client.appTools)
	}
	resourceURI, err := mcpAppResourceURI(client.tools[0])
	if err != nil || resourceURI != "ui://test/dashboard" {
		t.Fatalf("resource URI = %q, err=%v", resourceURI, err)
	}
	tool := &mcpTool{
		client: client, remoteName: "echo", name: mcpToolName("local", "echo"),
		desc: "Echo text", schema: schemaFromMCP(client.tools[0].InputSchema),
		appResourceURI: resourceURI,
	}
	result, err := tool.Execute(ctx, map[string]any{"text": "hello app"})
	if err != nil || !result.Success {
		t.Fatalf("MCP App tool result=%#v err=%v", result, err)
	}
	app, ok := result.Data.(*MCPAppPayload)
	if !ok {
		t.Fatalf("MCP App payload type = %T", result.Data)
	}
	if app.ResourceURI != resourceURI || !strings.Contains(app.HTML, "MCP App") ||
		!app.PrefersBorder || app.ToolResult["structuredContent"] == nil {
		t.Fatalf("MCP App payload = %#v", app)
	}
	if len(app.CSP.ConnectDomains) != 2 || len(app.CSP.ResourceDomains) != 1 {
		t.Fatalf("MCP App CSP = %#v", app.CSP)
	}
}

func TestMCPAppToolCallIsSameServerApprovedAndSchemaValidated(t *testing.T) {
	withTempConfigDir(t)
	studio := newStudioForTest(t)
	projectInfo := addTestProject(t, studio, "MCP App calls")

	cfg := testMCPConfig(t)
	cfg.Env["GO_MCP_APP"] = "1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	client.onClose = func() {
		studio.ensureMCPAppRegistry().unregisterClient(client)
	}
	defer client.Close()

	resourceURI, err := mcpAppResourceURI(client.tools[0])
	if err != nil {
		t.Fatal(err)
	}
	tool := &mcpTool{
		client: client, studio: studio, projectID: projectInfo.ID,
		remoteName: "echo", name: mcpToolName("local", "echo"),
		desc: "Echo text", schema: schemaFromMCP(client.tools[0].InputSchema),
		appResourceURI: resourceURI,
	}
	toolCtx := withAskUserRouting(ctx, projectInfo.ID, "default")
	parentResult, err := tool.Execute(toolCtx, map[string]any{"text": "initial"})
	if err != nil || !parentResult.Success {
		t.Fatalf("parent MCP App call = %#v, %v", parentResult, err)
	}
	app := parentResult.Data.(*MCPAppPayload)
	if _, err := uuid.Parse(app.InstanceID); err != nil {
		t.Fatalf("MCP App instance ID = %q, %v", app.InstanceID, err)
	}

	var approvals atomic.Int32
	studio.testMCPAppApproval = func(
		_ context.Context, projectID, sessionID, connector, toolName string, args map[string]any,
	) (bool, error) {
		approvals.Add(1)
		if projectID != projectInfo.ID || sessionID != "default" ||
			connector != "local" || toolName != "refresh_ui" {
			t.Fatalf("approval routing = %q/%q %q/%q", projectID, sessionID, connector, toolName)
		}
		return true, nil
	}
	result, err := studio.CallMCPAppTool(app.InstanceID, "refresh_ui", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result["structuredContent"] == nil || approvals.Load() != 1 {
		t.Fatalf("app tool result=%#v approvals=%d", result, approvals.Load())
	}

	// echo is visible to both model and app, but its required text argument is
	// validated before the user sees an approval card.
	if _, err := studio.CallMCPAppTool(app.InstanceID, "echo", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("missing required argument error = %v", err)
	}
	if approvals.Load() != 1 {
		t.Fatalf("invalid app call reached approval: %d", approvals.Load())
	}
	if _, err := studio.CallMCPAppTool(app.InstanceID, "not_on_this_server", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "not visible") {
		t.Fatalf("cross/unknown tool error = %v", err)
	}

	client.Close()
	if _, err := studio.CallMCPAppTool(app.InstanceID, "refresh_ui", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("closed-client app session error = %v", err)
	}
}

func TestMCPAppToolCallDenialAndArgumentRedaction(t *testing.T) {
	registry := newMCPAppRegistry()
	client := &mcpClient{
		cfg: MCPServerConfig{Name: "sensitive"},
		appTools: map[string]mcpRemoteTool{
			"save": {Name: "save", InputSchema: map[string]any{"type": "object"}},
		},
	}
	instanceID := registry.register("p", "default", "ui://safe/app", client)
	instance, err := registry.beginCall(instanceID)
	if err != nil {
		t.Fatal(err)
	}
	details := approvalDetailsText(mcpAppApprovalDetails(instance, "save", map[string]any{
		"title": "Draft", "api_key": "sk-super-secret-value",
		"nested": map[string]any{"password": "hunter2"},
	}))
	registry.finishCall(instanceID)
	if strings.Contains(details, "sk-super-secret-value") || strings.Contains(details, "hunter2") ||
		!strings.Contains(details, "<redacted>") {
		t.Fatalf("approval details did not redact secrets: %q", details)
	}

	withTempConfigDir(t)
	studio := newStudioForTest(t)
	projectInfo := addTestProject(t, studio, "Denied app")
	studio.mcpApps = registry
	registry.mu.Lock()
	registry.instances[instanceID].projectID = projectInfo.ID
	registry.mu.Unlock()
	studio.testMCPAppApproval = func(context.Context, string, string, string, string, map[string]any) (bool, error) {
		return false, nil
	}
	if _, err := studio.CallMCPAppTool(instanceID, "save", map[string]any{"title": "Draft"}); err == nil ||
		!strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied app call error = %v", err)
	}
}

func TestMCPAppClientCloseCancelsInflightSession(t *testing.T) {
	registry := newMCPAppRegistry()
	client := &mcpClient{cfg: MCPServerConfig{Name: "closing"}}
	instanceID := registry.register("p", "default", "ui://safe/app", client)
	if _, err := registry.beginCall(instanceID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !registry.attachCallCancel(instanceID, cancel) {
		t.Fatal("failed to attach in-flight cancellation")
	}
	registry.unregisterClient(client)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("closing MCP client did not cancel its in-flight app action")
	}
	if _, err := registry.beginCall(instanceID); err == nil {
		t.Fatal("closed MCP client left its app session reusable")
	}
}

func TestMCPAppRejectsCSPInjectionAndInvalidMetadata(t *testing.T) {
	for _, origin := range []string{
		"https://safe.example; script-src *",
		"https://safe.example/path",
		"http://insecure.example",
		"https://user:pass@example.com",
	} {
		if _, err := validateMCPAppDomains([]string{origin}, true); err == nil {
			t.Fatalf("accepted unsafe MCP App origin %q", origin)
		}
	}
	if _, err := validateMCPAppDomains([]string{"https://api.example.com", "wss://stream.example.com"}, true); err != nil {
		t.Fatalf("rejected safe MCP App origins: %v", err)
	}
	remote := mcpRemoteTool{
		Name: "hidden", InputSchema: map[string]any{"type": "object"},
		Meta: map[string]any{"ui": map[string]any{
			"resourceUri": "https://not-ui.example/app",
			"visibility":  []any{"model", "admin"},
		}},
	}
	if _, err := mcpToolVisibleToModel(remote); err == nil {
		t.Fatal("accepted unknown MCP App visibility")
	}
	if _, err := mcpAppResourceURI(remote); err == nil {
		t.Fatal("accepted non-ui MCP App resource URI")
	}
}

func TestMCPCompletedRequestsCannotBeKilledByLateContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tool := &mcpTool{
		client: client, remoteName: "echo", name: mcpToolName("local", "echo"),
		desc: "Echo text", schema: schemaFromMCP(client.tools[0].InputSchema),
	}
	for i := 0; i < 100; i++ {
		callCtx, cancelCall := context.WithCancel(context.Background())
		result, err := tool.Execute(callCtx, map[string]any{"text": fmt.Sprintf("call-%d", i)})
		cancelCall() // mirrors a caller's defer cancel immediately after success
		runtime.Gosched()
		if err != nil || !result.Success || result.Content != fmt.Sprintf("echo: call-%d", i) {
			t.Fatalf("request %d lost healthy transport after late cancel: result=%#v err=%v", i, result, err)
		}
	}
}

func TestMCPToolPreservesNonTextContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatalf("connectMCP returned error: %v", err)
	}
	defer client.Close()
	tool := &mcpTool{
		client:     client,
		remoteName: "echo",
		name:       mcpToolName("local", "echo"),
		desc:       "Echo text",
		schema:     schemaFromMCP(client.tools[0].InputSchema),
	}
	result, err := tool.Execute(ctx, map[string]any{"text": "mixed"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success || !strings.Contains(result.Content, "echo: mixed") || !strings.Contains(result.Content, `"type": "image"`) {
		t.Fatalf("non-text content was not preserved: %#v", result)
	}
}

func TestMCPToolBoundsOversizedMultibyteResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tool := &mcpTool{
		client: client, remoteName: "echo", name: mcpToolName("local", "echo"),
		desc: "Echo text", schema: schemaFromMCP(client.tools[0].InputSchema),
	}
	result, err := tool.Execute(ctx, map[string]any{"text": "oversized"})
	if err != nil || !result.Success {
		t.Fatalf("oversized tool call failed: result=%#v err=%v", result, err)
	}
	if !utf8.ValidString(result.Content) || !strings.Contains(result.Content, "OUTPUT TRUNCATED") {
		t.Fatalf("result was not rune-safely bounded: bytes=%d", len(result.Content))
	}
	if len([]rune(result.Content)) > config.DefaultToolResultMaxChars+300 {
		t.Fatalf("bounded MCP result is still too large: %d runes", len([]rune(result.Content)))
	}
	if result.Data != nil {
		t.Fatalf("MCP structured data should not duplicate bounded content: %#v", result.Data)
	}
}

func TestMCPToolHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatalf("connectMCP returned error: %v", err)
	}
	defer client.Close()

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	var transportBroken atomic.Bool
	tool := &mcpTool{
		client:           client,
		remoteName:       "echo",
		name:             mcpToolName("local", "echo"),
		desc:             "Echo text",
		schema:           schemaFromMCP(client.tools[0].InputSchema),
		onTransportError: func() { transportBroken.Store(true) },
	}
	result, err := tool.Execute(canceled, map[string]any{"text": "hello"})
	if err == nil || result.Success {
		t.Fatalf("expected canceled tool call error, got result=%#v err=%v", result, err)
	}
	if !transportBroken.Load() {
		t.Fatal("canceled MCP transport was not marked for next-turn recovery")
	}
}

func TestMCPRemoteToolErrorDoesNotInvalidateHealthyTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectMCP(ctx, testMCPConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var transportBroken atomic.Bool
	tool := &mcpTool{
		client: client, remoteName: "echo", name: mcpToolName("local", "echo"),
		desc: "Echo text", schema: schemaFromMCP(client.tools[0].InputSchema),
		onTransportError: func() { transportBroken.Store(true) },
	}
	result, err := tool.Execute(ctx, map[string]any{"text": "rpc-error"})
	if err == nil || result.Success || !strings.Contains(err.Error(), "remote tool rejected input") {
		t.Fatalf("expected remote application error, got result=%#v err=%v", result, err)
	}
	if transportBroken.Load() {
		t.Fatal("remote application error incorrectly invalidated MCP transport")
	}
	result, err = tool.Execute(ctx, map[string]any{"text": "still-alive"})
	if err != nil || !result.Success || result.Content != "echo: still-alive" {
		t.Fatalf("transport was not reusable after remote error: result=%#v err=%v", result, err)
	}
}

func TestBrokenMCPTransportForcesProjectClientRebuild(t *testing.T) {
	p := &Project{ID: "p", Directory: t.TempDir(), client: &mockClient{}}
	provider := p.client.(*mockClient)
	p.mcpTransportBroken.Store(true)
	err := p.initClient(Settings{DefaultProvider: "glm", DefaultModel: "glm-5.1"})
	if err == nil {
		t.Fatal("expected rebuild to reach missing-key provider initialization error")
	}
	provider.mu.Lock()
	closeCalls := provider.closeCalls
	provider.mu.Unlock()
	if closeCalls != 1 || p.client != nil || p.mcpTransportBroken.Load() {
		t.Fatalf("broken transport did not reset cached client: close=%d retained=%v broken=%v", closeCalls, p.client != nil, p.mcpTransportBroken.Load())
	}
}

func TestMCPConfigSaveListRemove(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)

	cfg := testMCPConfig(t)
	if err := s.SaveMCPServer(cfg); err != nil {
		t.Fatalf("SaveMCPServer returned error: %v", err)
	}
	list, err := s.ListMCPServers()
	if err != nil {
		t.Fatalf("ListMCPServers returned error: %v", err)
	}
	if len(list) != 1 || list[0].Name != "local" || !list[0].Enabled {
		t.Fatalf("unexpected MCP server list: %#v", list)
	}
	if list[0].Env["GO_WANT_MCP_HELPER"] != "1" {
		t.Fatalf("expected env to persist, got %#v", list[0].Env)
	}
	if err := s.RemoveMCPServer("local"); err != nil {
		t.Fatalf("RemoveMCPServer returned error: %v", err)
	}
	list, err = s.ListMCPServers()
	if err != nil {
		t.Fatalf("ListMCPServers after remove returned error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty MCP server list, got %#v", list)
	}
}

func TestMCPConfigRejectsInvalidEnv(t *testing.T) {
	cfg := testMCPConfig(t)
	cfg.Env = map[string]string{"BAD=KEY": "value"}
	if _, err := validateMCPConfig(cfg); err == nil {
		t.Fatal("expected invalid env key error")
	}
	cfg.Env = map[string]string{"BAD KEY": "value"}
	if _, err := validateMCPConfig(cfg); err == nil {
		t.Fatal("expected non-portable env key error")
	}
}

func TestMCPConfigEnforcesResourceLimits(t *testing.T) {
	cfg := testMCPConfig(t)
	cfg.Args = make([]string, maxMCPArgs+1)
	if _, err := validateMCPConfig(cfg); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("expected argument-count error, got %v", err)
	}
	cfg = testMCPConfig(t)
	cfg.Env = make(map[string]string, maxMCPEnvVars+1)
	for i := 0; i <= maxMCPEnvVars; i++ {
		cfg.Env[fmt.Sprintf("KEY_%d", i)] = "value"
	}
	if _, err := validateMCPConfig(cfg); err == nil || !strings.Contains(err.Error(), "environment variables") {
		t.Fatalf("expected environment-count error, got %v", err)
	}
	cfg = testMCPConfig(t)
	cfg.Env = map[string]string{"TOKEN": strings.Repeat("x", maxMCPEnvValueBytes+1)}
	if _, err := validateMCPConfig(cfg); err == nil {
		t.Fatal("expected environment-value size error")
	}
}

func TestLoadMCPConfigRejectsOversizeDuplicatesAndInvalidEntries(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(mcpConfigPath(), []byte(strings.Repeat(" ", maxMCPConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPServers(); err == nil || !strings.Contains(err.Error(), "1 MB") {
		t.Fatalf("expected oversized config error, got %v", err)
	}

	duplicates := []MCPServerConfig{
		{Name: "Docs", Command: "server", Enabled: true},
		{Name: " docs ", Command: "other", Enabled: true},
	}
	data, err := json.Marshal(duplicates)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpConfigPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPServers(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}

	invalid, err := json.Marshal([]MCPServerConfig{{Name: "bad", Command: "server", Env: map[string]string{"BAD KEY": "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpConfigPath(), invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMCPServers(); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("expected restored invalid-entry error, got %v", err)
	}
}

func TestMCPSettingsCanListAndRepairInvalidRestoredEntries(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	raw := []MCPServerConfig{
		{Name: " broken ", Command: "server", Env: map[string]string{"BAD KEY": "x"}, Enabled: true},
		{Name: "healthy", Command: "server", Enabled: true},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpConfigPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	statuses, err := s.ListMCPServers()
	if err != nil {
		t.Fatalf("recovery listing failed: %v", err)
	}
	if len(statuses) != 2 || statuses[0].Error == "" || statuses[0].Enabled {
		t.Fatalf("invalid entry was not safely surfaced: %+v", statuses)
	}
	if statuses[1].Error != "" || !statuses[1].Enabled {
		t.Fatalf("healthy entry was incorrectly disabled: %+v", statuses[1])
	}

	if err := s.SaveMCPServer(MCPServerConfig{Name: "broken", Command: "fixed-server", Enabled: true}); err != nil {
		t.Fatalf("could not replace invalid entry: %v", err)
	}
	configs, err := loadMCPServers()
	if err != nil || len(configs) != 2 {
		t.Fatalf("repaired config remains invalid: configs=%+v err=%v", configs, err)
	}
}

func TestMCPSettingsCanRemoveInvalidAndDuplicateEntries(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	raw := []MCPServerConfig{
		{Name: "duplicate", Command: "one", Enabled: true},
		{Name: " duplicate ", Command: "two", Enabled: true},
		{Name: "bad-env", Command: "server", Env: map[string]string{"BAD KEY": "x"}, Enabled: true},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpConfigPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.ListMCPServers()
	if err != nil || len(statuses) != 3 || !strings.Contains(statuses[0].Error, "duplicate") {
		t.Fatalf("duplicate recovery statuses = %+v, %v", statuses, err)
	}
	if err := s.RemoveMCPServer("duplicate"); err != nil {
		t.Fatalf("remove duplicate entries: %v", err)
	}
	if err := s.RemoveMCPServer("bad-env"); err != nil {
		t.Fatalf("remove invalid entry: %v", err)
	}
	configs, err := loadMCPServers()
	if err != nil || len(configs) != 0 {
		t.Fatalf("config was not recoverable after removals: %+v, %v", configs, err)
	}
}

func TestMCPConfigRejectsTooManyServers(t *testing.T) {
	configs := make([]MCPServerConfig, maxMCPServers+1)
	for i := range configs {
		configs[i] = MCPServerConfig{Name: fmt.Sprintf("server-%d", i), Command: "server"}
	}
	if _, err := validateMCPConfigs(configs); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected server-count error, got %v", err)
	}
}

func TestConcurrentMCPConfigSavesDoNotLoseUpdates(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	const count = 12
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.SaveMCPServer(MCPServerConfig{
				Name: fmt.Sprintf("parallel-%02d", i), Command: "server", Enabled: true,
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel save failed: %v", err)
		}
	}
	configs, err := loadMCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != count {
		t.Fatalf("lost concurrent config updates: got %d, want %d", len(configs), count)
	}
}

func TestProjectRegistersMCPTools(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	if err := s.SaveMCPServer(testMCPConfig(t)); err != nil {
		t.Fatalf("SaveMCPServer returned error: %v", err)
	}

	p := &Project{
		ID:        "p",
		Name:      "p",
		Directory: t.TempDir(),
		studio:    s,
	}
	reg := tools.DefaultRegistry(p.Directory)
	p.registerMCPTools(context.Background(), reg)
	defer p.resetClientLocked()

	name := mcpToolName("local", "echo")
	if _, ok := reg.Get(name); !ok {
		t.Fatalf("expected %s to be registered", name)
	}
}

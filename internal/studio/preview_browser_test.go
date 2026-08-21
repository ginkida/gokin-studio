package studio

import (
	"context"
	"strings"
	"testing"
)

func TestPreviewBrowserToolRoutesSessionAndReturnsVisionEvidence(t *testing.T) {
	s := NewStudio()
	s.ctx = context.Background()
	s.previewServers[previewServerKey("project", "chat", "web")] = &previewServerRun{
		projectID: "project", sessionID: "chat", config: PreviewServerConfiguration{Name: "web"},
		state: "running", bridgeToken: "bridge-token",
	}
	s.testPreviewBrowserEmitter = func(event map[string]any) {
		if event["projectID"] != "project" || event["sessionID"] != "chat" || event["configuration"] != "web" || event["bridgeToken"] != "bridge-token" {
			t.Fatalf("misrouted preview event: %#v", event)
		}
		requestID, _ := event["requestID"].(string)
		payload := `{"title":"Fixture","controls":[],"issues":[],"screenshotDataURL":"data:image/png;base64,aGVsbG8="}`
		if err := s.ResolvePreviewBrowserRequest(requestID, payload); err != nil {
			t.Fatal(err)
		}
	}
	tool := &previewBrowserTool{studio: s, attachVision: true}
	ctx := withAskUserRouting(context.Background(), "project", "chat")
	result, err := tool.Execute(ctx, map[string]any{"action": "inspect", "screenshot": true})
	if err != nil || !result.Success || !strings.Contains(result.Content, "Fixture") {
		t.Fatalf("preview result = %+v, %v", result, err)
	}
	if strings.Contains(result.Content, "screenshotDataURL") || len(result.MultimodalParts) != 1 || string(result.MultimodalParts[0].Data) != "hello" {
		t.Fatalf("vision evidence was not separated safely: %+v", result)
	}
}

func TestPreviewBrowserRegistrySerializesEachExactFrame(t *testing.T) {
	registry := newPreviewBrowserRegistry()
	first := &previewBrowserPending{projectID: "project", sessionID: "chat", configuration: "web", bridgeToken: "token-one"}
	if _, err := registry.register("first", first); err != nil {
		t.Fatalf("register first frame action: %v", err)
	}
	if _, err := registry.register("parallel", &previewBrowserPending{projectID: "project", sessionID: "chat", configuration: "web", bridgeToken: "token-one"}); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("same-frame parallel action was not rejected: %v", err)
	}
	if _, err := registry.register("replacement", &previewBrowserPending{projectID: "project", sessionID: "chat", configuration: "web", bridgeToken: "token-two"}); err != nil {
		t.Fatalf("replacement frame action rejected: %v", err)
	}
	if _, err := registry.register("other-chat", &previewBrowserPending{projectID: "project", sessionID: "other", configuration: "web", bridgeToken: "token-one"}); err != nil {
		t.Fatalf("independent chat action rejected: %v", err)
	}
	if _, err := registry.register("first", &previewBrowserPending{projectID: "other-project", sessionID: "chat", configuration: "web", bridgeToken: "token"}); err == nil || !strings.Contains(err.Error(), "identity already exists") {
		t.Fatalf("duplicate request identity was not rejected: %v", err)
	}
	registry.cleanup("first", first)
	if _, err := registry.register("after-cleanup", &previewBrowserPending{projectID: "project", sessionID: "chat", configuration: "web", bridgeToken: "token-one"}); err != nil {
		t.Fatalf("frame remained locked after cleanup: %v", err)
	}
	old := &previewBrowserPending{projectID: "old", sessionID: "chat", configuration: "web", bridgeToken: "old-token"}
	if _, err := registry.register("reused", old); err != nil {
		t.Fatalf("register old identity owner: %v", err)
	}
	if !registry.resolve("reused", `{"error":"done"}`) {
		t.Fatal("resolve old identity owner")
	}
	replacement := &previewBrowserPending{projectID: "new", sessionID: "chat", configuration: "web", bridgeToken: "new-token"}
	if _, err := registry.register("reused", replacement); err != nil {
		t.Fatalf("reuse released identity: %v", err)
	}
	registry.cleanup("reused", old)
	if _, err := registry.register("probe", &previewBrowserPending{projectID: replacement.projectID, sessionID: replacement.sessionID, configuration: replacement.configuration, bridgeToken: replacement.bridgeToken}); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("stale cleanup removed the replacement owner: %v", err)
	}
}

func TestPreviewBrowserToolPrefersVisibleStaticFileOverRunningDevServer(t *testing.T) {
	s := NewStudio()
	s.ctx = context.Background()
	s.previewServers[previewServerKey("project", "chat", "web")] = &previewServerRun{
		projectID: "project", sessionID: "chat", config: PreviewServerConfiguration{Name: "web"},
		state: "running", bridgeToken: "dev-token",
	}
	s.previewServers[previewServerKey("project", "chat", staticPreviewConfiguration)] = &previewServerRun{
		projectID: "project", sessionID: "chat", config: PreviewServerConfiguration{Name: staticPreviewConfiguration},
		state: "running", bridgeToken: "static-token", staticPath: "reports/result.html",
	}
	s.testPreviewBrowserEmitter = func(event map[string]any) {
		if event["configuration"] != staticPreviewConfiguration || event["bridgeToken"] != "static-token" {
			t.Fatalf("preview_browser ignored the visible static file: %#v", event)
		}
		requestID, _ := event["requestID"].(string)
		if err := s.ResolvePreviewBrowserRequest(requestID, `{"title":"Static file","controls":[],"issues":[]}`); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&previewBrowserTool{studio: s}).Execute(
		withAskUserRouting(context.Background(), "project", "chat"),
		map[string]any{"action": "inspect"},
	)
	if err != nil || !result.Success || !strings.Contains(result.Content, "Static file") {
		t.Fatalf("static preview result = %+v, %v", result, err)
	}
}

func TestPreviewBrowserToolValidatesConstrainedActions(t *testing.T) {
	tool := &previewBrowserTool{}
	invalid := []map[string]any{
		{"action": "navigate", "url": "https://example.com"},
		{"action": "click", "x": 1},
		{"action": "click", "x": -1, "y": 2},
		{"action": "click", "x": 1, "y": 10001},
		{"action": "fill", "x": 1, "y": 2, "text": ""},
		{"action": "fill", "x": 10001, "y": 2, "text": "value"},
		{"action": "scroll", "deltaY": 9000},
		{"action": "key", "key": "CMD+Q"},
	}
	for _, args := range invalid {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("accepted unsafe/invalid action: %#v", args)
		}
	}
	for _, args := range []map[string]any{{"action": "inspect"}, {"action": "click", "x": 4, "y": 8}, {"action": "key", "key": "ENTER"}} {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("rejected valid action %#v: %v", args, err)
		}
	}
}

func TestResolvePreviewBrowserRequestRejectsNullPayload(t *testing.T) {
	s := NewStudio()
	pending := &previewBrowserPending{projectID: "project", sessionID: "chat", configuration: "web", bridgeToken: "token"}
	if _, err := s.previewBrowser.register("request", pending); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolvePreviewBrowserRequest("request", `null`); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("null payload was accepted: %v", err)
	}
	if !s.previewBrowser.resolve("request", `{"error":"cleanup"}`) {
		t.Fatal("invalid payload consumed the pending request")
	}
}

func TestSessionPreviewAutoVerifyRunningIsScoped(t *testing.T) {
	s := NewStudio()
	s.previewServers[previewServerKey("project", "chat", "web")] = &previewServerRun{
		projectID: "project", sessionID: "chat", state: "running", autoVerify: true,
	}
	s.previewServers[previewServerKey("project", "other", "web")] = &previewServerRun{
		projectID: "project", sessionID: "other", state: "stopped", autoVerify: true,
	}
	s.previewServers[previewServerKey("other-project", "chat", "web")] = &previewServerRun{
		projectID: "other-project", sessionID: "chat", state: "running", autoVerify: false,
	}
	if !s.sessionPreviewAutoVerifyRunning("project", "chat") {
		t.Fatal("expected matching running autoVerify preview")
	}
	if s.sessionPreviewAutoVerifyRunning("project", "other") || s.sessionPreviewAutoVerifyRunning("other-project", "chat") {
		t.Fatal("autoVerify preview leaked across session or project scope")
	}
}

func TestPreviewBrowserToolRejectsBridgeErrorAsVerification(t *testing.T) {
	s := NewStudio()
	s.ctx = context.Background()
	s.previewServers[previewServerKey("project", "chat", "web")] = &previewServerRun{
		projectID: "project", sessionID: "chat", config: PreviewServerConfiguration{Name: "web"},
		state: "running", bridgeToken: "bridge-token",
	}
	s.testPreviewBrowserEmitter = func(event map[string]any) {
		requestID, _ := event["requestID"].(string)
		if err := s.ResolvePreviewBrowserRequest(requestID, `{"error":"preview changed"}`); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&previewBrowserTool{studio: s}).Execute(
		withAskUserRouting(context.Background(), "project", "chat"),
		map[string]any{"action": "inspect"},
	)
	if err != nil || result.Success || !strings.Contains(result.Error, "preview changed") {
		t.Fatalf("bridge error was accepted as verification: %+v, %v", result, err)
	}
}

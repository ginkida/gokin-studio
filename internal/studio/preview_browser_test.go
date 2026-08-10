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
		{"action": "fill", "x": 1, "y": 2, "text": ""},
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

package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func addExternalBrowserAgentTab(t *testing.T, s *Studio, projectID, sessionID, tabID, rawURL string) *externalBrowserRun {
	t.Helper()
	target, origin, err := normalizeExternalBrowserURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	run := &externalBrowserRun{
		id: tabID, projectID: projectID, sessionID: sessionID, target: target, origin: origin,
		localBase: "http://127.0.0.1:43123", browserURL: "http://127.0.0.1:43123/page", bridgeToken: "bridge-token-for-" + tabID,
		title: "Fixture", state: "running", createdAt: time.Now().UnixMilli(),
	}
	s.externalBrowserMu.Lock()
	s.externalBrowserTabs[tabID] = run
	s.externalBrowserMu.Unlock()
	return run
}

func TestExternalBrowserAgentValidationIsBounded(t *testing.T) {
	tool := &externalBrowserAgentTool{}
	valid := []map[string]any{
		{"action": "list"},
		{"action": "inspect", "tab_id": "tab"},
		{"action": "click", "tab_id": "tab", "expected_url": "https://example.com/", "x": 10, "y": 20},
		{"action": "fill", "tab_id": "tab", "expected_url": "https://example.com/", "x": 10, "y": 20, "text": "hello"},
		{"action": "scroll", "tab_id": "tab", "expected_url": "https://example.com/", "deltaY": -2000},
		{"action": "key", "tab_id": "tab", "expected_url": "https://example.com/", "key": "ENTER"},
	}
	for _, args := range valid {
		if err := tool.Validate(args); err != nil {
			t.Errorf("valid args %#v rejected: %v", args, err)
		}
	}
	invalid := []map[string]any{
		{"action": "navigate", "tab_id": "tab"},
		{"action": "inspect"},
		{"action": "click", "tab_id": "tab", "expected_url": "https://example.com/", "x": -1, "y": 20},
		{"action": "fill", "tab_id": "tab", "expected_url": "https://example.com/", "x": 1, "y": 2, "text": strings.Repeat("x", 4001)},
		{"action": "scroll", "tab_id": "tab", "expected_url": "https://example.com/", "deltaY": 2001},
		{"action": "key", "tab_id": "tab", "expected_url": "https://example.com/", "key": "Meta+L"},
	}
	for _, args := range invalid {
		if err := tool.Validate(args); err == nil {
			t.Errorf("invalid args accepted: %#v", args)
		}
	}
}

func TestExternalBrowserAgentListExcludesProxySecrets(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Browser list")
	addExternalBrowserAgentTab(t, s, project.ID, "default", "tab-list", "https://example.com/docs")
	if err := s.SetActiveExternalBrowserTab(project.ID, "default", "tab-list"); err != nil {
		t.Fatal(err)
	}
	tool := &externalBrowserAgentTool{studio: s}
	result, err := tool.Execute(withAskUserRouting(context.Background(), project.ID, "default"), map[string]any{"action": "list"})
	if err != nil || !result.Success {
		t.Fatalf("list result = %+v, %v", result, err)
	}
	if strings.Contains(result.Content, "bridge-token") || strings.Contains(result.Content, "127.0.0.1") || strings.Contains(result.Content, "browserURL") {
		t.Fatalf("list leaked proxy credentials: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"active": true`) || !strings.Contains(result.Content, "https://example.com/docs") {
		t.Fatalf("list omitted safe active-tab metadata: %s", result.Content)
	}
}

func TestExternalBrowserAgentRequiresActiveFreshTabAndTokenBoundResponse(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Browser action")
	run := addExternalBrowserAgentTab(t, s, project.ID, "default", "tab-action", "https://example.com/form")
	tool := &externalBrowserAgentTool{studio: s, attachVision: true}
	ctx := withAskUserRouting(context.Background(), project.ID, "default")
	if err := s.SetActiveExternalBrowserTab(project.ID, "default", run.id); err != nil {
		t.Fatal(err)
	}
	emitted := false
	s.testExternalBrowserAgentEmitter = func(event map[string]any) { emitted = true }
	stale, err := tool.Execute(ctx, map[string]any{"action": "click", "tab_id": run.id, "expected_url": "https://example.com/old", "x": 1, "y": 2})
	if err != nil || stale.Success || emitted || !strings.Contains(stale.Error, "changed after inspection") {
		t.Fatalf("stale action = %+v, %v, emitted=%v", stale, err, emitted)
	}
	png := base64.StdEncoding.EncodeToString([]byte("small-png-fixture"))
	s.testExternalBrowserAgentEmitter = func(event map[string]any) {
		requestID := event["requestID"].(string)
		if err := s.ResolveExternalBrowserAgentRequest(requestID, run.id, "wrong-token-that-is-long-enough", `{}`); err == nil {
			t.Error("wrong bridge token resolved request")
		}
		payload, _ := json.Marshal(map[string]any{
			"url": "https://example.com/form", "title": "Form", "text": "Visible page", "capturedAt": time.Now().UnixMilli(),
			"controls": []any{}, "headings": []any{}, "issues": []any{}, "screenshotDataURL": "data:image/png;base64," + png,
		})
		if err := s.ResolveExternalBrowserAgentRequest(requestID, run.id, run.bridgeToken, string(payload)); err != nil {
			t.Errorf("valid response rejected after bad token: %v", err)
		}
	}
	result, err := tool.Execute(ctx, map[string]any{"action": "inspect", "tab_id": run.id})
	if err != nil || !result.Success || len(result.MultimodalParts) != 1 {
		t.Fatalf("inspect result = %+v, %v", result, err)
	}
	if strings.Contains(result.Content, "screenshotDataURL") || !strings.Contains(result.Content, "Visible page") {
		t.Fatalf("inspect result did not separate vision data: %s", result.Content)
	}
}

func TestExternalBrowserAgentRegistrySerializesEachExactTab(t *testing.T) {
	registry := newExternalBrowserAgentRegistry()
	first := &externalBrowserAgentPending{projectID: "project", sessionID: "session", tabID: "tab", bridgeToken: "first-token", origin: "https://example.com"}
	if _, err := registry.register("first", first); err != nil {
		t.Fatalf("register first action: %v", err)
	}
	if _, err := registry.register("duplicate-id", &externalBrowserAgentPending{projectID: "project", sessionID: "session", tabID: "tab"}); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("same-tab parallel action was not rejected: %v", err)
	}
	if _, err := registry.register("other-tab", &externalBrowserAgentPending{projectID: "project", sessionID: "session", tabID: "other"}); err != nil {
		t.Fatalf("independent tab action rejected: %v", err)
	}
	if _, err := registry.register("first", &externalBrowserAgentPending{projectID: "other-project", sessionID: "session", tabID: "tab"}); err == nil || !strings.Contains(err.Error(), "identity already exists") {
		t.Fatalf("duplicate request identity was not rejected: %v", err)
	}
	registry.cleanup("first", first)
	if _, err := registry.register("after-cleanup", &externalBrowserAgentPending{projectID: "project", sessionID: "session", tabID: "tab"}); err != nil {
		t.Fatalf("tab remained locked after cleanup: %v", err)
	}
	old := &externalBrowserAgentPending{projectID: "old-project", sessionID: "session", tabID: "old-tab", bridgeToken: "old-token", origin: "https://old.example"}
	if _, err := registry.register("reused", old); err != nil {
		t.Fatalf("register old identity owner: %v", err)
	}
	if err := registry.resolve("reused", old.tabID, old.bridgeToken, `{"error":"done"}`); err != nil {
		t.Fatalf("resolve old identity owner: %v", err)
	}
	replacement := &externalBrowserAgentPending{projectID: "new-project", sessionID: "session", tabID: "new-tab"}
	if _, err := registry.register("reused", replacement); err != nil {
		t.Fatalf("reuse released identity: %v", err)
	}
	registry.cleanup("reused", old)
	if _, err := registry.register("probe", &externalBrowserAgentPending{projectID: replacement.projectID, sessionID: replacement.sessionID, tabID: replacement.tabID}); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("stale cleanup removed the replacement owner: %v", err)
	}
}

func TestExternalBrowserTabStateRejectsWrongTokenAndOrigin(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Browser state")
	run := addExternalBrowserAgentTab(t, s, project.ID, "default", "tab-state", "https://example.com/start")
	if err := s.UpdateExternalBrowserTabState(project.ID, "default", run.id, "wrong-token-that-is-long-enough", "https://example.com/app", "App"); err == nil {
		t.Fatal("wrong token updated browser state")
	}
	if err := s.UpdateExternalBrowserTabState(project.ID, "default", run.id, run.bridgeToken, "https://other.example/app", "App"); err == nil {
		t.Fatal("cross-origin bridge update accepted")
	}
	if err := s.UpdateExternalBrowserTabState(project.ID, "default", run.id, run.bridgeToken, "https://example.com/app#route", "SPA App"); err != nil {
		t.Fatal(err)
	}
	tab := externalBrowserTabSnapshot(run)
	if tab.URL != "https://example.com/app#route" || tab.Title != "SPA App" {
		t.Fatalf("state update = %+v", tab)
	}
}

func TestSanitizeExternalBrowserAgentPayloadEnforcesOriginAndBounds(t *testing.T) {
	if _, err := sanitizeExternalBrowserAgentPayload(`{"url":"https://evil.example/","text":"x"}`, "https://example.com"); err == nil {
		t.Fatal("cross-origin response accepted")
	}
	if _, err := sanitizeExternalBrowserAgentPayload(`{"url":"https://example.com/"}{}`, "https://example.com"); err == nil {
		t.Fatal("trailing JSON response accepted")
	}
	controls := make([]any, 350)
	for index := range controls {
		controls[index] = map[string]any{"tag": "button", "text": strings.Repeat("x", 700), "rect": map[string]any{"x": index, "y": 1, "width": 10, "height": 10}}
	}
	payload, _ := json.Marshal(map[string]any{"url": "https://example.com/page", "text": strings.Repeat("z", 50000), "controls": controls})
	sanitized, err := sanitizeExternalBrowserAgentPayload(string(payload), "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sanitized), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := len(decoded["controls"].([]any)); got != 300 {
		t.Fatalf("control count = %d, want 300", got)
	}
	first := decoded["controls"].([]any)[0].(map[string]any)
	if first["text"] != "" {
		t.Fatalf("oversized element text was not dropped: %#v", first["text"])
	}
}

func TestExternalBrowserApprovalDetailsAreBackendAuthoritative(t *testing.T) {
	target, _ := url.Parse("https://example.com/form")
	tab := ExternalBrowserTab{ID: "tab", URL: target.String(), Origin: "https://example.com", Title: "Checkout"}
	details := externalBrowserApprovalDetails(tab, map[string]any{"action": "fill", "x": 12, "y": 34, "text": "reviewed value"})
	encoded, _ := json.Marshal(details)
	text := string(encoded)
	for _, expected := range []string{"external_browser", "https://example.com", "https://example.com/form", "(12, 34)", "reviewed value"} {
		if !strings.Contains(text, expected) {
			t.Errorf("approval details missing %q: %s", expected, text)
		}
	}
}

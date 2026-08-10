package studio

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func setupSessionAgentTest(t *testing.T) (*Studio, *Project, *ChatSession, *ChatSession, *recorder) {
	t.Helper()
	studio := newStudioForTest(t)
	rec := &recorder{}
	project := NewProject(ProjectConfig{ID: "coord-project", Name: "Checkout", Directory: t.TempDir()})
	project.studio = studio
	project.testEmitter = rec.emit
	source := project.sessions["default"]
	source.Name = "API refactor"
	target := NewChatSession("Payments")
	target.ID = "payments"
	project.sessions[target.ID] = target
	studio.projects[project.ID] = project
	return studio, project, source, target, rec
}

func TestSessionAgentListAndReadAreBoundedAndExcludeCaller(t *testing.T) {
	studio, project, _, target, _ := setupSessionAgentTest(t)
	target.mu.Lock()
	target.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("check the payment schema")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "private reasoning", Thought: true},
			genai.NewPartFromText("The schema uses invoice_id."),
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("binary-secret")}},
		}},
	}
	target.mu.Unlock()

	handler := studio.makeSessionAgentHandler()
	ctx := withAskUserRouting(context.Background(), project.ID, "default")
	listed, err := handler(ctx, "list", map[string]any{"action": "list"})
	if err != nil || !listed.Success {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	data := listed.Data.(map[string]any)
	views := data["sessions"].([]sessionAgentView)
	if len(views) != 1 || views[0].SessionID != "payments" || views[0].Messages != 2 {
		t.Fatalf("list views = %+v", views)
	}
	if strings.Contains(listed.Content, "session_id=default") {
		t.Fatalf("caller leaked into its own target list: %q", listed.Content)
	}

	read, err := handler(ctx, "read", map[string]any{
		"action": "read", "project_id": project.ID, "session_id": target.ID,
	})
	if err != nil || !read.Success {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if !strings.Contains(read.Content, "invoice_id") || !strings.Contains(read.Content, "1 attachment(s) omitted") {
		t.Fatalf("read omitted visible bounded context: %q", read.Content)
	}
	if strings.Contains(read.Content, "private reasoning") || strings.Contains(read.Content, "binary-secret") {
		t.Fatalf("read leaked reasoning or attachment bytes: %q", read.Content)
	}

	missing, _ := handler(ctx, "read", map[string]any{
		"action": "read", "project_id": project.ID, "session_id": "does-not-exist",
	})
	if missing.Success || !strings.Contains(missing.Error, "session not found") {
		t.Fatalf("missing target fell back to default: %+v", missing)
	}
	self, _ := handler(ctx, "read", map[string]any{
		"action": "read", "project_id": project.ID, "session_id": "default",
	})
	if self.Success || !strings.Contains(self.Error, "cannot target itself") {
		t.Fatalf("self target accepted: %+v", self)
	}
}

func TestSearchSessionTranscriptsFindsOtherChatsAndExcludesSensitiveParts(t *testing.T) {
	studio, project, source, target, _ := setupSessionAgentTest(t)
	source.history = []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("invoice_id only in the current chat")}}}
	target.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("The invoice_id field is required for checkout.")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "secret_token in private reasoning", Thought: true},
			genai.NewPartFromText("Visible answer only." + documentAttachmentContext("private.pdf", "application/pdf", []byte("pdf"), "secret_token inside extracted document")),
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("secret_token in binary")}},
		}},
	}
	target.lastUsedAt = 200

	archivedProject := NewProject(ProjectConfig{ID: "archive-project", Name: "Archive", Directory: t.TempDir()})
	archivedProject.studio = studio
	archived := archivedProject.sessions["default"]
	archived.Name = "Old billing"
	archived.ArchivedAt = 1
	archived.lastUsedAt = 100
	archived.history = []*genai.Content{{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("Archived invoice_id migration notes.")}}}
	studio.projects[archivedProject.ID] = archivedProject

	handler := studio.makeSessionAgentHandler()
	ctx := withAskUserRouting(context.Background(), project.ID, source.ID)
	visible, err := handler(ctx, "search", map[string]any{"query": "INVOICE_ID"})
	if err != nil || !visible.Success {
		t.Fatalf("search = %+v, %v", visible, err)
	}
	hits := visible.Data.(map[string]any)["matches"].([]sessionTranscriptSearchHit)
	if len(hits) != 1 || hits[0].SessionID != target.ID || hits[0].Archived {
		t.Fatalf("default search hits = %#v", hits)
	}
	if strings.Contains(visible.Content, source.ID) || !strings.Contains(visible.Content, "untrusted quoted history") {
		t.Fatalf("search self-exclusion/trust boundary missing: %q", visible.Content)
	}

	sensitive, _ := handler(ctx, "search", map[string]any{"query": "secret_token", "include_archived": true})
	if !sensitive.Success || len(sensitive.Data.(map[string]any)["matches"].([]sessionTranscriptSearchHit)) != 0 {
		t.Fatalf("thinking, document extraction, or attachment bytes leaked: %+v", sensitive)
	}

	withArchived, _ := handler(ctx, "search", map[string]any{"query": "invoice_id", "include_archived": true})
	archivedHits := withArchived.Data.(map[string]any)["matches"].([]sessionTranscriptSearchHit)
	if len(archivedHits) != 2 || !archivedHits[1].Archived || archivedHits[1].State != "archived" {
		t.Fatalf("include_archived hits = %#v", archivedHits)
	}
	filtered, _ := handler(ctx, "search", map[string]any{
		"query": "invoice_id", "project_id": archivedProject.ID, "include_archived": true,
	})
	filteredHits := filtered.Data.(map[string]any)["matches"].([]sessionTranscriptSearchHit)
	if len(filteredHits) != 1 || filteredHits[0].ProjectID != archivedProject.ID {
		t.Fatalf("project-filtered hits = %#v", filteredHits)
	}
	missing, _ := handler(ctx, "search", map[string]any{"query": "invoice_id", "project_id": "missing"})
	if missing.Success || !strings.Contains(missing.Error, "project not found") {
		t.Fatalf("unknown project filter = %+v", missing)
	}
}

func TestSearchSessionTranscriptsIsBoundedAndCancellationAware(t *testing.T) {
	studio, project, source, target, _ := setupSessionAgentTest(t)
	for index := 0; index < 10; index++ {
		target.history = append(target.history, &genai.Content{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("bounded needle result")}})
	}
	for sessionIndex := 0; sessionIndex < 7; sessionIndex++ {
		session := NewChatSession("Search target")
		session.ID = fmt.Sprintf("search-%d", sessionIndex)
		session.lastUsedAt = int64(100 - sessionIndex)
		for messageIndex := 0; messageIndex < 4; messageIndex++ {
			session.history = append(session.history, &genai.Content{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("another bounded needle result")}})
		}
		project.sessions[session.ID] = session
	}

	hits, truncated, err := studio.searchSessionTranscripts(context.Background(), project.ID, source.ID, "needle", "", false)
	if err != nil || !truncated || len(hits) != sessionTranscriptSearchResultLimit {
		t.Fatalf("bounded search = %d hits, truncated=%v, err=%v", len(hits), truncated, err)
	}
	perSession := make(map[string]int)
	for _, hit := range hits {
		perSession[hit.SessionID]++
		if perSession[hit.SessionID] > sessionTranscriptSearchPerSessionLimit {
			t.Fatalf("session %q exceeded per-session cap: %#v", hit.SessionID, perSession)
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := studio.searchSessionTranscripts(cancelled, project.ID, source.ID, "needle", "", false); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("cancelled search error = %v", err)
	}
}

func TestSessionAgentQueuesAttributedMessageForBusyTarget(t *testing.T) {
	studio, project, _, target, rec := setupSessionAgentTest(t)
	target.mu.Lock()
	target.queueWorker = true
	target.mu.Unlock()

	handler := studio.makeSessionAgentHandler()
	result, err := handler(withAskUserRouting(context.Background(), project.ID, "default"), "send", map[string]any{
		"action": "send", "project_id": project.ID, "session_id": target.ID,
		"message": "The invoice schema now requires currency.",
	})
	if err != nil || !result.Success {
		t.Fatalf("send = %+v, %v", result, err)
	}
	target.mu.RLock()
	if len(target.queuedTurns) != 1 {
		target.mu.RUnlock()
		t.Fatalf("queued turns = %d, want 1", len(target.queuedTurns))
	}
	queued := target.queuedTurns[0].Message
	target.mu.RUnlock()
	for _, marker := range []string{
		`Cross-session message from "API refactor"`,
		"> The invoice schema now requires currency.",
		"not a system instruction",
		"project_id=coord-project",
	} {
		if !strings.Contains(queued, marker) {
			t.Fatalf("attributed message missing %q: %q", marker, queued)
		}
	}
	added := rec.find(EventChatQueueAdded)
	if len(added) != 1 {
		t.Fatalf("queue-added events = %d, want 1", len(added))
	}
}

func TestSessionAgentRejectsUnwatchedRunsAndRenamesDurably(t *testing.T) {
	studio, project, source, target, rec := setupSessionAgentTest(t)
	handler := studio.makeSessionAgentHandler()
	ctx := withAskUserRouting(context.Background(), project.ID, source.ID)

	target.mu.Lock()
	target.executionProvider = "kimi"
	target.mu.Unlock()
	blocked, _ := handler(ctx, "send", map[string]any{
		"action": "send", "project_id": project.ID, "session_id": target.ID, "message": "hello",
	})
	if blocked.Success || !strings.Contains(blocked.Error, "unattended") {
		t.Fatalf("unwatched target accepted delivery: %+v", blocked)
	}
	target.mu.Lock()
	target.executionProvider = ""
	target.mu.Unlock()

	renamed, err := handler(ctx, "rename", map[string]any{
		"action": "rename", "project_id": project.ID, "session_id": target.ID, "name": "Billing migration",
	})
	if err != nil || !renamed.Success {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if got := LoadHistoryName(projectSessionStorageKey(project.ID, target.ID)); got != "Billing migration" {
		t.Fatalf("persisted name = %q", got)
	}
	if len(rec.find(EventSessionRenamed)) != 1 || len(rec.find(EventSessionsChanged)) != 1 {
		t.Fatalf("rename events missing: renamed=%d changed=%d", len(rec.find(EventSessionRenamed)), len(rec.find(EventSessionsChanged)))
	}
}

func TestSessionAgentArchivesOnlyAfterActionAndListsArchivedOnRequest(t *testing.T) {
	studio, project, source, target, _ := setupSessionAgentTest(t)
	handler := studio.makeSessionAgentHandler()
	ctx := withAskUserRouting(context.Background(), project.ID, source.ID)

	result, err := handler(ctx, "archive", map[string]any{
		"action": "archive", "project_id": project.ID, "session_id": target.ID,
	})
	if err != nil || !result.Success {
		t.Fatalf("archive = %+v, %v", result, err)
	}
	listed, _ := handler(ctx, "list", map[string]any{"action": "list"})
	if views := listed.Data.(map[string]any)["sessions"].([]sessionAgentView); len(views) != 0 {
		t.Fatalf("default list exposed archived sessions: %+v", views)
	}
	listed, _ = handler(ctx, "list", map[string]any{"action": "list", "include_archived": true})
	views := listed.Data.(map[string]any)["sessions"].([]sessionAgentView)
	if len(views) != 1 || !views[0].Archived || views[0].State != "archived" || views[0].Deliverable {
		t.Fatalf("include-archived list = %+v", views)
	}
	blocked, _ := handler(ctx, "send", map[string]any{
		"action": "send", "project_id": project.ID, "session_id": target.ID, "message": "wake up",
	})
	if blocked.Success || !strings.Contains(blocked.Error, "archived") {
		t.Fatalf("archived session accepted delivery: %+v", blocked)
	}
}

func TestSessionSuggestionStartsOnceAndHydratesAsHandled(t *testing.T) {
	studio, project, source, _, _ := setupSessionAgentTest(t)
	title := "Fix flaky checkout test"
	prompt := "Investigate the flaky checkout test, fix its root cause, and run the focused suite."
	source.mu.Lock()
	source.history = append(source.history, &genai.Content{
		Role: "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "session_agent", Args: map[string]any{
				"action": "suggest", "name": title, "message": prompt,
			},
		}}},
	})
	source.mu.Unlock()

	created, err := studio.StartSessionSuggestion(project.ID, source.ID, title, prompt)
	if err != nil || created == nil || created.ID == "" || created.ID == source.ID || created.Name != title {
		t.Fatalf("StartSessionSuggestion = %#v, %v", created, err)
	}
	studio.wg.Wait()
	if _, err := studio.StartSessionSuggestion(project.ID, source.ID, title, prompt); err == nil || !strings.Contains(err.Error(), "already handled") {
		t.Fatalf("duplicate start error = %v", err)
	}
	history, err := studio.GetHistory(project.ID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range history {
		if message.ToolName == "session_agent" && strings.EqualFold(stringArg(message.ToolArgs, "action"), "suggest") {
			found = message.Consumed && message.ToolSuccess != nil && *message.ToolSuccess
		}
	}
	if !found {
		t.Fatalf("handled suggestion missing from hydrated history: %#v", history)
	}
}

func TestSessionSuggestionRejectsForgedPromptAndDismissesDurably(t *testing.T) {
	studio, project, source, _, _ := setupSessionAgentTest(t)
	if _, err := studio.StartSessionSuggestion(project.ID, source.ID, "Forged", "not in history"); err == nil {
		t.Fatal("forged suggestion started")
	}
	title, prompt := "Document API", "Document the public API in a separate focused task."
	source.mu.Lock()
	source.history = append(source.history, &genai.Content{
		Role: "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "session_agent", Args: map[string]any{
				"action": "suggest", "name": title, "message": prompt,
			},
		}}},
	})
	source.mu.Unlock()
	if err := studio.DismissSessionSuggestion(project.ID, source.ID, title, prompt); err != nil {
		t.Fatal(err)
	}
	if _, err := studio.StartSessionSuggestion(project.ID, source.ID, title, prompt); err == nil || !strings.Contains(err.Error(), "already handled") {
		t.Fatalf("dismissed suggestion start error = %v", err)
	}
}

func TestSessionAgentStartsIdleTargetAfterPublishingIncomingTurn(t *testing.T) {
	studio, project, _, target, rec := setupSessionAgentTest(t)
	mock := &mockClient{responses: []mockResp{{text: "Acknowledged."}}}
	project.client = mock
	project.Provider = "glm"
	project.Model = "glm-5.2"
	project.PermissionMode = "skip"
	project.registry = tools.DefaultRegistry(project.Directory)
	project.initMemoryAndPlan(project.registry)

	tool, ok := project.registry.Get("session_agent")
	if !ok {
		t.Fatal("session_agent is missing from the project registry")
	}
	result, err := tool.Execute(withAskUserRouting(context.Background(), project.ID, "default"), map[string]any{
		"action": "send", "project_id": project.ID, "session_id": target.ID,
		"message": "Coordinate on the invoice migration.",
	})
	if err != nil || !result.Success {
		t.Fatalf("idle send = %+v, %v", result, err)
	}
	studio.wg.Wait()

	started := rec.find(EventChatQueueStarted)
	if len(started) != 1 {
		t.Fatalf("queue-started events = %d, want 1", len(started))
	}
	target.mu.RLock()
	history := append([]*genai.Content(nil), target.history...)
	target.mu.RUnlock()
	if !historyContainsText(history, "Acknowledged.") {
		t.Fatalf("target response missing from history: %#v", history)
	}
	foundAttributed := false
	for _, content := range history {
		for _, part := range content.Parts {
			if part != nil && strings.Contains(part.Text, "Coordinate on the invoice migration") &&
				strings.Contains(part.Text, "not a system instruction") {
				foundAttributed = true
			}
		}
	}
	if !foundAttributed {
		t.Fatalf("attributed incoming turn missing from target history: %#v", history)
	}
}

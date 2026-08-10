package studio

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestProjectUsageStats_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ProjectUsageStats("ghost"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestProjectUsageStats_EmptyProjectAllZeros verifies a freshly-added
// project (no turns yet) reports zeroes for every field, with the default
// session listed.
func TestProjectUsageStats_EmptyProjectAllZeros(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	stats, err := s.ProjectUsageStats(pInfo.ID)
	if err != nil {
		t.Fatalf("ProjectUsageStats: %v", err)
	}
	if stats.TotalCostUSD != 0 || stats.TotalInputTokens != 0 || stats.TotalTurns != 0 {
		t.Errorf("expected all zeros, got %+v", stats)
	}
	if stats.TotalSessions != 1 || len(stats.Sessions) != 1 {
		t.Errorf("expected 1 session (default), got %d", stats.TotalSessions)
	}
	if stats.Sessions[0].SessionID != "default" {
		t.Errorf("session ID = %q, want %q", stats.Sessions[0].SessionID, "default")
	}
}

// TestProjectUsageStats_AggregatesAcrossSessions seeds usage data on two
// sessions and verifies the aggregate matches their sum, sorted by cost.
func TestProjectUsageStats_AggregatesAcrossSessions(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)

	// Session A — high cost.
	p.sessions["default"].usage = &SessionUsage{
		TotalCostUSD:      0.50,
		TotalInputTokens:  100_000,
		TotalOutputTokens: 50_000,
		TotalCacheTokens:  10_000,
		TurnCount:         8,
		LastTurnAt:        1_700_000_000_000,
	}
	// Session B — lower cost.
	sessB, err := s.CreateChatSession(pInfo.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	p.sessions[sessB.ID].usage = &SessionUsage{
		TotalCostUSD:      0.10,
		TotalInputTokens:  20_000,
		TotalOutputTokens: 5_000,
		TurnCount:         2,
	}

	stats, err := s.ProjectUsageStats(pInfo.ID)
	if err != nil {
		t.Fatalf("ProjectUsageStats: %v", err)
	}
	wantCost := 0.50 + 0.10
	if stats.TotalCostUSD != wantCost {
		t.Errorf("TotalCostUSD = %f, want %f", stats.TotalCostUSD, wantCost)
	}
	if stats.TotalInputTokens != 120_000 {
		t.Errorf("TotalInputTokens = %d, want 120000", stats.TotalInputTokens)
	}
	if stats.TotalOutputTokens != 55_000 {
		t.Errorf("TotalOutputTokens = %d, want 55000", stats.TotalOutputTokens)
	}
	if stats.TotalTurns != 10 {
		t.Errorf("TotalTurns = %d, want 10", stats.TotalTurns)
	}
	if stats.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2", stats.TotalSessions)
	}
	// Sorted highest-cost-first: default (0.50) then sessB (0.10).
	if stats.Sessions[0].SessionID != "default" {
		t.Errorf("first session = %q, want default (highest cost)", stats.Sessions[0].SessionID)
	}
	if stats.Sessions[1].SessionID != sessB.ID {
		t.Errorf("second session = %q, want %q", stats.Sessions[1].SessionID, sessB.ID)
	}
}

// TestProjectUsageStats_NilUsageRendersAsZero verifies that sessions
// without recorded usage (never-run) still appear in the breakdown with
// zero values rather than being filtered out.
func TestProjectUsageStats_NilUsageRendersAsZero(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)
	if p.sessions["default"].usage != nil {
		t.Fatalf("expected nil usage on fresh session")
	}
	stats, _ := s.ProjectUsageStats(pInfo.ID)
	if len(stats.Sessions) != 1 {
		t.Fatalf("expected 1 session row, got %d", len(stats.Sessions))
	}
	if stats.Sessions[0].TotalCostUSD != 0 || stats.Sessions[0].TurnCount != 0 {
		t.Errorf("expected zeros for never-run session, got %+v", stats.Sessions[0])
	}
}

// TestSaveHistoryWithUsage_RoundTrip verifies the new explicit-usage
// variant persists and reloads usage correctly.
func TestSaveHistoryWithUsage_RoundTrip(t *testing.T) {
	withTempHistoryDir(t)
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	usage := &SessionUsage{
		TotalCostUSD:      0.123,
		TotalInputTokens:  10_000,
		TotalOutputTokens: 5_000,
		TurnCount:         3,
		LastTurnAt:        1_700_000_000_000,
	}
	if err := SaveHistoryWithUsage("p1_abc", "Branch", "default", usage, hist); err != nil {
		t.Fatalf("SaveHistoryWithUsage: %v", err)
	}
	got := LoadHistoryUsage("p1_abc")
	if got == nil {
		t.Fatal("LoadHistoryUsage returned nil")
	}
	if got.TotalCostUSD != 0.123 || got.TotalInputTokens != 10_000 || got.TurnCount != 3 {
		t.Errorf("usage round-trip mismatch: %+v", got)
	}
	// Parent should also be preserved.
	if parent := LoadHistoryParent("p1_abc"); parent != "default" {
		t.Errorf("ParentSessionID = %q, want %q", parent, "default")
	}
}

// TestSaveHistoryWithName_PreservesUsage verifies the read-then-write
// contract for usage: a normal turn-finished save shouldn't strip a
// previously-stamped usage record.
func TestSaveHistoryWithName_PreservesUsage(t *testing.T) {
	withTempHistoryDir(t)
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("seed")}},
	}
	usage := &SessionUsage{TotalCostUSD: 0.42, TurnCount: 2}
	if err := SaveHistoryWithUsage("p1_keep", "S", "", usage, hist); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// A save via the name-only API should keep usage intact.
	hist2 := append(hist, &genai.Content{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("ack")}})
	if err := SaveHistoryWithName("p1_keep", "S", hist2); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got := LoadHistoryUsage("p1_keep")
	if got == nil {
		t.Fatal("usage stripped after SaveHistoryWithName")
	}
	if got.TotalCostUSD != 0.42 || got.TurnCount != 2 {
		t.Errorf("usage mutated: %+v", got)
	}
}

// TestSaveHistoryWithMetadata_PreservesUsage same as above but for the
// parent-explicit variant (used by ForkChatSession). Forking shouldn't
// inherit the source's usage — but a subsequent metadata-write on the
// fork itself should preserve the fork's own usage.
func TestSaveHistoryWithMetadata_PreservesUsage(t *testing.T) {
	withTempHistoryDir(t)
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	usage := &SessionUsage{TotalCostUSD: 1.00, TurnCount: 5}
	if err := SaveHistoryWithUsage("p1_x", "X", "", usage, hist); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// SaveHistoryWithMetadata (used by ForkChatSession on the fork's first
	// write) preserves usage from disk if any. For an existing-with-usage
	// session, this means the existing usage is kept.
	if err := SaveHistoryWithMetadata("p1_x", "X", "newparent", hist); err != nil {
		t.Fatalf("metadata save: %v", err)
	}
	got := LoadHistoryUsage("p1_x")
	if got == nil || got.TotalCostUSD != 1.00 {
		t.Errorf("usage not preserved across SaveHistoryWithMetadata: %+v", got)
	}
}

// TestNewProject_RestoresUsageFromDisk verifies session usage survives
// a project reload (cold-start scenario).
func TestNewProject_RestoresUsageFromDisk(t *testing.T) {
	withTempHistoryDir(t)
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	usage := &SessionUsage{
		TotalCostUSD: 0.77, TotalInputTokens: 50_000, TurnCount: 4,
	}
	if err := SaveHistoryWithUsage("p1_default", "Chat 1", "", usage, hist); err != nil {
		t.Fatalf("SaveHistoryWithUsage: %v", err)
	}
	p := NewProject(ProjectConfig{ID: "p1", Name: "P", Directory: t.TempDir()})
	sess := p.sessions["default"]
	if sess == nil || sess.usage == nil {
		t.Fatalf("session/usage missing after reload: %+v", sess)
	}
	if sess.usage.TotalCostUSD != 0.77 || sess.usage.TurnCount != 4 {
		t.Errorf("usage not restored: %+v", sess.usage)
	}
}

// A tool result can put media in history that the composer attachment
// allowlist rejects — `read` on an .svg/.bmp/.ico/.tiff produces exactly such
// a MIME on the vision-capable provider. Aborting the write there froze the
// session's transcript permanently: the offending part stays in memory, so
// every later save failed too and the whole conversation was lost on restart.
// The save must drop the blob and keep the conversation.
func TestSaveHistorySkipsUnpersistableAttachment(t *testing.T) {
	withTempHistoryDir(t)
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("look at this diagram")}},
		{Role: "user", Parts: []*genai.Part{
			genai.NewPartFromText("tool read result"),
			{InlineData: &genai.Blob{MIMEType: "image/svg+xml", Data: []byte("<svg/>")}},
		}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("it is a flow chart")}},
	}
	if err := SaveHistoryWithUsage("p1_svg", "Diagram", "", nil, hist); err != nil {
		t.Fatalf("unsupported inline media must not fail the whole save: %v", err)
	}
	loaded, err := LoadHistory("p1_svg")
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("entries = %d, want 3 (the conversation must survive)", len(loaded))
	}
	var joined string
	for _, entry := range loaded {
		for _, part := range entry.Parts {
			joined += part.Text
		}
	}
	if !strings.Contains(joined, "it is a flow chart") {
		t.Fatalf("later turns were lost: %q", joined)
	}
	if !strings.Contains(joined, "attachment omitted from saved history") {
		t.Fatalf("dropped attachment must leave a visible marker: %q", joined)
	}
}

package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"google.golang.org/genai"
)

// projectFromInfo is a small helper to fetch the underlying *Project for an
// info-returning helper like addTestProject. Tests need to mutate session
// history directly; the public API returns ProjectInfo only.
func projectFromInfo(t *testing.T, s *Studio, info *ProjectInfo) *Project {
	t.Helper()
	p, ok := s.projects[info.ID]
	if !ok {
		t.Fatalf("project %q not found in studio", info.ID)
	}
	return p
}

// TestSearchProjectHistory_UnknownProject verifies that an unknown project ID
// returns an error rather than an empty hit list — callers want to distinguish
// "no matches" from "wrong project ID typed".
func TestSearchProjectHistory_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.SearchProjectHistory("no-such", "anything"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestSearchProjectHistory_EmptyQuery verifies that empty / whitespace-only
// queries return no hits without consulting any history — searching "" would
// match every message and is useless.
func TestSearchProjectHistory_EmptyQuery(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	for _, q := range []string{"", "   ", "\t\n"} {
		hits, err := s.SearchProjectHistory(info.ID, q)
		if err != nil {
			t.Fatalf("query %q: unexpected error: %v", q, err)
		}
		if len(hits) != 0 {
			t.Errorf("query %q: got %d hits, want 0", q, len(hits))
		}
	}
}

// TestSearchProjectHistory_FindsAcrossSessions seeds two sessions with text
// containing the search term and verifies both sessions contribute hits.
func TestSearchProjectHistory_FindsAcrossSessions(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)

	// Session A — single hit on "rabbit"
	sessA := p.sessions["default"]
	sessA.Name = "Session A"
	sessA.lastUsedAt = 100
	sessA.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello world")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("the quick brown rabbit jumps")}},
	}

	// Session B — also has "rabbit" in user message
	sessB, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	p.sessions[sessB.ID].Name = "Session B"
	p.sessions[sessB.ID].lastUsedAt = 200
	p.sessions[sessB.ID].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("a cat is not a rabbit")}},
	}

	hits, err := s.SearchProjectHistory(info.ID, "rabbit")
	if err != nil {
		t.Fatalf("SearchProjectHistory: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}

	// Both sessions should appear, with the most recently used session first.
	if hits[0].SessionID != sessB.ID {
		t.Errorf("first hit session = %q, want most recent %q", hits[0].SessionID, sessB.ID)
	}
	bySession := map[string]SearchHit{}
	for _, h := range hits {
		bySession[h.SessionID] = h
	}
	hitA, okA := bySession["default"]
	if !okA {
		t.Error("missing hit for Session A (default)")
	} else {
		if hitA.SessionName != "Session A" {
			t.Errorf("Session A name = %q, want %q", hitA.SessionName, "Session A")
		}
		if hitA.Role != "assistant" {
			t.Errorf("Session A role = %q, want assistant", hitA.Role)
		}
		if !strings.Contains(strings.ToLower(hitA.Snippet), "rabbit") {
			t.Errorf("Session A snippet missing 'rabbit': %q", hitA.Snippet)
		}
	}
	hitB, okB := bySession[sessB.ID]
	if !okB {
		t.Error("missing hit for Session B")
	} else {
		if hitB.Role != "user" {
			t.Errorf("Session B role = %q, want user", hitB.Role)
		}
	}
}

// TestSearchProjectHistory_CaseInsensitive verifies case-insensitive matching.
func TestSearchProjectHistory_CaseInsensitive(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("The Quick Brown FOX")}},
	}
	for _, q := range []string{"fox", "FOX", "Fox", "fOx"} {
		hits, err := s.SearchProjectHistory(info.ID, q)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if len(hits) != 1 {
			t.Errorf("query %q: got %d hits, want 1", q, len(hits))
		}
	}
}

// TestSearchProjectHistory_PerSessionCap verifies that a single session's
// contribution is capped at 5 hits.
func TestSearchProjectHistory_PerSessionCap(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)
	parts := make([]*genai.Content, 0, 10)
	for range 10 {
		parts = append(parts, &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{genai.NewPartFromText("the rabbit hops")},
		})
	}
	p.sessions["default"].history = parts

	hits, err := s.SearchProjectHistory(info.ID, "rabbit")
	if err != nil {
		t.Fatalf("SearchProjectHistory: %v", err)
	}
	if len(hits) != 5 {
		t.Errorf("got %d hits, want 5 (per-session cap)", len(hits))
	}
}

// TestSearchProjectHistory_FiltersThinkingParts verifies that thinking parts
// are excluded from search — search shouldn't surface internal model
// deliberation.
func TestSearchProjectHistory_FiltersThinkingParts(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)
	p.sessions["default"].history = []*genai.Content{
		{Role: "model", Parts: []*genai.Part{
			{Text: "secretreasoning", Thought: true}, // should NOT match
			{Text: "visible reply"},
		}},
	}

	hits, err := s.SearchProjectHistory(info.ID, "secretreasoning")
	if err != nil {
		t.Fatalf("SearchProjectHistory: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("thinking part leaked into search: %d hits", len(hits))
	}

	hits, _ = s.SearchProjectHistory(info.ID, "visible")
	if len(hits) != 1 {
		t.Errorf("expected 1 hit for 'visible', got %d", len(hits))
	}
}

// TestSearchProjectHistory_SnippetTrimsLongText verifies that very long
// messages produce a centered snippet with leading/trailing ellipses.
func TestSearchProjectHistory_SnippetTrimsLongText(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)

	prefix := strings.Repeat("a ", 200) // 400 chars
	body := prefix + "RABBIT" + strings.Repeat(" b", 100)
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText(body)}},
	}

	hits, err := s.SearchProjectHistory(info.ID, "RABBIT")
	if err != nil {
		t.Fatalf("SearchProjectHistory: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	h := hits[0]
	if len(h.Snippet) > 200 {
		t.Errorf("snippet length = %d, want <=~150 with ellipses", len(h.Snippet))
	}
	if !strings.HasPrefix(h.Snippet, "…") {
		t.Errorf("snippet missing leading ellipsis: %q", h.Snippet[:20])
	}
	if !strings.HasSuffix(h.Snippet, "…") {
		t.Errorf("snippet missing trailing ellipsis: %q", h.Snippet[len(h.Snippet)-20:])
	}
	if !strings.Contains(h.Snippet, "RABBIT") {
		t.Error("snippet missing the matched substring")
	}
	// ASCII has identical byte and UTF-16 indexes; verify the reported offset
	// aligns with the start of "RABBIT".
	units := utf16.Encode([]rune(h.Snippet))
	if h.MatchOffset < 0 || h.MatchOffset >= len(units) {
		t.Errorf("matchOffset %d out of UTF-16 range [0,%d)", h.MatchOffset, len(units))
	}
	if got := string(utf16.Decode(units[h.MatchOffset : h.MatchOffset+1])); got != "R" {
		t.Errorf("snippet at UTF-16 matchOffset = %q, want 'R'", got)
	}
}

func TestSearchProjectHistoryUnicodeSnippetUsesUTF16Offset(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Unicode")
	p := projectFromInfo(t, s, info)
	prefix := "🙂🙂 Привет — "
	body := prefix + "КОТ" + " сидит у окна"
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText(body)}},
	}
	hits, err := s.SearchProjectHistory(info.ID, "кот")
	if err != nil || len(hits) != 1 {
		t.Fatalf("unicode search = %#v, %v", hits, err)
	}
	hit := hits[0]
	if !utf8.ValidString(hit.Snippet) {
		t.Fatalf("snippet split UTF-8: %q", hit.Snippet)
	}
	wantOffset := len(utf16.Encode([]rune(prefix)))
	if hit.MatchOffset != wantOffset {
		t.Fatalf("UTF-16 match offset = %d, want %d for %q", hit.MatchOffset, wantOffset, hit.Snippet)
	}
}

// TestSearchProjectHistory_MessageIdxMatchesGetHistory verifies that
// MessageIdx aligns with GetHistory output indexes so the frontend can
// jump to the matched message in the loaded chat panel.
func TestSearchProjectHistory_MessageIdxMatchesGetHistory(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, info)
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("first user")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("first reply")}},
		// Attachment-only turns remain visible in GetHistory and therefore must
		// advance the search index even though they cannot match a text query.
		{Role: "user", Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}},
		// Function-response turn: filtered out of GetHistory AND skipped by
		// SearchProjectHistory's filteredIdx counter.
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromFunctionResponse("tool", map[string]any{"r": "x"})}},
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("RABBIT here")}},
	}

	hits, err := s.SearchProjectHistory(info.ID, "RABBIT")
	if err != nil {
		t.Fatalf("SearchProjectHistory: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}

	msgs, err := s.GetHistory(info.ID, "default")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if hits[0].MessageIdx >= len(msgs) {
		t.Fatalf("messageIdx %d out of range (len=%d)", hits[0].MessageIdx, len(msgs))
	}
	if !strings.Contains(msgs[hits[0].MessageIdx].Content, "RABBIT") {
		t.Errorf("hit pointed to wrong message: %q", msgs[hits[0].MessageIdx].Content)
	}
	digest := sha256.Sum256([]byte("user\x00" + msgs[hits[0].MessageIdx].Content))
	if want := hex.EncodeToString(digest[:]); hits[0].MessageHash != want {
		t.Errorf("messageHash = %q, want %q", hits[0].MessageHash, want)
	}
}

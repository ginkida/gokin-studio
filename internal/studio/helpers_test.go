package studio

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestDeriveSessionName(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"whitespace", "   \n\t", ""},
		{"short", "fix the bug", "fix the bug"},
		{"multiline keeps first line", "fix the bug\n\nmore details", "fix the bug"},
		{"strips leading slash command", "/plan refactor auth", "refactor auth"},
		{"slash command only", "/clear", ""},
		{"strips code fence", "look at this:\n```\nvar x = 1\n```", "look at this:"},
		{"truncates long on word boundary", strings.Repeat("word ", 20), "word word word word word word word word…"},
		{"collapses whitespace", "fix   \t  the   bug", "fix the bug"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSessionName(tc.in)
			// Truncation test is approximate (different word counts hit the cap).
			if tc.name == "truncates long on word boundary" {
				if !strings.HasSuffix(got, "…") {
					t.Errorf("expected truncation with …, got %q", got)
				}
				if len(got) > 50 {
					t.Errorf("expected truncated output <= 50 chars, got %d: %q", len(got), got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("deriveSessionName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsDefaultSessionName(t *testing.T) {
	cases := map[string]bool{
		"Chat 1":          true,
		"Chat 12":         true,
		"Chat abc1":       true,
		"  Chat 2  ":      true,
		"Chat":            false,
		"Chat ":           false,
		"Chat first":      false, // 5 non-hex chars → len(rest)!=4 path
		"Chat wxyz":       false, // 4 non-hex chars → !isHex return-false path
		"fix the bug":     false,
		"Chat 1 renamed":  false, // space
		"My Chat":         false,
		"":                false,
	}
	for in, want := range cases {
		if got := isDefaultSessionName(in); got != want {
			t.Errorf("isDefaultSessionName(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHasUserMessage(t *testing.T) {
	mkText := func(role, text string) *genai.Content {
		return &genai.Content{Role: role, Parts: []*genai.Part{genai.NewPartFromText(text)}}
	}
	mkFunc := func(role string) *genai.Content {
		// function response — role="user" but no text
		return &genai.Content{Role: role, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "bash"}}}}
	}

	if hasUserMessage(nil) {
		t.Fatal("nil history should not have user messages")
	}
	if hasUserMessage([]*genai.Content{mkText("model", "hi")}) {
		t.Fatal("model-only history should not count as having user messages")
	}
	if !hasUserMessage([]*genai.Content{mkText("user", "hello"), mkText("model", "hi")}) {
		t.Fatal("user+model history should be detected")
	}
	// A function response with role=user but no text parts must NOT count.
	if hasUserMessage([]*genai.Content{mkFunc("user"), mkText("model", "hi")}) {
		t.Fatal("function-response-only history should not count as user text turn")
	}
}

func TestHumanizeAPIError(t *testing.T) {
	cases := []struct {
		in, wantSubstr string
	}{
		{"nil", ""}, // special-cased below
		{"401 Unauthorized: bad key", "API key"},
		{"Error: 429 rate limit exceeded", "Rate limited"},
		{"context length exceeded for this model", "/clear"},
		{"no such host: foo.bar.com", "internet connection"},
		{"request timeout after 60s", "timed out"},
		{"403 forbidden", "Access denied"},
		{"404 model_not_found", "Model not found"},
		{"some other weird error", "Error:"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if tc.in == "nil" {
				if got := humanizeAPIError(nil); got != "" {
					t.Errorf("humanizeAPIError(nil) = %q, want empty", got)
				}
				return
			}
			got := humanizeAPIError(errString(tc.in))
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("humanizeAPIError(%q) = %q, want substring %q", tc.in, got, tc.wantSubstr)
			}
		})
	}
}

func TestSummarizeRetryReason(t *testing.T) {
	cases := map[string]string{
		"rate limit exceeded":       "rate limit",
		"request timeout":           "timeout",
		"connection refused":        "network",
		"503 service unavailable":   "service overloaded",
		"502 bad gateway":           "gateway error",
		"EOF while reading":         "connection dropped",
		"some unclassified failure": "transient error",
	}
	for in, want := range cases {
		if got := summarizeRetryReason(errString(in)); got != want {
			t.Errorf("summarizeRetryReason(%q) = %q, want %q", in, got, want)
		}
	}
	if got := summarizeRetryReason(nil); got != "" {
		t.Errorf("summarizeRetryReason(nil) = %q, want empty", got)
	}
}

// errString lets us construct an error from a string for table-driven tests
// without pulling in fmt.Errorf per case.
type errString string

func (e errString) Error() string { return string(e) }

func TestIsInsidePath(t *testing.T) {
	cases := []struct {
		name string
		path string
		root string
		want bool
	}{
		{"equal", "/home/user/foo", "/home/user/foo", true},
		{"direct child", "/home/user/foo/bar", "/home/user/foo", true},
		{"nested grandchild", "/home/user/foo/a/b/c", "/home/user/foo", true},
		{"sibling with shared prefix must NOT match", "/home/user/foobar", "/home/user/foo", false},
		{"parent is NOT inside child", "/home/user", "/home/user/foo", false},
		{"unrelated path", "/tmp/other", "/home/user/foo", false},
		{"root with trailing slash still works", "/home/user/foo/bar", "/home/user/foo/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isInsidePath(tc.path, tc.root)
			if got != tc.want {
				t.Errorf("isInsidePath(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
			}
		})
	}
}

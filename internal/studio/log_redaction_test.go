package studio

import (
	"strings"
	"testing"
)

func TestSanitizeLogMessage_Empty(t *testing.T) {
	if got := sanitizeLogMessage(""); got != "" {
		t.Errorf("empty input should pass through; got %q", got)
	}
}

func TestSanitizeLogMessage_NoSecrets(t *testing.T) {
	cases := []string{
		"failed to save config: disk full",
		"ski rental near Bearer Lake (not a token)", // tricky: "Bearer Lake" should NOT match (word "Lake" has no token chars after Bearer)
		"client error 401 invalid token",            // generic
		"call to /v1/chat/completions returned 200",
	}
	for _, in := range cases {
		got := sanitizeLogMessage(in)
		if strings.Contains(got, "<redacted") {
			t.Errorf("benign input %q got redacted unnecessarily: %q", in, got)
		}
	}
}

func TestSanitizeLogMessage_SKKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"login failed: sk-1234567890abcdef", "login failed: <redacted:sk-key>"},
		{"key=sk-kimi-abcdef1234567890 oops", "key=<redacted:sk-key> oops"},
		{"prefix sk-zai-ABCDEFGHIJKLMNOP suffix", "prefix <redacted:sk-key> suffix"},
		// Too short → not matched (avoid false positives on "sk-x")
		{"too short sk-12345 ok", "too short sk-12345 ok"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := sanitizeLogMessage(c.in)
			if got != c.want {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestSanitizeLogMessage_BearerToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Authorization: Bearer abc123def456", "Authorization: <redacted:bearer>"},
		{"Bearer eyJabcdefghij.abcdefghij.abcdefghij retry", "<redacted:bearer> retry"},
		// "Bearer" followed by non-token text — must NOT match
		{"Be it Bearer of bad news", "Be it Bearer of bad news"},
		// Short token still matches if >= 8 chars
		{"Bearer abc12345 retry", "<redacted:bearer> retry"},
		// Too short (<8 chars) — not matched
		{"Bearer abc retry", "Bearer abc retry"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := sanitizeLogMessage(c.in)
			if got != c.want {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestSanitizeLogMessage_JWT(t *testing.T) {
	// JWTs are eyJ... three base64-segments separated by dots.
	// MiniMax uses these as API keys.
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "header X-Key: eyJabcdefgh.eyJpcGFwcml2.SflKxwRJSMeKKF retry",
			want: "header X-Key: <redacted:jwt> retry",
		},
		{
			// Just "eyJ" prefix WITHOUT three segments → not matched
			in:   "eyJustATypo not a token",
			want: "eyJustATypo not a token",
		},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := sanitizeLogMessage(c.in)
			if got != c.want {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestSanitizeLogMessage_MultipleSecretsInOneMessage(t *testing.T) {
	in := "first key sk-1234567890abcdef, then Bearer xyz123456789, ok"
	got := sanitizeLogMessage(in)
	if !strings.Contains(got, "<redacted:sk-key>") {
		t.Errorf("missing sk-key redaction in %q", got)
	}
	if !strings.Contains(got, "<redacted:bearer>") {
		t.Errorf("missing bearer redaction in %q", got)
	}
	// Neither value should appear.
	if strings.Contains(got, "sk-1234567890abcdef") || strings.Contains(got, "xyz123456789") {
		t.Errorf("secret value leaked: %q", got)
	}
}

func TestSanitizeLogMessage_Idempotent(t *testing.T) {
	in := "key=sk-1234567890abcdef end"
	once := sanitizeLogMessage(in)
	twice := sanitizeLogMessage(once)
	if once != twice {
		t.Errorf("not idempotent:\nonce  = %q\ntwice = %q", once, twice)
	}
}

// Integration: event log's Log() should apply redaction before storing.
// This is the SECURITY regression guard — if someone removes the redaction
// call from Log(), this test fails.
func TestEventLog_Log_AppliesRedaction(t *testing.T) {
	l := NewEventLog()
	const secret = "sk-DO-NOT-LOG-2345678901234567890"
	l.Log("error", "frontend", "rejected: "+secret)
	snap := l.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry; got %+v", snap)
	}
	if strings.Contains(snap[0].Message, secret) {
		t.Errorf("EVENT LOG LEAKED SECRET %q in stored message %q", secret, snap[0].Message)
	}
	if !strings.Contains(snap[0].Message, "<redacted:sk-key>") {
		t.Errorf("expected sk-key redaction marker; got %q", snap[0].Message)
	}
}

func TestEventLog_Log_RedactsBeforeTruncation(t *testing.T) {
	// If a secret appears inside the first 2048 chars of a HUGE message,
	// truncation alone won't remove it. Redaction must run first.
	l := NewEventLog()
	const secret = "sk-CRITICAL-LEAK-23456789012345678901"
	huge := strings.Repeat("padding ", 100) + secret + strings.Repeat(" tail", 300)
	l.Log("error", "test", huge)
	snap := l.Snapshot()
	if strings.Contains(snap[0].Message, secret) {
		preview := snap[0].Message
		if len(preview) > 120 {
			preview = preview[:120]
		}
		t.Errorf("HUGE message leaked secret through truncation: %q...", preview)
	}
}

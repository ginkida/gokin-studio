package studio

import "regexp"

// secretPatterns is the list of regexes that match likely secrets in log
// messages. Each match is replaced with `<redacted:kind>` so the audit log
// preserves the SHAPE of what happened (a token leak attempt) without the
// actual value.
//
// Conservative on the false-positive side: patterns are anchored to a
// recognisable prefix (sk-, Bearer, eyJ) so generic text like "ski rental"
// or "Bearer it in mind" doesn't get clobbered.
var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	// API keys with "sk-" prefix: GLM (sk-..., sk-zai-...), Anthropic-style,
	// Kimi (sk-kimi-...), OpenAI-style fallbacks if added later. Require
	// at least 16 chars after the prefix to avoid matching e.g. "sk-1".
	{kind: "sk-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	// Authorization headers — `Bearer <token>`. The token is anything not
	// whitespace, capped at 200 chars so a single Bearer doesn't eat the
	// rest of the message.
	{kind: "bearer", re: regexp.MustCompile(`\bBearer\s+[A-Za-z0-9._\-]{8,200}`)},
	// JWTs (MiniMax keys, common token shape). Three base64 segments
	// separated by dots starting with eyJ.
	{kind: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
}

// sanitizeLogMessage applies all redaction patterns to the message and
// returns the redacted form. Idempotent (running it twice is a no-op).
// Empty input returns empty.
//
// Called from EventLog.Log so EVERY log entry — frontend (iter 720+
// componentDidCatch / window.onerror / unhandledrejection) and backend
// (config save failures, audit logs, chat:error tee) — gets scrubbed
// before persistence (iter 760+ disk) and before bundling into backups
// (iter 750+/840+ archive walks include events.log).
func sanitizeLogMessage(s string) string {
	if s == "" {
		return s
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, "<redacted:"+p.kind+">")
	}
	return s
}

package client

import (
	"strings"
	"testing"
)

// TestClassifyGLMErrorCode pins the Z.AI / GLM error code classification table.
// These codes arrive in HTTP-200 SSE events with an {error:{code,message}} body.
// The keyword field must embed retryable signals ("rate limit", "overloaded") so
// isRetryableError picks them up via substring matching, enabling auto-retry for
// transient GLM errors.
func TestClassifyGLMErrorCode(t *testing.T) {
	cases := []struct {
		code        string
		message     string
		wantRetry   bool
		wantKeyword string // substring that must appear in errText for retryable cases
	}{
		// Retryable: rate limiting
		{"1210", "too many requests", true, "rate limit"},
		// Retryable: concurrency / throughput / overload
		{"1301", "concurrency exceeded", true, "overloaded"},
		{"1302", "throughput exceeded", true, "overloaded"},
		{"1303", "throughput exceeded", true, "overloaded"},
		{"1305", "service busy", true, "overloaded"},
		// Non-retryable: user must act
		{"1211", "balance low", false, ""},
		{"1213", "balance low", false, ""},
		{"1212", "quota exceeded", false, ""},
		{"1308", "balance exhausted", false, ""},
		{"1214", "auth failed", false, ""},
		{"1215", "auth failed", false, ""},
		// Unknown code with message — use raw message
		{"9999", "some weird error", false, ""},
		// Unknown code without message
		{"8888", "", false, ""},
	}

	for _, c := range cases {
		t.Run("code_"+c.code, func(t *testing.T) {
			retryable, keyword, description := classifyGLMErrorCode(c.code, c.message)
			if retryable != c.wantRetry {
				t.Errorf("retryable = %v, want %v (code=%q)", retryable, c.wantRetry, c.code)
			}
			if c.wantKeyword != "" {
				if keyword != c.wantKeyword {
					t.Errorf("keyword = %q, want %q (code=%q)", keyword, c.wantKeyword, c.code)
				}
			} else {
				if keyword != "" {
					t.Errorf("keyword = %q, want empty for non-retryable (code=%q)", keyword, c.code)
				}
			}
			if description == "" {
				t.Errorf("description must be non-empty (code=%q)", c.code)
			}
		})
	}
}

// TestClassifyGLMErrorCode_ErrorTextContainsKeyword verifies that for retryable
// codes the constructed errText (description [keyword] (code): message) contains
// the keyword so isRetryableError's substring check fires correctly.
func TestClassifyGLMErrorCode_ErrorTextContainsKeyword(t *testing.T) {
	retryableCodes := []struct {
		code    string
		message string
		keyword string
	}{
		{"1210", "too many requests", "rate limit"},
		{"1301", "too many connections", "overloaded"},
		{"1305", "server overloaded", "overloaded"},
	}

	for _, c := range retryableCodes {
		retryable, keyword, description := classifyGLMErrorCode(c.code, c.message)
		if !retryable {
			t.Fatalf("code %s should be retryable", c.code)
		}
		// Build the same errText that doStreamRequest would build.
		errText := description + " [" + keyword + "] (" + c.code + "): " + c.message
		if !strings.Contains(errText, c.keyword) {
			t.Errorf("errText %q does not contain keyword %q", errText, c.keyword)
		}
	}
}

// TestClassifyGLMErrorCode_UnknownCodeFallback verifies the fallback path:
// unknown code with a message uses the raw message; without a message uses
// "GLM error <code>".
func TestClassifyGLMErrorCode_UnknownCodeFallback(t *testing.T) {
	_, _, desc := classifyGLMErrorCode("9999", "custom error text")
	if desc != "custom error text" {
		t.Errorf("unknown code with message: got %q, want raw message", desc)
	}

	_, _, desc = classifyGLMErrorCode("8888", "")
	if !strings.HasPrefix(desc, "GLM error") {
		t.Errorf("unknown code without message: got %q, want 'GLM error ...'", desc)
	}
}

// TestClassifyGLMErrorCode_1308Actionable pins that GLM quota/balance code 1308
// surfaces an actionable recovery hint (top up / switch provider) instead of a
// raw code — ported from gokin v0.100.36.
func TestClassifyGLMErrorCode_1308Actionable(t *testing.T) {
	retryable, _, desc := classifyGLMErrorCode("1308", "balance exhausted")
	if retryable {
		t.Error("1308 (quota/balance) must be non-retryable")
	}
	if !strings.Contains(desc, "top up") && !strings.Contains(desc, "switch provider") {
		t.Errorf("1308 description should be actionable (top up / switch provider), got %q", desc)
	}
}

// TestStreamStallProneProvider pins the {kimi, glm} stall-prone set used to grant
// extra stream-idle tolerance — ported from gokin v0.100.36 (generalized Kimi's
// tolerance to GLM, which stalls mid-stream the same way).
func TestStreamStallProneProvider(t *testing.T) {
	cases := map[string]bool{
		"kimi": true, "glm": true, "GLM": true, " Kimi ": true,
		"deepseek": false, "minimax": false, "ollama": false, "anthropic": false, "": false,
	}
	for provider, want := range cases {
		if got := streamStallProneProvider(provider); got != want {
			t.Errorf("streamStallProneProvider(%q) = %v, want %v", provider, got, want)
		}
	}
}

// TestAdaptiveStreamRetryPolicyGLMDefaults verifies GLM now gets the same extra
// stream-stall tolerance Kimi had (MaxRetries>=3, MaxPartialRetries>=2) for a
// healthy/new session — the generalization from gokin v0.100.36.
func TestAdaptiveStreamRetryPolicyGLMDefaults(t *testing.T) {
	policy := AdaptiveStreamRetryPolicy("glm")
	if policy.MaxRetries < 3 {
		t.Errorf("glm MaxRetries = %d, want >= 3 (stall-prone tolerance)", policy.MaxRetries)
	}
	if policy.MaxPartialRetries < 2 {
		t.Errorf("glm MaxPartialRetries = %d, want >= 2 (stall-prone tolerance)", policy.MaxPartialRetries)
	}
}

// TestShouldExtendStreamIdle pins the one-shot idle-timeout extension decision:
// a thinking model with no content yet (silent reasoning) OR a stall-prone
// provider (GLM/Kimi) that stalled after partial content gets one extra window;
// everything else fails normally; and a stream already extended never extends
// again. This is the studio-appropriate adaptation of gokin v0.100.36's GLM
// stream-stall tolerance (studio streams live, can't resume-by-continuation).
func TestShouldExtendStreamIdle(t *testing.T) {
	cases := []struct {
		name           string
		alreadyExt     bool
		contentRecv    bool
		thinking       bool
		provider       string
		wantExtend     bool
		wantThinkPhase bool
	}{
		{"thinking, no content yet → extend (thinking phase)", false, false, true, "glm", true, true},
		{"glm stalled after partial content → extend (stall)", false, true, false, "glm", true, false},
		{"kimi stalled after partial content → extend (stall)", false, true, false, "kimi", true, false},
		{"deepseek stalled after partial content → NO extend", false, true, false, "deepseek", false, false},
		{"glm no content, thinking off → NO extend", false, false, false, "glm", false, false},
		{"minimax after content → NO extend", false, true, true, "minimax", false, false},
		{"already extended (thinking) → NO extend", true, false, true, "glm", false, false},
		{"already extended (stall) → NO extend", true, true, false, "glm", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			extend, thinkPhase := shouldExtendStreamIdle(c.alreadyExt, c.contentRecv, c.thinking, c.provider)
			if extend != c.wantExtend {
				t.Errorf("extend = %v, want %v", extend, c.wantExtend)
			}
			if thinkPhase != c.wantThinkPhase {
				t.Errorf("thinkingPhase = %v, want %v", thinkPhase, c.wantThinkPhase)
			}
		})
	}
}

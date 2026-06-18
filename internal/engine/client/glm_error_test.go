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

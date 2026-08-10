package client

import (
	"strings"
	"testing"
)

func TestClassifyCompatibleStreamErrorKimi(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		errorType string
		message   string
		retryable bool
		contains  string
	}{
		{name: "rate limit", code: "429", errorType: "rate_limit_error", message: "too many requests", retryable: true, contains: "Kimi rate limit"},
		{name: "overloaded", code: "503", errorType: "overloaded_error", message: "service overloaded", retryable: true, contains: "temporarily unavailable"},
		{name: "permanent quota", code: "429", errorType: "insufficient_quota", message: "check billing", retryable: false, contains: "quota"},
		{name: "authentication", code: "401", errorType: "authentication_error", message: "invalid API key", retryable: false, contains: "authentication failed"},
		{name: "unknown", code: "9001", errorType: "request_error", message: "bad request", retryable: false, contains: "Kimi API error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			retryable, description := classifyCompatibleStreamError("kimi", tc.code, tc.errorType, tc.message)
			if retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v (%q)", retryable, tc.retryable, description)
			}
			if !strings.Contains(description, tc.contains) {
				t.Fatalf("description = %q, want substring %q", description, tc.contains)
			}
		})
	}
}

func TestClassifyCompatibleStreamErrorDoesNotUseGLMCodesForKimi(t *testing.T) {
	retryable, description := classifyCompatibleStreamError("kimi", "1305", "request_error", "request rejected")
	if retryable {
		t.Fatalf("Kimi code 1305 was incorrectly treated as retryable GLM overload: %q", description)
	}
	if strings.Contains(description, "GLM") || strings.Contains(description, "Z.AI") {
		t.Fatalf("Kimi error leaked GLM identity: %q", description)
	}
}

package client

import "testing"

func TestGLM52ReasoningEffortMapping(t *testing.T) {
	for _, tc := range []struct {
		budget int32
		want   string
	}{
		{1, "high"},
		{8192, "high"},
		{16384, "high"},
		{32768, "max"},
	} {
		if got := glm52ReasoningEffort(tc.budget); got != tc.want {
			t.Errorf("glm52ReasoningEffort(%d) = %q, want %q", tc.budget, got, tc.want)
		}
	}
}

func TestApplyAnthropicThinking_GLM52UsesNativeEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "glm", "glm-5.2", true, 32768, 131072, 0)

	if body["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", body["reasoning_effort"])
	}
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v, want enabled", body["thinking"])
	}
}

func TestApplyAnthropicThinking_FutureGLMUsesNativeEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "glm", "glm-5.3", true, 32768, 131072, 0)
	if body["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", body["reasoning_effort"])
	}
}

func TestApplyAnthropicThinking_GLMDisabledIsExplicit(t *testing.T) {
	for _, model := range []string{"glm-5.2", "glm-5.1", "glm-4.7"} {
		t.Run(model, func(t *testing.T) {
			body := map[string]interface{}{}
			applyAnthropicThinking(body, "glm", model, false, 0, 131072, 0.7)
			thinking, ok := body["thinking"].(map[string]interface{})
			if !ok || thinking["type"] != "disabled" {
				t.Fatalf("thinking = %#v, want explicit disabled", body["thinking"])
			}
			if _, exists := body["reasoning_effort"]; exists {
				t.Fatalf("disabled request unexpectedly contains reasoning_effort: %#v", body)
			}
		})
	}
}

func TestApplyAnthropicThinking_Pre52GLMDoesNotSendEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "glm", "glm-5.1", true, 8192, 131072, 0)
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatalf("GLM-5.1 unexpectedly contains GLM-5.2 effort: %#v", body)
	}
}

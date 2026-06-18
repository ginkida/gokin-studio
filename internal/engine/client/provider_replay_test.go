package client

import (
	"testing"

	"google.golang.org/genai"
)

// Ported from the gokin upstream alongside wiring canSerialiseAssistantForProvider
// into the history→wire conversion (it was dead code in studio).

func TestCanSerialiseAssistantForProvider_DropsUnsignedThinkingForStrictProvider(t *testing.T) {
	deepseek := &AnthropicClient{
		config: AnthropicConfig{
			BaseURL:        "https://api.deepseek.com/anthropic",
			EnableThinking: true,
		},
	}
	// Unsigned thought + tool_use — DeepSeek-strict would 400 if we kept the
	// tool_use with an unsigned thinking block. Drop the whole turn.
	parts := []*genai.Part{
		{Thought: true, Text: "let me think about this"}, // no ThoughtSignature
		{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "read"}},
	}
	if deepseek.canSerialiseAssistantForProvider(parts) {
		t.Error("DeepSeek-strict must refuse an unsigned-thought message (drop whole turn)")
	}
	// Signed thought + tool_use — fine to serialise.
	signed := []*genai.Part{
		{Thought: true, Text: "let me think", ThoughtSignature: []byte("sig-abc")},
		{FunctionCall: &genai.FunctionCall{ID: "call_2", Name: "read"}},
	}
	if !deepseek.canSerialiseAssistantForProvider(signed) {
		t.Error("signed-thought message must pass")
	}
}

func TestCanSerialiseAssistantForProvider_TolerantProviderAllowsUnsignedThinking(t *testing.T) {
	kimi := &AnthropicClient{
		config: AnthropicConfig{
			BaseURL:        "https://api.kimi.com/coding",
			EnableThinking: true,
		},
	}
	parts := []*genai.Part{
		{Thought: true, Text: "let me think"}, // no signature
		{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "read"}},
	}
	if !kimi.canSerialiseAssistantForProvider(parts) {
		t.Error("Kimi should tolerate unsigned thinking (tool_use still serialisable)")
	}
}

func TestCanSerialiseAssistantForProvider_PlainTextAlwaysOK(t *testing.T) {
	for _, base := range []string{
		DefaultAnthropicBaseURL,
		"https://api.deepseek.com/anthropic",
		"https://api.kimi.com/coding",
	} {
		ac := &AnthropicClient{config: AnthropicConfig{BaseURL: base, EnableThinking: true}}
		parts := []*genai.Part{{Text: "I'll help you with that."}}
		if !ac.canSerialiseAssistantForProvider(parts) {
			t.Errorf("plain text message should serialise for %s", base)
		}
	}
}

func TestCanSerialiseAssistantForProvider_EmptyPartsUnserialisable(t *testing.T) {
	ac := &AnthropicClient{config: AnthropicConfig{BaseURL: DefaultAnthropicBaseURL, EnableThinking: true}}
	if ac.canSerialiseAssistantForProvider(nil) {
		t.Error("nil parts should not serialise")
	}
	if ac.canSerialiseAssistantForProvider([]*genai.Part{}) {
		t.Error("empty parts should not serialise")
	}
	// A turn that is ONLY an unsigned thought (no tool_use/text) is degenerate
	// and must be dropped even for tolerant providers.
	kimi := &AnthropicClient{config: AnthropicConfig{BaseURL: "https://api.kimi.com/coding", EnableThinking: true}}
	if kimi.canSerialiseAssistantForProvider([]*genai.Part{{Thought: true, Text: "hmm"}}) {
		t.Error("unsigned-thought-only turn should not serialise")
	}
}

func TestHasSerializableAssistantParts(t *testing.T) {
	cases := []struct {
		name  string
		parts []*genai.Part
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []*genai.Part{}, false},
		{"nil element", []*genai.Part{nil}, false},
		{"tool_use", []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "x", Name: "read"}}}, true},
		{"plain text", []*genai.Part{{Text: "hi"}}, true},
		{"signed thought", []*genai.Part{{Thought: true, Text: "t", ThoughtSignature: []byte("s")}}, true},
		{"unsigned thought only", []*genai.Part{{Thought: true, Text: "t"}}, false},
		{"empty text only", []*genai.Part{{Text: ""}}, false},
	}
	for _, tc := range cases {
		if got := hasSerializableAssistantParts(tc.parts); got != tc.want {
			t.Errorf("%s: hasSerializableAssistantParts = %v, want %v", tc.name, got, tc.want)
		}
	}
}

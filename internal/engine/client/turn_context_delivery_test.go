package client

import (
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestAppendTurnContextToContentsIsCopyOnWrite(t *testing.T) {
	first := genai.NewContentFromText("earlier", genai.RoleUser)
	last := genai.NewContentFromText("question", genai.RoleUser)
	history := []*genai.Content{first, last}

	got := appendTurnContextToContents(history, "project facts")
	if len(got) != 2 || got[0] != first || got[1] == last {
		t.Fatalf("unexpected copy-on-write shape: %#v", got)
	}
	if len(last.Parts) != 1 || last.Parts[0].Text != "question" {
		t.Fatalf("caller history was mutated: %#v", last.Parts)
	}
	if len(got[1].Parts) != 2 || !strings.Contains(got[1].Parts[1].Text, "project facts") || !strings.Contains(got[1].Parts[1].Text, "<turn-context>") {
		t.Fatalf("ephemeral context part missing: %#v", got[1].Parts)
	}
}

func TestAppendTurnContextRequiresFinalUserMessage(t *testing.T) {
	model := genai.NewContentFromText("answer", genai.RoleModel)
	history := []*genai.Content{model}
	if got := appendTurnContextToContents(history, "context"); got[0] != model {
		t.Fatalf("model-final history should be unchanged: %#v", got)
	}
	if got := appendTurnContextToContents(history, ""); got[0] != model {
		t.Fatal("empty context should be a no-op")
	}
}

func TestOpenAIRequestIncludesAndClearsEphemeralTurnContext(t *testing.T) {
	c := &OpenAIOAuthClient{model: "test-model"}
	history := []*genai.Content{genai.NewContentFromText("question", genai.RoleUser)}
	c.SetTurnContext("reference snapshot")
	body := c.buildRequest(history)
	input, ok := body["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("OpenAI input missing context item: %#v", body["input"])
	}
	if text, _ := input[1]["content"].(string); !strings.Contains(text, "reference snapshot") {
		t.Fatalf("OpenAI context content missing: %#v", input[1])
	}
	if len(history[0].Parts) != 1 {
		t.Fatal("OpenAI request construction mutated history")
	}

	c.SetTurnContext("")
	body = c.buildRequest(history)
	input, _ = body["input"].([]map[string]any)
	if len(input) != 1 {
		t.Fatalf("cleared context leaked into next request: %#v", input)
	}
}

func TestTurnContextSurvivesProviderModelClone(t *testing.T) {
	gemini := &GeminiClient{model: "one"}
	gemini.SetTurnContext("gemini context")
	geminiClone := gemini.WithModel("two").(*GeminiClient)
	if geminiClone.turnContext != "gemini context" {
		t.Fatal("Gemini model clone lost turn context")
	}

	oauth := &GeminiOAuthClient{model: "one"}
	oauth.SetTurnContext("oauth context")
	oauthClone := oauth.WithModel("two").(*GeminiOAuthClient)
	if oauthClone.turnContext != "oauth context" {
		t.Fatal("Gemini OAuth model clone lost turn context")
	}
}

func TestAnthropicAndOllamaClonesIsolateMutableTurnState(t *testing.T) {
	transport := &http.Client{}
	anthropic := &AnthropicClient{
		config: AnthropicConfig{Model: "one"}, httpClient: transport, turnContext: "base",
	}
	a := anthropic.WithModel("two").(*AnthropicClient)
	b := anthropic.WithModel("three").(*AnthropicClient)
	if a == anthropic || b == anthropic || a == b || a.httpClient != transport || b.httpClient != transport {
		t.Fatal("Anthropic WithModel did not create isolated state sharing one transport")
	}
	a.SetTurnContext("session-a")
	b.SetTurnContext("session-b")
	if anthropic.turnContext != "base" || a.turnContext != "session-a" || b.turnContext != "session-b" {
		t.Fatalf("Anthropic turn state leaked: base=%q a=%q b=%q", anthropic.turnContext, a.turnContext, b.turnContext)
	}

	ollama := &OllamaClient{config: OllamaConfig{Model: "one"}, turnContext: "base"}
	o1 := ollama.WithModel("two").(*OllamaClient)
	o2 := ollama.WithModel("three").(*OllamaClient)
	if o1 == ollama || o2 == ollama || o1 == o2 {
		t.Fatal("Ollama WithModel did not create isolated state clones")
	}
	o1.SetTurnContext("session-a")
	o2.SetTurnContext("session-b")
	if ollama.turnContext != "base" || o1.turnContext != "session-a" || o2.turnContext != "session-b" {
		t.Fatalf("Ollama turn state leaked: base=%q a=%q b=%q", ollama.turnContext, o1.turnContext, o2.turnContext)
	}
}

func TestFallbackTurnClonePreservesSelectedProvider(t *testing.T) {
	first := &fakeClient{id: "first"}
	second := &fakeClient{id: "second"}
	chain, err := NewFallbackClient([]Client{first, second}, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	chain.current = 1
	clone, ok := chain.WithModel("same").(*FallbackClient)
	if !ok {
		t.Fatalf("WithModel returned %T, want *FallbackClient", chain.WithModel("same"))
	}
	if clone.current != 1 {
		t.Fatalf("fallback clone reset selected provider: got %d, want 1", clone.current)
	}
}

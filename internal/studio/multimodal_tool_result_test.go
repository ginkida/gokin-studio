package studio

import (
	"context"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type imageResultTool struct{}

func (*imageResultTool) Name() string        { return "image_probe" }
func (*imageResultTool) Description() string { return "returns a test image" }
func (*imageResultTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "image_probe"}
}
func (*imageResultTool) Validate(map[string]any) error { return nil }
func (*imageResultTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	result := tools.NewSuccessResult("captured")
	result.MultimodalParts = []*tools.MultimodalPart{{MimeType: "image/png", Data: []byte{1, 2, 3}}}
	return result, nil
}

type multimodalMockClient struct {
	*mockClient
	partsCalls [][]*genai.Part
}

func (m *multimodalMockClient) WithModel(string) client.Client { return m }

func (m *multimodalMockClient) SendFunctionResponseParts(_ context.Context, history []*genai.Content, parts []*genai.Part) (*client.StreamingResponse, error) {
	copied := append([]*genai.Part(nil), parts...)
	m.partsCalls = append(m.partsCalls, copied)
	m.lastFuncRespHistory = history
	return makeStream(m.pop()), nil
}

func TestStudioDeliversToolImagesToKimiAsAdjacentParts(t *testing.T) {
	base := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{ID: "call-image", Name: "image_probe"}}},
		{text: "I can see it"},
	}}
	mc := &multimodalMockClient{mockClient: base}
	reg := tools.NewRegistry()
	if err := reg.Register(&imageResultTool{}); err != nil {
		t.Fatal(err)
	}
	p, _ := newTestProject(t, base, reg)
	p.client = mc
	p.Provider = "kimi"
	p.Model = "k3"
	runAgent(p, "inspect screen")

	if len(mc.partsCalls) != 1 {
		t.Fatalf("multimodal calls = %d, want 1", len(mc.partsCalls))
	}
	parts := mc.partsCalls[0]
	if len(parts) != 2 || parts[0].FunctionResponse == nil || parts[1].InlineData == nil {
		t.Fatalf("parts = %#v, want FunctionResponse followed by InlineData", parts)
	}
	if parts[0].FunctionResponse.ID != "call-image" || parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("unexpected multimodal parts: %#v", parts)
	}
}

func TestStudioKeepsToolImagesOutOfGLMRequestsAndHistory(t *testing.T) {
	base := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{ID: "call-image", Name: "image_probe"}}},
		{text: "text fallback"},
	}}
	mc := &multimodalMockClient{mockClient: base}
	reg := tools.NewRegistry()
	if err := reg.Register(&imageResultTool{}); err != nil {
		t.Fatal(err)
	}
	p, _ := newTestProject(t, base, reg)
	p.client = mc
	runAgent(p, "inspect image")

	if len(mc.partsCalls) != 0 {
		t.Fatalf("GLM received %d multimodal calls", len(mc.partsCalls))
	}
	p.mu.RLock()
	session := p.sessions["default"]
	p.mu.RUnlock()
	session.mu.RLock()
	defer session.mu.RUnlock()
	for _, content := range session.history {
		for _, part := range content.Parts {
			if part.InlineData != nil {
				t.Fatal("GLM history retained unsupported tool image")
			}
		}
	}
}

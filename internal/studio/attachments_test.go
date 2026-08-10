package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// 1×1 transparent PNG.
const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(testPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testDOCXAttachment(t *testing.T) MessageAttachment {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "brief.docx")
	result, err := tools.NewDocumentCreateTool(dir).Execute(context.Background(), map[string]any{
		"file_path": path,
		"format":    "docx",
		"content_json": `{"title":"Client brief","sections":[` +
			`{"heading":"Decision","paragraphs":["Approve the bounded document pipeline."]}]}`,
	})
	if err != nil || !result.Success {
		t.Fatalf("create DOCX = %#v, %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return MessageAttachment{
		Name:     "brief.docx",
		MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Data:     base64.StdEncoding.EncodeToString(data),
	}
}

func TestDecodeMessageAttachments_KimiImage(t *testing.T) {
	parts, err := decodeMessageAttachments("kimi", "k3", []MessageAttachment{{
		Name: "pixel.png", MIMEType: "image/png", Data: testPNGBase64,
	}})
	if err != nil {
		t.Fatalf("decodeMessageAttachments: %v", err)
	}
	if len(parts) != 1 || parts[0].InlineData == nil {
		t.Fatalf("decoded parts = %#v", parts)
	}
	if parts[0].InlineData.MIMEType != "image/png" || !bytes.Equal(parts[0].InlineData.Data, testPNGBytes(t)) {
		t.Fatalf("decoded blob = %#v", parts[0].InlineData)
	}
}

func TestDecodeMessageAttachments_RejectsUnsupportedProviderAndSpoof(t *testing.T) {
	valid := MessageAttachment{Name: "pixel.png", MIMEType: "image/png", Data: testPNGBase64}
	if _, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{valid}); err == nil {
		t.Fatal("GLM text-only model accepted an image")
	}
	spoof := valid
	spoof.MIMEType = "image/jpeg"
	if _, err := decodeMessageAttachments("kimi", "k3", []MessageAttachment{spoof}); err == nil {
		t.Fatal("MIME-spoofed image was accepted")
	}
}

func TestDecodeMessageAttachments_DocumentForGLMAndKimi(t *testing.T) {
	attachment := testDOCXAttachment(t)
	for _, target := range []struct {
		provider, model string
	}{{"glm", "glm-5.2"}, {"kimi", "k3"}} {
		t.Run(target.provider, func(t *testing.T) {
			parts, err := decodeMessageAttachments(target.provider, target.model, []MessageAttachment{attachment})
			if err != nil {
				t.Fatal(err)
			}
			if len(parts) != 2 || parts[0].Text == "" || parts[1].InlineData == nil {
				t.Fatalf("document parts = %#v", parts)
			}
			if !strings.Contains(parts[0].Text, "UNTRUSTED ATTACHED DOCUMENT") ||
				!strings.Contains(parts[0].Text, "Approve the bounded document pipeline") {
				t.Fatalf("document extraction = %q", parts[0].Text)
			}
			if parts[1].InlineData.DisplayName != "brief.docx" {
				t.Fatalf("display name = %q", parts[1].InlineData.DisplayName)
			}
			filtered := historyForProvider([]*genai.Content{{Role: "user", Parts: parts}}, target.provider)
			if len(filtered) != 1 || len(filtered[0].Parts) != 1 || filtered[0].Parts[0].InlineData != nil {
				t.Fatalf("provider history retained document binary = %#v", filtered)
			}
		})
	}
}

func TestDocumentAttachmentContextIsHiddenFromChatAndRejectsMalformedPackage(t *testing.T) {
	attachment := testDOCXAttachment(t)
	parts, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	visible := stripDocumentAttachmentContext("Please summarize." + parts[0].Text)
	if visible != "Please summarize." {
		t.Fatalf("visible chat text = %q", visible)
	}
	malformed := attachment
	malformed.Data = base64.StdEncoding.EncodeToString([]byte("PK\x03\x04not-an-office-package"))
	if _, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{malformed}); err == nil {
		t.Fatal("malformed DOCX was accepted")
	}
	badName := attachment
	badName.Name = "../brief.docx"
	if _, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{badName}); err == nil {
		t.Fatal("path-like attachment name was accepted")
	}
	for _, name := range []string{"folder\\brief.docx", "brief\nforged.docx"} {
		badName.Name = name
		if _, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{badName}); err == nil {
			t.Fatalf("unsafe attachment name %q was accepted", name)
		}
	}
}

func TestDocumentAttachmentHistoryRoundTripPreservesNameAndBinary(t *testing.T) {
	withTempConfigDir(t)
	attachment := testDOCXAttachment(t)
	parts, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	key := "document_attachment_session"
	if err := SaveHistoryWithName(key, "Documents", []*genai.Content{{Role: "user", Parts: parts}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadHistory(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || len(loaded[0].Parts) != 2 || loaded[0].Parts[1].InlineData == nil {
		t.Fatalf("loaded history = %#v", loaded)
	}
	blob := loaded[0].Parts[1].InlineData
	if blob.DisplayName != "brief.docx" || blob.MIMEType != attachment.MIMEType {
		t.Fatalf("loaded blob metadata = %#v", blob)
	}
	original, _ := base64.StdEncoding.DecodeString(attachment.Data)
	if !bytes.Equal(blob.Data, original) {
		t.Fatal("document bytes changed after history round trip")
	}
}

func TestSendMessageWithAttachmentsRejectsBeforeStartingTurn(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Images")
	attachment := MessageAttachment{Name: "pixel.png", MIMEType: "image/png", Data: testPNGBase64}
	err := s.SendMessageWithAttachments(info.ID, "inspect", []MessageAttachment{attachment}, "default")
	if err == nil || !strings.Contains(err.Error(), "Kimi") {
		t.Fatalf("GLM attachment error = %v", err)
	}
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	session := p.GetSession("default")
	session.mu.RLock()
	defer session.mu.RUnlock()
	if len(session.history) != 0 || session.active {
		t.Fatalf("rejected attachment changed session: history=%d active=%v", len(session.history), session.active)
	}
}

func TestSendMessageWithDocumentDeliversExtractionAndPreservesBlobMetadata(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	attachment := testDOCXAttachment(t)
	parts, err := decodeMessageAttachments("glm", "glm-5.2", []MessageAttachment{attachment})
	if err != nil {
		t.Fatal(err)
	}

	p.SendMessageWithAttachments(context.Background(), "Summarize.", parts, Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.2",
	})

	mc.mu.Lock()
	calls := append([][]*genai.Content(nil), mc.sendHistoryCalls...)
	mc.mu.Unlock()
	if len(calls) != 1 || !historyContainsTextContaining(calls[0], "Approve the bounded document pipeline") {
		t.Fatalf("provider did not receive extracted document context: %#v", calls)
	}
	for _, content := range calls[0] {
		for _, part := range content.Parts {
			if part != nil && part.InlineData != nil {
				t.Fatal("provider received the native document binary")
			}
		}
	}

	session := p.GetSession("default")
	session.mu.RLock()
	defer session.mu.RUnlock()
	if len(session.history) < 1 || len(session.history[0].Parts) != 3 {
		t.Fatalf("persisted user turn = %#v", session.history)
	}
	blob := session.history[0].Parts[2].InlineData
	if blob == nil || blob.DisplayName != "brief.docx" {
		t.Fatalf("persisted document metadata = %#v", blob)
	}
}

func historyContainsTextContaining(history []*genai.Content, want string) bool {
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && strings.Contains(part.Text, want) {
				return true
			}
		}
	}
	return false
}

func TestHistoryAttachmentPersistsOutsideJSONAndReloads(t *testing.T) {
	withTempConfigDir(t)
	key := "project_session"
	data := testPNGBytes(t)
	history := []*genai.Content{{
		Role: "user",
		Parts: []*genai.Part{
			genai.NewPartFromText("inspect this"),
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: data}},
		},
	}}
	if err := SaveHistoryWithName(key, "Media", history); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}
	jsonData, err := os.ReadFile(historyPath(key))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(jsonData, []byte(testPNGBase64)) {
		t.Fatal("history JSON contains inline base64 media")
	}
	mediaEntries, err := os.ReadDir(historyMediaDir(key))
	if err != nil || len(mediaEntries) != 1 {
		t.Fatalf("media entries = %v, err = %v", mediaEntries, err)
	}

	loaded, err := LoadHistory(key)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].Parts) != 2 || loaded[0].Parts[1].InlineData == nil {
		t.Fatalf("loaded history = %#v", loaded)
	}
	if !bytes.Equal(loaded[0].Parts[1].InlineData.Data, data) {
		t.Fatal("reloaded attachment bytes differ")
	}

	DeleteHistory(key)
	if _, err := os.Stat(historyMediaDir(key)); !os.IsNotExist(err) {
		t.Fatalf("media directory survived DeleteHistory: %v", err)
	}
}

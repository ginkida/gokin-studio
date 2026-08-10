package studio

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestHistoryForProvider_StripsImagesOnlyForGLM(t *testing.T) {
	image := &genai.Part{InlineData: &genai.Blob{MIMEType: "image/png", Data: testPNGBytes(t)}}
	history := []*genai.Content{{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText("look"), image},
	}}
	if got := historyForProvider(history, "kimi"); len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("Kimi history lost image: %#v", got)
	}
	got := historyForProvider(history, "glm")
	if len(got) != 1 || len(got[0].Parts) != 2 {
		t.Fatalf("GLM filtered history = %#v", got)
	}
	if got[0].Parts[0].Text != "look" || !strings.Contains(got[0].Parts[1].Text, "omitted") {
		t.Fatalf("GLM fallback parts = %#v", got[0].Parts)
	}
	for _, part := range got[0].Parts {
		if part.InlineData != nil {
			t.Fatal("GLM history retained image part")
		}
	}
}

func TestModelSwitchWarning_MediaAndCache(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Switch")
	if err := s.SetProjectProvider(info.ID, "kimi", "k3"); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	session := p.GetSession("default")
	session.mu.Lock()
	session.history = []*genai.Content{{
		Role: "user",
		Parts: []*genai.Part{
			genai.NewPartFromText("image"),
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: testPNGBytes(t)}},
		},
	}}
	session.mu.Unlock()

	warning, err := s.ModelSwitchWarning(info.ID, "glm", "glm-5.2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "image") || !strings.Contains(warning, "text-only") {
		t.Fatalf("warning = %q", warning)
	}

	cacheWarning, err := s.ModelSwitchWarning(info.ID, "kimi", "k3-256k")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cacheWarning, "cache") {
		t.Fatalf("cache warning = %q", cacheWarning)
	}
}

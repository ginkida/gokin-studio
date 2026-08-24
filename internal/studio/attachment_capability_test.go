package studio

import (
	"strings"
	"testing"
)

// The composer decides whether to offer an image picker from the catalog's
// per-model inputModalities; the backend gate used to decide the same thing
// from the provider name instead. Two copies of one rule agreed only because
// the modality split happened to fall exactly on the provider boundary — the
// first Kimi model without vision, or the first GLM model with it, would have
// split them, and the user-visible result is a composer that accepts an image
// the backend then refuses (or the reverse).
//
// This pins the backend to the catalog for every model the product ships.
func TestImageAttachmentGateMatchesCatalogModalities(t *testing.T) {
	image := MessageAttachment{Name: "pixel.png", MIMEType: "image/png", Data: testPNGBase64}
	for _, provider := range studioProviderCatalog {
		for _, model := range provider.Models {
			declared := modelSupportsImageInput(provider.ID, model)
			_, err := decodeMessageAttachments(provider.ID, model, []MessageAttachment{image})
			accepted := err == nil
			if accepted != declared {
				t.Errorf("%s/%s: catalog advertises image=%v but decodeMessageAttachments accepted=%v (err=%v)",
					provider.ID, model, declared, accepted, err)
			}
			if !declared && err != nil && !strings.Contains(err.Error(), model) {
				t.Errorf("%s/%s: refusal %q should name the model that lacks image input", provider.ID, model, err)
			}
		}
	}
}

// A model advertised by the account before the catalog ships an entry inherits
// its family flagship's modalities, so the gate must follow that inference
// rather than falling back to "unknown model, no images".
func TestImageAttachmentGateFollowsInferredFutureModels(t *testing.T) {
	if !isFutureStudioModelID("kimi", "k4") {
		t.Skip("k4 is no longer treated as a forward-compatible id")
	}
	if !modelSupportsImageInput("kimi", "k4") {
		t.Error("a future Kimi flagship must inherit k3's image input")
	}
	if modelSupportsImageInput("glm", "glm-6") {
		t.Error("a future GLM flagship must inherit the text-only modality, not gain vision")
	}
}

// Anything the catalog does not describe at all must not be granted vision.
func TestImageAttachmentGateRefusesUnknownModels(t *testing.T) {
	for _, c := range [][2]string{{"kimi", ""}, {"glm", ""}, {"", ""}, {"ollama", "llama3"}, {"kimi", "K3"}} {
		if modelSupportsImageInput(c[0], c[1]) {
			t.Errorf("modelSupportsImageInput(%q, %q) = true for a model the catalog does not describe", c[0], c[1])
		}
	}
}

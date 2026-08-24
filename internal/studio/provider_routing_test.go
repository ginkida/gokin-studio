package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/config"
)

// clearProviderKeyEnv removes every key the client factory could pick up, so a
// developer machine that exports a real key cannot mask a routing bug.
func clearProviderKeyEnv(t *testing.T) {
	t.Helper()
	for _, p := range config.Providers {
		for _, env := range p.EnvVars {
			t.Setenv(env, "")
		}
	}
	t.Setenv("GOOGLE_API_KEY", "")
}

// Every catalog model must reach its OWN provider. The regression this guards
// against: initClient set only cfg.API.ActiveProvider, which the client factory
// never reads — it resolves cfg.Model.Provider and falls back to guessing from
// the model name, defaulting to Gemini when no prefix matches. A Kimi project on
// "k3" therefore built a Gemini client and asked the user for a Gemini API key.
// Asserting on the missing-key error is what makes the routing observable: the
// message names the provider the factory actually chose.
func TestInitClientRoutesEveryCatalogModelToItsOwnProvider(t *testing.T) {
	clearProviderKeyEnv(t)
	for _, provider := range studioProviderCatalog {
		registryEntry := config.GetProvider(provider.ID)
		if registryEntry == nil {
			t.Fatalf("engine provider registry has no entry for catalog provider %q", provider.ID)
		}
		for _, model := range provider.Models {
			t.Run(provider.ID+"/"+model, func(t *testing.T) {
				p := &Project{ID: "p", Directory: t.TempDir(), Provider: provider.ID, Model: model}
				err := p.initClient(Settings{})
				if err == nil {
					t.Fatal("expected a missing-key error with every provider key cleared")
				}
				text := err.Error()
				if !strings.Contains(text, registryEntry.EnvVars[0]) &&
					!strings.Contains(text, registryEntry.DisplayName) {
					t.Fatalf("error = %q, want it to name %s (%s) — the factory routed to a different provider",
						text, registryEntry.DisplayName, registryEntry.EnvVars[0])
				}
				for _, other := range config.Providers {
					// Ollama is local and declares no key env var.
					if other.Name == provider.ID || len(other.EnvVars) == 0 {
						continue
					}
					if strings.Contains(text, other.EnvVars[0]) {
						t.Fatalf("error = %q leaks unrelated provider %s: the request was routed to the wrong client",
							text, other.Name)
					}
				}
			})
		}
	}
}

// The exact user-visible symptom, pinned on its own so a regression reads as
// what it is rather than as a generic routing failure.
func TestInitClientKimiK3NeverAsksForAGeminiKey(t *testing.T) {
	clearProviderKeyEnv(t)
	p := &Project{ID: "p", Directory: t.TempDir(), Provider: "kimi", Model: "k3"}
	err := p.initClient(Settings{})
	if err == nil {
		t.Fatal("expected a missing-key error with every provider key cleared")
	}
	text := err.Error()
	if strings.Contains(strings.ToLower(text), "gemini") || strings.Contains(text, "aistudio.google.com") {
		t.Fatalf("kimi/k3 asked for a Gemini key: %q", text)
	}
	// The message names the provider's first env alias (GOKIN_KIMI_KEY); accept
	// any of the aliases the factory actually honours rather than pinning one.
	named := strings.Contains(text, "Kimi")
	for _, env := range config.GetProvider("kimi").EnvVars {
		named = named || strings.Contains(text, env)
	}
	if !named {
		t.Fatalf("error = %q, want it to name Kimi or one of its key env vars", text)
	}
}

// A configured key must produce a working client rather than an error, which is
// the other half of the routing contract.
func TestInitClientKimiK3BuildsAClientWithAKey(t *testing.T) {
	clearProviderKeyEnv(t)
	p := &Project{ID: "p", Directory: t.TempDir(), Provider: "kimi", Model: "k3"}
	if err := p.initClient(Settings{KimiKey: "test-key-not-a-real-credential"}); err != nil {
		t.Fatalf("initClient with a Kimi key: %v", err)
	}
	if p.client == nil {
		t.Fatal("initClient reported success but left p.client nil")
	}
	t.Cleanup(func() {
		p.mu.Lock()
		p.resetClientLocked()
		p.mu.Unlock()
	})
}

// Defense in depth for the engine-level path: name-based detection has to know
// the current flagship ids, which carry no vendor name at all.
func TestDetectProviderFromModelKnowsCatalogModelNames(t *testing.T) {
	for _, provider := range studioProviderCatalog {
		for _, model := range provider.Models {
			if got := config.DetectProviderFromModel(model); got != provider.ID {
				t.Errorf("DetectProviderFromModel(%q) = %q, want %q", model, got, provider.ID)
			}
		}
	}
}

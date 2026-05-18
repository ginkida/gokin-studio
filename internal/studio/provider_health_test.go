package studio

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httpProbe is a thin tester wrapper around the package-level helpers so we
// can drive them against a httptest.Server without going through the full
// CheckProviderHealth dispatch (which reads from s.config).
func httpProbe(t *testing.T, url string, useAuth bool, apiKey string) *ProviderHealthInfo {
	t.Helper()
	info := &ProviderHealthInfo{Provider: "test"}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if useAuth {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	doProbe(info, req)
	return info
}

func TestCheckProviderHealth_Unknown(t *testing.T) {
	s := newStudioForTest(t)
	info := s.CheckProviderHealth("nope")
	if info.OK {
		t.Error("expected OK=false for unknown provider")
	}
	if info.Error == "" {
		t.Error("expected error message for unknown provider")
	}
}

func TestCheckProviderHealth_GLM_NoKeySet(t *testing.T) {
	s := newStudioForTest(t)
	// Default config has empty GLMKey.
	info := s.CheckProviderHealth("glm")
	if info.OK {
		t.Error("expected OK=false when no API key configured")
	}
	if !strings.Contains(info.Error, "no API key") {
		t.Errorf("expected 'no API key' error, got %q", info.Error)
	}
}

func TestProbe_2xxIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "test-key")
	if !info.OK {
		t.Errorf("expected OK=true on 200, got %+v", info)
	}
	if info.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", info.StatusCode)
	}
	if info.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d, want >=0", info.LatencyMs)
	}
}

func TestProbe_401IsBadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "wrong-key")
	if info.OK {
		t.Error("expected OK=false on 401")
	}
	if !strings.Contains(info.Error, "invalid API key") {
		t.Errorf("expected 'invalid API key' error, got %q", info.Error)
	}
	if info.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", info.StatusCode)
	}
}

func TestProbe_403IsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "key")
	if info.OK {
		t.Error("expected OK=false on 403")
	}
	if !strings.Contains(info.Error, "forbidden") {
		t.Errorf("expected 'forbidden' error, got %q", info.Error)
	}
}

// TestProbe_404IsTreatedAsReachable verifies the special-case for
// providers that don't expose /v1/models. The probe returns OK=true with
// a "may still work" note rather than blocking the user.
func TestProbe_404IsTreatedAsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "key")
	if !info.OK {
		t.Errorf("expected OK=true on 404 (treat as reachable), got %+v", info)
	}
	if !strings.Contains(info.Description, "may still work") {
		t.Errorf("expected 'may still work' note, got %q", info.Description)
	}
}

func TestProbe_4xxIsClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "key")
	if info.OK {
		t.Error("expected OK=false on 400")
	}
	if !strings.Contains(info.Error, "client error") {
		t.Errorf("expected 'client error' message, got %q", info.Error)
	}
}

func TestProbe_5xxIsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	info := httpProbe(t, srv.URL+"/v1/models", true, "key")
	if info.OK {
		t.Error("expected OK=false on 500")
	}
	if !strings.Contains(info.Error, "server error") {
		t.Errorf("expected 'server error' message, got %q", info.Error)
	}
}

// TestProbe_UnreachableHostFailsCleanly verifies that a connection error
// (refused / non-routable IP) populates the Error + Description without
// panicking. Uses a port that's almost certainly closed on localhost.
func TestProbe_UnreachableHostFailsCleanly(t *testing.T) {
	info := httpProbe(t, "http://127.0.0.1:1/v1/models", true, "key")
	if info.OK {
		t.Errorf("expected OK=false for closed port, got %+v", info)
	}
	if info.Error == "" {
		t.Error("expected an Error message")
	}
}

// TestCheckProviderHealth_GLM_AgainstFakeServer wires the full
// CheckProviderHealth path through a fake server by overriding the GLM
// base URL. This exercises probeAnthropicCompat end-to-end.
//
// We can't override the base URL via settings (it's compiled in via
// client.DefaultGLMBaseURL), so we instead test ollama which DOES read
// the URL from settings.
func TestCheckProviderHealth_OllamaWithCustomURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/tags") {
			t.Errorf("expected /api/tags, got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	s := newStudioForTest(t)
	s.config.Settings.OllamaURL = srv.URL

	info := s.CheckProviderHealth("ollama")
	if !info.OK {
		t.Errorf("expected OK=true, got %+v", info)
	}
	if !strings.Contains(info.Endpoint, "/api/tags") {
		t.Errorf("Endpoint = %q, expected /api/tags suffix", info.Endpoint)
	}
}

// TestProbeAnthropicCompat_AgainstFakeServer drives probeAnthropicCompat
// directly (it's package-private) so we can exercise the auth header
// + endpoint construction without hitting the real GLM/Kimi/MiniMax API.
// Verifies the Authorization Bearer header lands on the request.
func TestProbeAnthropicCompat_AgainstFakeServer(t *testing.T) {
	var sawAuth, sawAnthropicVersion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-key-abc" {
			sawAuth = true
		}
		if r.Header.Get("anthropic-version") != "" {
			sawAnthropicVersion = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	info := &ProviderHealthInfo{Provider: "test"}
	probeAnthropicCompat(info, "test", "test-key-abc", srv.URL)
	if !info.OK {
		t.Errorf("expected OK=true, got %+v", info)
	}
	if !sawAuth {
		t.Error("expected Authorization Bearer header on request")
	}
	if !sawAnthropicVersion {
		t.Error("expected anthropic-version header on request")
	}
	if !strings.HasSuffix(info.Endpoint, "/v1/models") {
		t.Errorf("Endpoint = %q, expected /v1/models suffix", info.Endpoint)
	}
}

// TestProbeAnthropicCompat_NoKeyShortCircuits verifies the early-return
// when the API key is empty — should not make any HTTP call at all.
func TestProbeAnthropicCompat_NoKeyShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	info := &ProviderHealthInfo{Provider: "test"}
	probeAnthropicCompat(info, "test", "", srv.URL)
	if called {
		t.Error("expected no HTTP call when API key is empty")
	}
	if info.Error == "" {
		t.Error("expected an Error message")
	}
}

// TestCheckProviderHealth_OllamaDefaultURL verifies the empty-URL fallback
// to localhost:11434 — important so users who haven't configured Ollama
// don't see "no URL set"; instead they see "unreachable" if their local
// Ollama isn't running.
func TestCheckProviderHealth_OllamaDefaultURL(t *testing.T) {
	s := newStudioForTest(t)
	s.config.Settings.OllamaURL = "" // empty → default
	info := s.CheckProviderHealth("ollama")
	// Either OK (if user has Ollama running on the default port) or
	// unreachable. Either way, Endpoint should reflect the default URL.
	if !strings.Contains(info.Endpoint, "11434") {
		t.Errorf("expected default port 11434 in endpoint, got %q", info.Endpoint)
	}
}

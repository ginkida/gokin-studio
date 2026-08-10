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

func TestCheckProviderHealthWithKey_DoesNotRequirePersistedKey(t *testing.T) {
	s := newStudioForTest(t)
	info := s.CheckProviderHealthWithKey("glm", "")
	if info.OK || !strings.Contains(info.Error, "no API key") {
		t.Fatalf("expected explicit empty key to fail before probing, got %+v", info)
	}
}

func TestCheckProviderHealthWithKey_RejectsUnsupportedProvider(t *testing.T) {
	s := newStudioForTest(t)
	info := s.CheckProviderHealthWithKey("openai", "secret")
	if info.OK || !strings.Contains(info.Error, "unknown provider") {
		t.Fatalf("expected unsupported provider error, got %+v", info)
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

func TestProbe_DiscoversOnlyAllowlistedModelsInCatalogOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id":"kimi-for-coding"},
				{"id":"unrelated-provider-model"},
				{"id":"k3-256k"},
				{"id":"k3"}
			]
		}`))
	}))
	defer srv.Close()

	info := &ProviderHealthInfo{Provider: "kimi"}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	doProbe(info, req)
	if !info.OK {
		t.Fatalf("probe failed: %+v", info)
	}
	want := []string{"k3", "k3-256k", "kimi-for-coding"}
	if strings.Join(info.AvailableModels, ",") != strings.Join(want, ",") {
		t.Fatalf("AvailableModels = %v, want %v", info.AvailableModels, want)
	}
	if info.RecommendedModel != "k3" {
		t.Fatalf("RecommendedModel = %q, want k3", info.RecommendedModel)
	}
}

func TestPopulateAvailableStudioModels_AcceptsGLMOneMillionAlias(t *testing.T) {
	info := &ProviderHealthInfo{Provider: "glm"}
	populateAvailableStudioModels(info, []byte(`{"models":["glm-4.7",{"name":"glm-5.2[1m]"}]}`))
	if len(info.AvailableModels) != 2 || info.AvailableModels[0] != "glm-5.2" || info.AvailableModels[1] != "glm-4.7" {
		t.Fatalf("AvailableModels = %v", info.AvailableModels)
	}
	if info.RecommendedModel != "glm-5.2" {
		t.Fatalf("RecommendedModel = %q", info.RecommendedModel)
	}
}

func TestPopulateAvailableStudioModels_MalformedResponseIsNonFatal(t *testing.T) {
	info := &ProviderHealthInfo{Provider: "kimi", OK: true}
	populateAvailableStudioModels(info, []byte(`not-json`))
	if !info.OK || len(info.AvailableModels) != 0 || info.RecommendedModel != "" {
		t.Fatalf("malformed optional discovery changed health: %+v", info)
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

// TestProbeAnthropicCompat_AgainstFakeServer drives probeAnthropicCompat
// directly (it's package-private) so we can exercise the auth header
// + endpoint construction without hitting the real GLM/Kimi API.
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

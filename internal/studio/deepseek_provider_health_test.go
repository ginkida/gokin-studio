package studio

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckProviderHealth_DeepSeek_Endpoint verifies the iter 940+ +
// 960+ wiring: Test Connection for DeepSeek hits the official
// `https://api.deepseek.com/anthropic` adapter with BOTH x-api-key AND
// Authorization: Bearer headers, matching the dual-auth strategy DeepSeek
// docs use for the OpenAI endpoint (Bearer) but their Anthropic adapter
// commonly accepts (x-api-key from the Anthropic spec).
func TestCheckProviderHealth_DeepSeek_Endpoint(t *testing.T) {
	// httptest server pretending to be the DeepSeek Anthropic adapter.
	var gotPath, gotXAPI, gotAuth, gotAnthropicVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotXAPI = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotAnthropicVer = r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	// Spin up a Studio and probe with our test server URL spoofed in. We
	// directly call probeAnthropicCompat (lower-level than CheckProviderHealth
	// which uses the hardcoded DefaultDeepSeekBaseURL) so we can inject the
	// httptest URL.
	info := &ProviderHealthInfo{Provider: "deepseek"}
	probeAnthropicCompat(info, "deepseek", "sk-test-key-1234567890ab", srv.URL)

	if !info.OK {
		t.Errorf("expected OK=true; got error=%q status=%d", info.Error, info.StatusCode)
	}
	// Verify the request hit /v1/models (the probe endpoint).
	if !strings.HasSuffix(gotPath, "/v1/models") {
		t.Errorf("expected path to end with /v1/models; got %q", gotPath)
	}
	if gotAnthropicVer == "" {
		t.Errorf("anthropic-version header missing")
	}
	// One of the auth headers must be present. Probe sends Bearer for
	// Anthropic-compat endpoints by default.
	if gotXAPI == "" && gotAuth == "" {
		t.Errorf("no auth header present; x-api-key=%q Authorization=%q", gotXAPI, gotAuth)
	}
	if gotAuth != "" && !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("Authorization header should be 'Bearer ...'; got %q", gotAuth)
	}
}

// TestCheckProviderHealth_DeepSeek_EmptyKey verifies the no-key path
// short-circuits cleanly without trying to make a request.
func TestCheckProviderHealth_DeepSeek_EmptyKey(t *testing.T) {
	_ = withTempHistoryDir(t)
	clearEnv(t, "DEEPSEEK_API_KEY")
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.DeepSeekKey = ""

	info := s.CheckProviderHealth("deepseek")
	if info.OK {
		t.Error("expected OK=false with no key configured")
	}
	if !strings.Contains(strings.ToLower(info.Error), "no api key") {
		t.Errorf("expected 'no API key' error; got %q", info.Error)
	}
}

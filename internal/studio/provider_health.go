package studio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// ProviderHealthInfo is the JSON-friendly result of a provider connectivity
// probe. Fields are populated best-effort: OK=true means the probe got a
// 2xx response; 401/403/4xx/5xx populate the Error field with a humanised
// message but still report StatusCode for diagnosis.
type ProviderHealthInfo struct {
	Provider    string `json:"provider"`
	OK          bool   `json:"ok"`
	LatencyMs   int64  `json:"latencyMs,omitempty"`
	StatusCode  int    `json:"statusCode,omitempty"`
	Error       string `json:"error,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"` // URL we hit, for the user to verify in their head
	Description string `json:"description,omitempty"` // friendly description (e.g. "API key valid · 12 ms")
}

// healthCheckTimeout caps each probe so a hung provider can't block the
// Settings UI. 5s matches typical "is this server up" expectations and is
// long enough for trans-continental latency without being annoying.
const healthCheckTimeout = 5 * time.Second

// CheckProviderHealth probes a provider's API to verify connectivity and
// authentication. The probe varies per provider:
//   - GLM, MiniMax, Kimi (Anthropic-compatible): GET /v1/models with Bearer
//     auth. Cheapest authenticated call that doesn't consume tokens.
//   - Ollama (local): GET /api/tags. No auth.
//
// Status code interpretation:
//   - 2xx → OK (auth + server reachable)
//   - 401 → "invalid API key" (most useful signal — the user mistyped)
//   - 403 → "forbidden" (key is valid but lacks permission for this endpoint)
//   - 404 → "endpoint not found" (treat as reachable — the API key likely works,
//      just this endpoint isn't implemented; some Anthropic-compatible providers
//      omit /v1/models)
//   - 4xx other → "client error: <code>"
//   - 5xx → "server error: <code>"
//   - connection refused / timeout → "unreachable" / "timeout"
func (s *Studio) CheckProviderHealth(provider string) *ProviderHealthInfo {
	info := &ProviderHealthInfo{Provider: provider}

	s.mu.RLock()
	settings := s.config.Settings
	s.mu.RUnlock()

	// iter 780+: resolve via ResolveProviderKey so env vars (GLM_API_KEY etc.)
	// fall back when the Settings field is empty, matching what initClient
	// does at send-time. Otherwise an env-only user gets a misleading
	// "no API key configured" from the Test Connection button despite
	// chats working fine.
	switch provider {
	case "ollama":
		url, _ := ResolveProviderKey("ollama", settings)
		base := strings.TrimRight(url, "/")
		probeOllama(info, base)
	case "glm":
		key, _ := ResolveProviderKey("glm", settings)
		probeAnthropicCompat(info, "glm", key, client.DefaultGLMBaseURL)
	case "minimax":
		key, _ := ResolveProviderKey("minimax", settings)
		probeAnthropicCompat(info, "minimax", key, client.DefaultMiniMaxBaseURL)
	case "kimi":
		key, _ := ResolveProviderKey("kimi", settings)
		probeAnthropicCompat(info, "kimi", key, client.DefaultKimiBaseURL)
	case "deepseek":
		key, _ := ResolveProviderKey("deepseek", settings)
		probeAnthropicCompat(info, "deepseek", key, client.DefaultDeepSeekBaseURL)
	default:
		info.Error = fmt.Sprintf("unknown provider: %s", provider)
	}
	return info
}

// probeAnthropicCompat hits `<base>/v1/models` with Bearer auth. Used for
// every Anthropic-compatible cloud provider GLM, MiniMax, Kimi).
func probeAnthropicCompat(info *ProviderHealthInfo, provider, apiKey, baseURL string) {
	if apiKey == "" {
		info.Error = "no API key configured"
		info.Description = "Set the API key in Settings before testing"
		return
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/models"
	info.Endpoint = endpoint

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		info.Error = "bad request: " + err.Error()
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	// Some providers want x-api-key too (Anthropic native). Set both — extras are ignored.
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	_ = provider // unused; kept for parity with probeOllama
	doProbe(info, req)
}

// probeOllama hits `<base>/api/tags`. No auth.
func probeOllama(info *ProviderHealthInfo, baseURL string) {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/tags"
	info.Endpoint = endpoint
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		info.Error = "bad request: " + err.Error()
		return
	}
	doProbe(info, req)
}

// doProbe performs the HTTP call with a timeout, populates info fields,
// and translates the result into a humanised description.
func doProbe(info *ProviderHealthInfo, req *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	httpClient := &http.Client{Timeout: healthCheckTimeout}
	start := time.Now()
	resp, err := httpClient.Do(req)
	info.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		// Distinguish timeout from connection error so the user knows
		// whether to check the URL or just retry.
		if ctx.Err() == context.DeadlineExceeded {
			info.Error = "timeout after " + healthCheckTimeout.String()
			info.Description = "Server didn't respond within " + healthCheckTimeout.String() + " — check the URL or your network"
		} else {
			info.Error = "unreachable: " + err.Error()
			info.Description = "Couldn't connect — check the base URL is correct"
		}
		return
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused (and so a misbehaving
	// server doesn't leave us with a half-read TCP stream).
	_, _ = io.Copy(io.Discard, resp.Body)

	info.StatusCode = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		info.OK = true
		info.Description = fmt.Sprintf("OK · %d ms", info.LatencyMs)
	case resp.StatusCode == 401:
		info.Error = "invalid API key"
		info.Description = "Server says the API key is wrong — re-check it in the provider dashboard"
	case resp.StatusCode == 403:
		info.Error = "forbidden"
		info.Description = "API key was accepted but lacks permission for this endpoint"
	case resp.StatusCode == 404:
		// Some Anthropic-compatible providers don't expose /v1/models.
		// Treat as "auth probably worked, server up": the chat endpoint
		// is the real test path and that we exercise on first send.
		info.OK = true
		info.Description = fmt.Sprintf("Reachable (%d ms) · /v1/models not implemented; chat endpoint may still work", info.LatencyMs)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		info.Error = fmt.Sprintf("client error: HTTP %d", resp.StatusCode)
		info.Description = "Server rejected the request — check the base URL points to the right API"
	case resp.StatusCode >= 500:
		info.Error = fmt.Sprintf("server error: HTTP %d", resp.StatusCode)
		info.Description = "Provider is having trouble — try again in a moment"
	default:
		info.Error = fmt.Sprintf("unexpected HTTP %d", resp.StatusCode)
	}
}

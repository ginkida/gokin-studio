package studio

import (
	"context"
	"encoding/json"
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
	Endpoint    string `json:"endpoint,omitempty"`    // URL we hit, for the user to verify in their head
	Description string `json:"description,omitempty"` // friendly description (e.g. "API key valid · 12 ms")
	// AvailableModels contains only IDs from Studio's GLM/Kimi allowlist that
	// the authenticated /v1/models response actually advertised. An empty list
	// means discovery was unavailable, not that every model is forbidden.
	AvailableModels  []string `json:"availableModels,omitempty"`
	RecommendedModel string   `json:"recommendedModel,omitempty"`
}

// healthCheckTimeout caps each probe so a hung provider can't block the
// Settings UI. 5s matches typical "is this server up" expectations and is
// long enough for trans-continental latency without being annoying.
const healthCheckTimeout = 5 * time.Second
const studioModelDiscoveryTTL = 15 * time.Minute

// CheckProviderHealth probes a provider's API to verify connectivity and
// authentication. GLM and Kimi use an authenticated GET /v1/models probe,
// which does not consume completion tokens.
//
// Status code interpretation:
//   - 2xx → OK (auth + server reachable)
//   - 401 → "invalid API key" (most useful signal — the user mistyped)
//   - 403 → "forbidden" (key is valid but lacks permission for this endpoint)
//   - 404 → "endpoint not found" (treat as reachable — the API key likely works,
//     just this endpoint isn't implemented; some Anthropic-compatible providers
//     omit /v1/models)
//   - 4xx other → "client error: <code>"
//   - 5xx → "server error: <code>"
//   - connection refused / timeout → "unreachable" / "timeout"
func (s *Studio) CheckProviderHealth(provider string) *ProviderHealthInfo {
	s.mu.RLock()
	settings := defaultConfig().Settings
	if s.config != nil {
		settings = s.config.Settings
	}
	s.mu.RUnlock()

	// iter 780+: resolve via ResolveProviderKey so env vars (GLM_API_KEY etc.)
	// fall back when the Settings field is empty, matching what initClient
	// does at send-time. Otherwise an env-only user gets a misleading
	// "no API key configured" from the Test Connection button despite
	// chats working fine.
	var info *ProviderHealthInfo
	switch provider {
	case "glm":
		key, _ := ResolveProviderKey("glm", settings)
		info = checkProviderHealthWithKey("glm", key)
	case "kimi":
		key, _ := ResolveProviderKey("kimi", settings)
		info = checkProviderHealthWithKey("kimi", key)
	default:
		info = checkProviderHealthWithKey(provider, "")
	}
	s.rememberAvailableStudioModels(info)
	return info
}

// CheckProviderHealthWithKey probes the credential currently present in the
// UI without persisting it first. This keeps "Test connection" honest when a
// user has edited a key but has not clicked Save yet.
func (s *Studio) CheckProviderHealthWithKey(provider, apiKey string) *ProviderHealthInfo {
	info := checkProviderHealthWithKey(provider, apiKey)
	s.rememberAvailableStudioModels(info)
	return info
}

func (s *Studio) rememberAvailableStudioModels(info *ProviderHealthInfo) {
	if info == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !info.OK || len(info.AvailableModels) == 0 {
		delete(s.discoveredModels, info.Provider)
		delete(s.discoveredModelsAt, info.Provider)
		return
	}
	if s.discoveredModels == nil {
		s.discoveredModels = make(map[string]map[string]bool)
	}
	if s.discoveredModelsAt == nil {
		s.discoveredModelsAt = make(map[string]time.Time)
	}
	models := make(map[string]bool, len(info.AvailableModels))
	for _, model := range info.AvailableModels {
		if validateStudioProviderModel(info.Provider, model) == nil || isFutureStudioModelID(info.Provider, model) {
			models[model] = true
		}
	}
	s.discoveredModels[info.Provider] = models
	s.discoveredModelsAt[info.Provider] = time.Now()
}

func checkProviderHealthWithKey(provider, apiKey string) *ProviderHealthInfo {
	info := &ProviderHealthInfo{Provider: provider}
	apiKey = strings.TrimSpace(apiKey)
	switch provider {
	case "glm":
		probeAnthropicCompat(info, "glm", apiKey, client.DefaultGLMBaseURL)
	case "kimi":
		probeAnthropicCompat(info, "kimi", apiKey, client.DefaultKimiBaseURL)
	default:
		info.Error = fmt.Sprintf("unknown provider: %s", provider)
	}
	return info
}

// probeAnthropicCompat hits `<base>/v1/models` with Bearer auth. Used for
// both supported Anthropic-compatible cloud providers (GLM and Kimi).
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
	_ = provider // retained for readable call sites and future provider headers
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
	// Model catalogs are tiny, but keep the health endpoint bounded. Drain any
	// remainder so the HTTP connection can still be reused.
	const maxHealthBody = 1 << 20
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody+1))
	_, _ = io.Copy(io.Discard, resp.Body)

	info.StatusCode = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		info.OK = true
		info.Description = fmt.Sprintf("OK · %d ms", info.LatencyMs)
		if readErr == nil && len(body) <= maxHealthBody {
			populateAvailableStudioModels(info, body)
			if len(info.AvailableModels) > 0 {
				info.Description += fmt.Sprintf(" · %d Studio model(s) available", len(info.AvailableModels))
			}
		}
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

// populateAvailableStudioModels intersects an untrusted provider response with
// the product catalog. It never forwards arbitrary remote model IDs into the
// UI, preserving the GLM/Kimi-only boundary even if a gateway returns extras.
func populateAvailableStudioModels(info *ProviderHealthInfo, body []byte) {
	definition := providerDefinition(info.Provider)
	if definition == nil || len(body) == 0 {
		return
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	advertised := make(map[string]bool)
	advertisedOrder := make([]string, 0)
	collectModelIDs(payload, advertised, &advertisedOrder)
	known := make(map[string]bool, len(definition.Models))
	for _, model := range definition.Models {
		known[model] = true
		if advertised[model] || advertised[model+"[1m]"] {
			info.AvailableModels = append(info.AvailableModels, model)
		}
	}
	// Add provider-advertised future models that stay inside the strict GLM or
	// Kimi namespace. Newer numeric families lead the list and become the
	// recommendation; other safe variants follow the stable catalog.
	newer := make([]string, 0)
	other := make([]string, 0)
	seenExtra := make(map[string]bool)
	for _, raw := range advertisedOrder {
		model := strings.TrimSuffix(raw, "[1m]")
		if known[model] || seenExtra[model] || !isFutureStudioModelID(info.Provider, model) {
			continue
		}
		seenExtra[model] = true
		if modelVersionCompare(info.Provider, model, defaultModelForProvider(info.Provider)) > 0 {
			newer = append(newer, model)
		} else {
			other = append(other, model)
		}
	}
	info.AvailableModels = append(append(newer, info.AvailableModels...), other...)
	if len(info.AvailableModels) > 0 {
		info.RecommendedModel = info.AvailableModels[0]
	}
}

func collectModelIDs(payload any, out map[string]bool, order *[]string) {
	var list []any
	switch value := payload.(type) {
	case []any:
		list = value
	case map[string]any:
		for _, key := range []string{"data", "models"} {
			if candidate, ok := value[key].([]any); ok {
				list = append(list, candidate...)
			}
		}
	}
	for _, item := range list {
		add := func(id string) {
			id = strings.ToLower(strings.TrimSpace(id))
			if id == "" || out[id] {
				return
			}
			out[id] = true
			*order = append(*order, id)
		}
		switch value := item.(type) {
		case string:
			add(value)
		case map[string]any:
			for _, key := range []string{"id", "name", "model"} {
				if id, ok := value[key].(string); ok && strings.TrimSpace(id) != "" {
					add(id)
					break
				}
			}
		}
	}
}

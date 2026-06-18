package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/config"
	"github.com/ginkida/gokin-studio/internal/engine/logging"
	"github.com/ginkida/gokin-studio/internal/engine/security"
)

// defaultGLMThinkingBudget is the auto-default when the user hasn't configured
// a budget explicitly. 8192 is a middle-ground — enough for multi-step
// reasoning without inflating the per-turn cost. The old value (2048)
// truncated chain-of-thought on moderately complex tasks (ported from
// upstream gokin, which documents the same rationale). Users can override
// via the Settings → Thinking Budget slider.
const defaultGLMThinkingBudget int32 = 8192

// defaultKimiThinkingBudget mirrors the GLM default for Kimi Coding Plan
// (K2.6 / kimi-for-coding). The endpoint implements Anthropic Extended Thinking.
const defaultKimiThinkingBudget int32 = 8192

// defaultDeepSeekThinkingBudget is the auto-default for DeepSeek V4 Pro when
// the user enables thinking but hasn't tuned the budget in config.yaml.
const defaultDeepSeekThinkingBudget int32 = 8192

// defaultMiniMaxThinkingBudget is the auto-default for MiniMax M2.x thinking.
const defaultMiniMaxThinkingBudget int32 = 8192

// thinkingBudgetMin / thinkingBudgetMax are the API-enforced bounds for
// Anthropic-compat Extended Thinking. Requests outside [1024, 65536] get a
// provider 400 with a cryptic message.
const (
	thinkingBudgetMin int32 = 1024
	thinkingBudgetMax int32 = 65536
)

// normalizeThinkingBudget repairs a configured budget before an API call.
// 0 → autoDefault (unset — use provider default); any value outside
// [thinkingBudgetMin, thinkingBudgetMax] → autoDefault (hand-edited typo).
// A user who set "100" almost certainly meant "1000" — clamping to 1024
// would mask the slip; falling back to the auto-default is safer.
func normalizeThinkingBudget(budget, autoDefault int32) int32 {
	if budget == 0 {
		return autoDefault
	}
	if budget < thinkingBudgetMin || budget > thinkingBudgetMax {
		return autoDefault
	}
	return budget
}

// globalPool is the shared client connection pool.
var (
	globalPool *ClientPool
	poolMu     sync.Mutex
)

// GetPool returns the global client connection pool, creating it if necessary.
func GetPool(cfg *config.Config) *ClientPool {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool == nil {
		maxSize := cfg.Model.MaxPoolSize
		if maxSize <= 0 {
			maxSize = DefaultMaxPoolSize
		}
		globalPool = NewClientPool(maxSize)
	}
	return globalPool
}

// ClosePool closes the global client connection pool.
func ClosePool() {
	poolMu.Lock()
	defer poolMu.Unlock()
	if globalPool != nil {
		globalPool.Close()
		globalPool = nil
	}
}

// NewClient creates a client based on the configuration and model provider.
// This is the main entry point for client creation.
// If FallbackProviders are configured, returns a FallbackClient wrapping
// clients for the primary provider and each fallback provider.
// Uses the connection pool to reuse existing clients when possible.
func NewClient(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	return newClient(ctx, cfg, modelID, true)
}

// NewClientNoPool is like NewClient but NEVER uses the shared connection pool:
// every call returns a fresh, independently-owned client instance.
//
// The shared pool is keyed only by "provider:model", so two callers wanting
// the same provider+model receive the SAME client. That's a hazard for any
// caller that mutates per-owner state on the client instance (system
// instruction, tool set, status callback). Gokin Studio runs many concurrent
// projects, each with its own system prompt + pinned context that it sets on
// the client; with the shared pool, two projects on the same model (e.g. both
// on the default glm-5.1) alias to one client, so project B's
// SetSystemInstruction silently clobbers project A's — sending the wrong
// persona / leaking one project's pinned context into another. Studio already
// caches one client per project and rebuilds it on settings/provider changes,
// so it gains nothing from pooling. Such multi-tenant callers should use this.
func NewClientNoPool(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	return newClient(ctx, cfg, modelID, false)
}

// newClient is the shared implementation. usePool selects whether the global
// connection pool is consulted/populated (true) or bypassed entirely (false).
func newClient(ctx context.Context, cfg *config.Config, modelID string, usePool bool) (Client, error) {
	// Migrate configuration to new format
	config.MigrateConfig(cfg)

	// Normalize configuration
	if err := config.NormalizeConfig(cfg); err != nil {
		return nil, err
	}

	// If modelID is not specified, use default from config
	if modelID == "" {
		modelID = cfg.Model.Name
	}

	logging.Debug("creating client",
		"provider", cfg.Model.Provider,
		"modelID", modelID,
		"preset", cfg.Model.Preset,
		"pooled", usePool)

	// Determine the primary provider
	provider := cfg.Model.Provider
	if provider == "" {
		provider = cfg.API.Backend
	}

	// If fallback providers are configured, build a FallbackClient
	if len(cfg.Model.FallbackProviders) > 0 {
		return newFallbackClientFromConfig(ctx, cfg, provider, modelID, usePool)
	}

	return getOrCreateClient(ctx, cfg, provider, modelID, usePool)
}

// newFallbackClientFromConfig creates a FallbackClient with the primary provider
// and each configured fallback provider.
func newFallbackClientFromConfig(ctx context.Context, cfg *config.Config, primaryProvider, modelID string, usePool bool) (Client, error) {
	var clients []Client
	var clientProviders []string

	// Build candidate provider list (primary + configured fallbacks), then
	// reorder by dynamic health score so unhealthy providers are de-prioritized.
	candidateProviders := []string{}
	addProvider := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, existing := range candidateProviders {
			if existing == p {
				return
			}
		}
		candidateProviders = append(candidateProviders, p)
	}
	addProvider(primaryProvider)
	for _, fbProvider := range cfg.Model.FallbackProviders {
		addProvider(fbProvider)
	}

	orderedProviders := reorderProvidersByHealth(candidateProviders)

	// Create clients in health-prioritized order.
	for _, provider := range orderedProviders {
		c, err := getOrCreateClient(ctx, cfg, provider, modelID, usePool)
		if err != nil {
			logging.Warn("failed to create fallback chain client",
				"provider", provider,
				"error", err.Error())
			continue
		}
		clients = append(clients, c)
		clientProviders = append(clientProviders, provider)
	}

	if len(clients) == 0 {
		return nil, fmt.Errorf("failed to create any client: primary provider %q and all fallback providers failed", primaryProvider)
	}

	return NewFallbackClient(clients, clientProviders)
}

// getOrCreateClient retrieves a client from the pool or creates a new one.
// When usePool is false the pool is bypassed entirely and a fresh, dedicated
// instance is returned (see NewClientNoPool).
func getOrCreateClient(ctx context.Context, cfg *config.Config, provider, modelID string, usePool bool) (Client, error) {
	if !usePool {
		return createClientForProvider(ctx, cfg, provider, modelID)
	}

	pool := GetPool(cfg)

	// Check pool first
	if c, ok := pool.Get(provider, modelID); ok {
		return c, nil
	}

	// Create new client
	c, err := createClientForProvider(ctx, cfg, provider, modelID)
	if err != nil {
		return nil, err
	}

	// Store in pool for reuse
	pool.Put(provider, modelID, c)

	return c, nil
}

// createClientForProvider creates a new client for the given provider.
func createClientForProvider(ctx context.Context, cfg *config.Config, provider, modelID string) (Client, error) {
	switch provider {
	case "glm":
		return newGLMClient(cfg, modelID)
	case "deepseek":
		return newDeepSeekClient(cfg, modelID)
	case "minimax":
		return newMiniMaxClient(cfg, modelID)
	case "kimi":
		return newKimiClient(cfg, modelID)
	case "gemini":
		// Check OAuth first
		if cfg.API.HasOAuthToken("gemini") {
			logging.Debug("using Gemini OAuth client", "email", cfg.API.GeminiOAuth.Email)
			oauthClient, err := NewGeminiOAuthClient(ctx, cfg)
			if err == nil {
				return oauthClient, nil
			}

			logging.Warn("failed to initialize Gemini OAuth client, falling back to API key if available", "error", err)

			// Graceful fallback: if OAuth is stale/broken but API key exists, continue with API key client.
			apiClient, keyErr := NewGeminiClient(ctx, cfg)
			if keyErr == nil {
				return apiClient, nil
			}

			return nil, fmt.Errorf("gemini auth failed (oauth error: %v; api-key fallback error: %v)", err, keyErr)
		}
		return NewGeminiClient(ctx, cfg)
	case "anthropic":
		return newAnthropicNativeClient(cfg, modelID)
	case "openai":
		if cfg.API.HasOAuthToken("openai") {
			logging.Debug("using OpenAI OAuth client", "email", cfg.API.OpenAIOAuth.Email)
			return NewOpenAIOAuthClient(ctx, cfg)
		}
		return nil, fmt.Errorf("OpenAI requires OAuth login. Use /oauth-login openai to authenticate with your ChatGPT account")
	case "ollama":
		return newOllamaClient(cfg, modelID)
	default:
		// Fallback to auto-detection from model name
		return autoDetectClient(ctx, cfg, modelID)
	}
}

// autoDetectClient attempts to create a client by detecting the provider from the model name.
func autoDetectClient(ctx context.Context, cfg *config.Config, modelID string) (Client, error) {
	logging.Debug("unknown provider, auto-detecting from model name", "modelID", modelID)

	provider := config.DetectProviderFromModel(modelID)
	return createClientForProvider(ctx, cfg, provider, modelID)
}

func resolveProviderTimeouts(cfg *config.Config, provider string, defaultStreamIdle, defaultHTTP time.Duration) (time.Duration, time.Duration) {
	streamIdleTimeout := defaultStreamIdle
	if cfg.API.Retry.StreamIdleTimeout > 0 {
		streamIdleTimeout = cfg.API.Retry.StreamIdleTimeout
	}
	httpTimeout := defaultHTTP
	if cfg.API.Retry.HTTPTimeout > 0 {
		httpTimeout = cfg.API.Retry.HTTPTimeout
	}
	if provider != "" && len(cfg.API.Retry.Providers) > 0 {
		if override, ok := cfg.API.Retry.Providers[strings.ToLower(strings.TrimSpace(provider))]; ok {
			if override.StreamIdleTimeout > 0 {
				streamIdleTimeout = override.StreamIdleTimeout
			}
			if override.HTTPTimeout > 0 {
				httpTimeout = override.HTTPTimeout
			}
		}
	}
	return streamIdleTimeout, httpTimeout
}

// newGLMClient creates a GLM (GLM-4.7) client using Anthropic-compatible API.
func newGLMClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("glm")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for glm")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default GLM endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultGLMBaseURL
	}

	// GLM/Z.AI needs longer timeouts — server is slower than Anthropic.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "glm", 180*time.Second, 5*time.Minute)

	// GLM 4.7+ supports extended thinking — enable by default if user hasn't explicitly configured it
	enableThinking := cfg.Model.EnableThinking
	thinkingBudget := cfg.Model.ThinkingBudget
	if !enableThinking && thinkingBudget == 0 && supportsGLMThinking(modelID) {
		enableThinking = true
		thinkingBudget = defaultGLMThinkingBudget
	}
	// Repair any out-of-range budget (hand-edited config.yaml typo) so we
	// don't send a value the provider will reject with a cryptic 400.
	if enableThinking {
		thinkingBudget = normalizeThinkingBudget(thinkingBudget, defaultGLMThinkingBudget)
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    enableThinking,
		ThinkingBudget:    thinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "glm",
	}

	return NewAnthropicClient(anthropicConfig)
}

// supportsGLMThinking returns true for GLM models that support extended thinking.
func supportsGLMThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	return strings.HasPrefix(m, "glm-5") || strings.HasPrefix(m, "glm-4.7")
}

// SupportsKimiThinking returns true for Kimi models that implement Extended
// Thinking on the Coding Plan endpoint. kimi-for-coding and the kimi-k2* prefix
// cover current and future K2.x variants. (Ported from gokin.)
func SupportsKimiThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	return strings.HasPrefix(m, "kimi-for-coding") ||
		strings.HasPrefix(m, "kimi-k2")
}

// SupportsDeepSeekThinking reports whether a DeepSeek model supports Extended
// Thinking. V4 (pro + flash) and the legacy reasoner do; deepseek-chat does
// not — it's a pure chat model and the API rejects thinking blocks on that route.
// (Ported from gokin; empirically confirmed April 2026.)
func SupportsDeepSeekThinking(modelID string) bool {
	m := strings.ToLower(modelID)
	if m == "deepseek-chat" {
		return false
	}
	return strings.HasPrefix(m, "deepseek-v4") ||
		strings.HasPrefix(m, "deepseek-reasoner") ||
		m == "deepseek"
}

// newDeepSeekClient creates a DeepSeek client using Anthropic-compatible API.
func newDeepSeekClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("deepseek")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for deepseek")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default DeepSeek endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultDeepSeekBaseURL
	}

	// DeepSeek may have long silent reasoning/tool phases on complex prompts.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "deepseek", 120*time.Second, 5*time.Minute)

	dsThinkingBudget := cfg.Model.ThinkingBudget
	if cfg.Model.EnableThinking {
		dsThinkingBudget = normalizeThinkingBudget(dsThinkingBudget, defaultDeepSeekThinkingBudget)
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    dsThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "deepseek",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newMiniMaxClient creates a MiniMax client using Anthropic-compatible API.
func newMiniMaxClient(cfg *config.Config, modelID string) (Client, error) {
	p := config.GetProvider("minimax")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for minimax")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultMiniMaxBaseURL
	}

	// MiniMax may have long silent reasoning/tool phases.
	// Use relaxed defaults unless user explicitly configured stricter values.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "minimax", 120*time.Second, 5*time.Minute)

	mmThinkingBudget := cfg.Model.ThinkingBudget
	if cfg.Model.EnableThinking {
		mmThinkingBudget = normalizeThinkingBudget(mmThinkingBudget, defaultMiniMaxThinkingBudget)
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    mmThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		MaxRetries:        0, // Request retries are orchestrated at App layer.
		RetryDelay:        cfg.API.Retry.RetryDelay,
		HTTPTimeout:       httpTimeout,
		Provider:          "minimax",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newKimiClient creates a Kimi Code client using Anthropic-compatible API.
func newKimiClient(cfg *config.Config, modelID string) (Client, error) {
	p := config.GetProvider("kimi")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for kimi")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultKimiBaseURL
	}

	// Kimi may pause longer between chunks on complex tool chains.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "kimi", 120*time.Second, 5*time.Minute)

	kimiThinkingBudget := cfg.Model.ThinkingBudget
	if cfg.Model.EnableThinking {
		kimiThinkingBudget = normalizeThinkingBudget(kimiThinkingBudget, defaultKimiThinkingBudget)
	}

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    kimiThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		MaxRetries:        0, // Request retries are orchestrated at App layer.
		RetryDelay:        cfg.API.Retry.RetryDelay,
		HTTPTimeout:       httpTimeout,
		Provider:          "kimi",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newAnthropicNativeClient creates a native Anthropic client for Claude models.
func newAnthropicNativeClient(cfg *config.Config, modelID string) (Client, error) {
	// Load API key from environment or config via registry
	p := config.GetProvider("anthropic")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for anthropic")
	}
	legacyKey := ""
	if p.UsesLegacyKey {
		legacyKey = cfg.API.APIKey
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), legacyKey)

	if !loadedKey.IsSet() {
		return nil, fmt.Errorf("%s API key required (set %s environment variable or use /login %s <key>)", p.DisplayName, p.EnvVars[0], p.Name)
	}

	// Log key source for debugging (without exposing the key)
	logging.Debug("loaded API key",
		"provider", p.Name,
		"source", loadedKey.Source,
		"model", modelID)

	// Validate key format
	if err := security.ValidateKeyFormat(loadedKey.Value); err != nil {
		return nil, fmt.Errorf("invalid %s API key: %w", p.DisplayName, err)
	}

	// Use custom base URL if provided, otherwise use default Anthropic endpoint
	baseURL := cfg.Model.CustomBaseURL
	if baseURL == "" {
		baseURL = DefaultAnthropicBaseURL
	}

	// Anthropic can also produce long silent phases with extended thinking/tools.
	streamIdleTimeout, httpTimeout := resolveProviderTimeouts(cfg, "anthropic", 120*time.Second, 5*time.Minute)

	anthropicConfig := AnthropicConfig{
		APIKey:            loadedKey.Value,
		BaseURL:           baseURL,
		Model:             modelID,
		MaxTokens:         cfg.Model.MaxOutputTokens,
		Temperature:       cfg.Model.Temperature,
		StreamEnabled:     true,
		EnableThinking:    cfg.Model.EnableThinking,
		ThinkingBudget:    cfg.Model.ThinkingBudget,
		StreamIdleTimeout: streamIdleTimeout,
		// Request retries are orchestrated at App layer.
		MaxRetries:  0,
		RetryDelay:  cfg.API.Retry.RetryDelay,
		HTTPTimeout: httpTimeout,
		Provider:    "anthropic",
	}

	return NewAnthropicClient(anthropicConfig)
}

// newOllamaClient creates an Ollama client for local LLM inference.
func newOllamaClient(cfg *config.Config, modelID string) (Client, error) {
	// Load optional API key (for remote Ollama servers with auth)
	p := config.GetProvider("ollama")
	if p == nil {
		return nil, fmt.Errorf("provider registry missing entry for ollama")
	}
	loadedKey := security.GetProviderKey(p.EnvVars, p.GetKey(&cfg.API), "")

	// Log key source for debugging (without exposing the key)
	if loadedKey.IsSet() {
		logging.Debug("loaded Ollama API key",
			"source", loadedKey.Source,
			"model", modelID)
	}

	// Use custom base URL if provided, otherwise use default
	baseURL := cfg.API.OllamaBaseURL
	if baseURL == "" {
		baseURL = config.DefaultOllamaBaseURL
	}

	_, httpTimeout := resolveProviderTimeouts(cfg, "ollama", 0, config.DefaultHTTPTimeout)

	ollamaConfig := OllamaConfig{
		BaseURL:     baseURL,
		APIKey:      loadedKey.Value, // Optional
		Model:       modelID,
		Temperature: cfg.Model.Temperature,
		MaxTokens:   cfg.Model.MaxOutputTokens,
		HTTPTimeout: httpTimeout,
		MaxRetries:  0, // Request retries are orchestrated at App layer.
		RetryDelay:  cfg.API.Retry.RetryDelay,
	}

	return NewOllamaClient(ollamaConfig)
}

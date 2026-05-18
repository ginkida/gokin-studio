package studio

import (
	"os"
	"strings"
)

// providerEnvVar maps a provider ID to the environment variable that holds
// its API key (or, for Ollama, its base URL). README documents these names
// as the fallback when the in-app Settings value is empty.
var providerEnvVar = map[string]string{
	"glm":      "GLM_API_KEY",
	"minimax":  "MINIMAX_API_KEY",
	"kimi":     "KIMI_API_KEY",
	"deepseek": "DEEPSEEK_API_KEY",
	"ollama":   "OLLAMA_HOST",
}

// KeySource describes where a resolved API key came from, useful in
// diagnostics output ("API key set" vs "API key set (from $GLM_API_KEY)").
type KeySource string

const (
	KeySourceNone    KeySource = ""        // no key configured anywhere
	KeySourceSetting KeySource = "setting" // from in-app Settings (YAML)
	KeySourceEnv     KeySource = "env"     // from environment variable
	KeySourceDefault KeySource = "default" // built-in fallback (Ollama localhost)
)

// ResolveProviderKey returns the effective API key (or base URL for Ollama)
// for a provider, honouring the same precedence as initClient: in-app
// setting first, environment variable second, built-in default third.
// Returns the key plus a KeySource tag indicating where it came from.
//
// Centralised here so diagnostics, provider health, and any future caller
// agree on the resolution order without each duplicating the firstNonEmpty
// chain.
func ResolveProviderKey(provider string, settings Settings) (string, KeySource) {
	prov := strings.ToLower(strings.TrimSpace(provider))
	switch prov {
	case "glm":
		if v := strings.TrimSpace(settings.GLMKey); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("GLM_API_KEY")); v != "" {
			return v, KeySourceEnv
		}
		return "", KeySourceNone
	case "minimax":
		if v := strings.TrimSpace(settings.MiniMaxKey); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")); v != "" {
			return v, KeySourceEnv
		}
		return "", KeySourceNone
	case "kimi":
		if v := strings.TrimSpace(settings.KimiKey); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("KIMI_API_KEY")); v != "" {
			return v, KeySourceEnv
		}
		return "", KeySourceNone
	case "deepseek":
		if v := strings.TrimSpace(settings.DeepSeekKey); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); v != "" {
			return v, KeySourceEnv
		}
		return "", KeySourceNone
	case "ollama":
		// Ollama: URL, not key. Built-in localhost fallback exists, so
		// "none" really means "no configured URL anywhere" → falls back to
		// http://localhost:11434.
		if v := strings.TrimSpace(settings.OllamaURL); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); v != "" {
			return v, KeySourceEnv
		}
		return "http://localhost:11434", KeySourceDefault
	}
	return "", KeySourceNone
}

// envVarForProvider returns the readable env var name for a provider, or
// empty if the provider isn't recognised. Used by diagnostics to mention
// "(from $GLM_API_KEY)" in the OK message.
func envVarForProvider(provider string) string {
	return providerEnvVar[strings.ToLower(strings.TrimSpace(provider))]
}

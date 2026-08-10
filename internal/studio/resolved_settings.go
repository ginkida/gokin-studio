package studio

import (
	"os"
	"strings"
)

// providerEnvVar maps a provider ID to the environment variable that holds
// its API key. README documents these names as the fallback when the in-app
// Settings value is empty.
var providerEnvVar = map[string]string{
	"glm":  "GLM_API_KEY",
	"kimi": "KIMI_API_KEY",
}

// KeySource describes where a resolved API key came from, useful in
// diagnostics output ("API key set" vs "API key set (from $GLM_API_KEY)").
type KeySource string

const (
	KeySourceNone    KeySource = ""        // no key configured anywhere
	KeySourceSetting KeySource = "setting" // from in-app Settings (YAML)
	KeySourceEnv     KeySource = "env"     // from environment variable
)

// ResolveProviderKey returns the effective API key for a provider, honouring
// the same precedence as initClient: in-app setting first, environment
// variable second.
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
	case "kimi":
		if v := strings.TrimSpace(settings.KimiKey); v != "" {
			return v, KeySourceSetting
		}
		if v := strings.TrimSpace(os.Getenv("KIMI_API_KEY")); v != "" {
			return v, KeySourceEnv
		}
		return "", KeySourceNone
	}
	return "", KeySourceNone
}

// GetProviderCredentialSources exposes only whether each supported provider's
// effective credential comes from Settings or the process environment. It
// deliberately never returns the secret itself; the frontend only needs this
// metadata to avoid falsely blocking env-only GLM/Kimi users.
func (s *Studio) GetProviderCredentialSources() map[string]string {
	s.mu.RLock()
	settings := defaultConfig().Settings
	if s.config != nil {
		settings = s.config.Settings
	}
	s.mu.RUnlock()

	result := make(map[string]string, len(studioProviderCatalog))
	for _, provider := range studioProviderCatalog {
		_, source := ResolveProviderKey(provider.ID, settings)
		result[provider.ID] = string(source)
	}
	return result
}

// envVarForProvider returns the readable env var name for a provider, or
// empty if the provider isn't recognised. Used by diagnostics to mention
// "(from $GLM_API_KEY)" in the OK message.
func envVarForProvider(provider string) string {
	return providerEnvVar[strings.ToLower(strings.TrimSpace(provider))]
}

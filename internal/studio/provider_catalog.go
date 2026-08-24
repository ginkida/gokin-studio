package studio

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultStudioProvider = "glm"
	defaultStudioModel    = "glm-5.2"
)

// studioProviderCatalog is the product contract: Gokin Studio intentionally
// supports only GLM and Kimi. The lower-level engine still contains generic
// clients used by package tests and reusable code, but no other provider may
// enter the desktop runtime through config or an RPC binding.
var studioProviderCatalog = []ProviderInfo{
	{
		ID:   "glm",
		Name: "GLM (Z.AI)",
		Models: []string{
			"glm-5.2",
			"glm-5.1",
			"glm-5",
			"glm-5-turbo",
			"glm-4.7",
		},
		ModelDetails: []ProviderModelInfo{
			{
				ID: "glm-5.2", ContextWindow: 1_000_000,
				DefaultMaxOutputTokens: 131_072, MaxOutputTokens: 131_072,
				InputModalities: []string{"text"}, ReasoningControl: "high / max",
				Latest: true, Recommended: true,
				Description: "Latest GLM flagship · 1M context · long-horizon coding",
			},
			{
				ID: "glm-5.1", ContextWindow: 200_000,
				DefaultMaxOutputTokens: 65_536, MaxOutputTokens: 131_072,
				InputModalities: []string{"text"}, ReasoningControl: "thinking toggle",
				Description: "Previous GLM flagship · 200K context",
			},
			{
				ID: "glm-5", ContextWindow: 200_000,
				DefaultMaxOutputTokens: 65_536, MaxOutputTokens: 131_072,
				InputModalities: []string{"text"}, ReasoningControl: "thinking toggle",
				Description: "Agentic coding · 200K context",
			},
			{
				ID: "glm-5-turbo", ContextWindow: 200_000,
				DefaultMaxOutputTokens: 65_536, MaxOutputTokens: 131_072,
				InputModalities: []string{"text"}, ReasoningControl: "thinking toggle",
				Description: "Faster GLM 5 tier · 200K context",
			},
			{
				ID: "glm-4.7", ContextWindow: 200_000,
				DefaultMaxOutputTokens: 65_536, MaxOutputTokens: 131_072,
				InputModalities: []string{"text"}, ReasoningControl: "thinking toggle",
				Description: "Stable coding model · 200K context",
			},
		},
	},
	{
		ID:   "kimi",
		Name: "Kimi Code",
		Models: []string{
			"k3",
			"k3-256k",
			"kimi-for-coding",
			"kimi-for-coding-highspeed",
		},
		ModelDetails: []ProviderModelInfo{
			{
				ID: "k3", ContextWindow: 1_048_576,
				DefaultMaxOutputTokens: 131_072, MaxOutputTokens: 131_072,
				InputModalities: []string{"text", "image"}, ReasoningControl: "low / high / max",
				Latest: true, Recommended: true,
				Description: "Latest Kimi flagship · 1M context · native vision",
			},
			{
				ID: "k3-256k", ContextWindow: 262_144,
				DefaultMaxOutputTokens: 131_072, MaxOutputTokens: 131_072,
				InputModalities: []string{"text", "image"}, ReasoningControl: "low / high / max",
				Description: "Kimi K3 · 256K context · lower quota use",
			},
			{
				ID: "kimi-for-coding", ContextWindow: 262_144,
				DefaultMaxOutputTokens: 32_768, MaxOutputTokens: 131_072,
				InputModalities: []string{"text", "image"}, ReasoningControl: "thinking budget",
				Description: "Kimi K2.7 Code · routine development",
			},
			{
				ID: "kimi-for-coding-highspeed", ContextWindow: 262_144,
				DefaultMaxOutputTokens: 32_768, MaxOutputTokens: 131_072,
				InputModalities: []string{"text", "image"}, ReasoningControl: "thinking budget",
				Description: "Kimi K2.7 Code · 5–6× faster output tier",
			},
		},
	},
}

var (
	// Future models are accepted only inside the two product namespaces. This
	// lets an authenticated /models response expose glm-5.3 or k4 without a
	// desktop release, while a gateway can never smuggle Claude/OpenAI IDs into
	// Studio. Keep the syntax intentionally narrow: lowercase API identifiers,
	// numeric model families, and simple alphanumeric variant suffixes.
	glmStudioModelID  = regexp.MustCompile(`^glm-[0-9]+(?:\.[0-9]+)*(?:-[a-z0-9]+)*$`)
	kimiStudioModelID = regexp.MustCompile(`^(?:k[0-9]+(?:-[a-z0-9]+)*|kimi-(?:for-coding|k[0-9]+)(?:[.-][a-z0-9]+)*)$`)
	glmModelVersion   = regexp.MustCompile(`^glm-([0-9]+(?:\.[0-9]+)*)`)
	kimiModelVersion  = regexp.MustCompile(`^(?:k|kimi-k)([0-9]+(?:\.[0-9]+)*)(?:-|$)`)
)

func providerDefinition(provider string) *ProviderInfo {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for i := range studioProviderCatalog {
		if studioProviderCatalog[i].ID == provider {
			return &studioProviderCatalog[i]
		}
	}
	return nil
}

func modelDefinition(provider, model string) *ProviderModelInfo {
	definition := providerDefinition(provider)
	if definition == nil {
		return nil
	}
	for i := range definition.ModelDetails {
		if definition.ModelDetails[i].ID == model {
			return &definition.ModelDetails[i]
		}
	}
	if !isFutureStudioModelID(provider, model) {
		return nil
	}

	// An account-advertised newer flagship inherits the current flagship's
	// transport-safe limits until explicit metadata ships. This avoids treating
	// a future GLM/Kimi release as a 4K unknown model while retaining the same
	// bounded output/context contract already used by its provider family.
	baseID := defaultModelForProvider(provider)
	for i := range definition.ModelDetails {
		if definition.ModelDetails[i].ID != baseID {
			continue
		}
		inferred := definition.ModelDetails[i]
		inferred.ID = model
		inferred.Latest = modelVersionCompare(provider, model, baseID) > 0
		inferred.Recommended = inferred.Latest
		inferred.Description = fmt.Sprintf("Account-advertised %s model · inferred flagship capabilities", strings.ToUpper(provider))
		return &inferred
	}
	return nil
}

// modelSupportsImageInput reports whether the catalog advertises image input
// for this exact model. The catalog is the single product contract, and it
// answers per MODEL — including account-advertised future ids, which inherit
// their family flagship's modalities. Callers must not re-derive the answer
// from the provider name: the frontend composer already reads
// inputModalities, and a second, provider-shaped copy of the same rule is a
// divergence waiting to happen the moment one family ships a model that
// differs from its siblings.
func modelSupportsImageInput(provider, model string) bool {
	definition := modelDefinition(provider, model)
	if definition == nil {
		return false
	}
	for _, modality := range definition.InputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

// imageCapableModelsHint names the catalog models that do accept image input,
// so a refusal can tell the user where to go. Derived rather than written out:
// the previous refusal hardcoded "Kimi Code models", which is a second copy of
// the modality table and goes stale the moment the lineup changes.
func imageCapableModelsHint() string {
	var groups []string
	for _, provider := range studioProviderCatalog {
		var capable []string
		for _, model := range provider.Models {
			if modelSupportsImageInput(provider.ID, model) {
				capable = append(capable, model)
			}
		}
		if len(capable) > 0 {
			groups = append(groups, fmt.Sprintf("%s (%s)", provider.Name, strings.Join(capable, ", ")))
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return " — models that do: " + strings.Join(groups, "; ")
}

func isStudioModelID(provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	switch provider {
	case "glm":
		return glmStudioModelID.MatchString(model)
	case "kimi":
		return kimiStudioModelID.MatchString(model)
	default:
		return false
	}
}

// isFutureStudioModelID distinguishes a genuinely forward-compatible model
// from an arbitrary unlisted legacy/experimental variant. Static catalog
// entries remain valid separately; dynamic IDs must be at least the current
// flagship generation so account discovery can surface them safely.
func isFutureStudioModelID(provider, model string) bool {
	return isStudioModelID(provider, model) &&
		modelVersionCompare(provider, model, defaultModelForProvider(provider)) >= 0
}

func modelVersionCompare(provider, left, right string) int {
	var matcher *regexp.Regexp
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "glm":
		matcher = glmModelVersion
	case "kimi":
		matcher = kimiModelVersion
	default:
		return 0
	}
	parse := func(value string) []int {
		match := matcher.FindStringSubmatch(strings.ToLower(strings.TrimSpace(value)))
		if len(match) < 2 {
			return nil
		}
		parts := strings.Split(match[1], ".")
		out := make([]int, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(left), parse(right)
	for i := 0; i < max(len(a), len(b)); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func defaultMaxOutputTokens(provider, model string) int32 {
	if definition := modelDefinition(provider, model); definition != nil {
		return definition.DefaultMaxOutputTokens
	}
	return 0
}

func maxOutputTokens(provider, model string) int32 {
	if definition := modelDefinition(provider, model); definition != nil {
		return definition.MaxOutputTokens
	}
	return 0
}

func defaultModelForProvider(provider string) string {
	if definition := providerDefinition(provider); definition != nil && len(definition.Models) > 0 {
		return definition.Models[0]
	}
	return defaultStudioModel
}

func validateStudioProviderModel(provider, model string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	definition := providerDefinition(provider)
	if definition == nil {
		return fmt.Errorf("unsupported provider %q: Gokin Studio supports only GLM and Kimi", provider)
	}
	for _, candidate := range definition.Models {
		if candidate == model {
			return nil
		}
	}
	return fmt.Errorf("unsupported model %q for provider %q", model, provider)
}

// validateStudioProviderModelRuntime accepts a forward-compatible model ID for
// internal execution and persisted configuration. Public mutations use the
// Studio-scoped validator below, which additionally requires account discovery
// (or an already-saved project/default) before accepting a new ID.
func validateStudioProviderModelRuntime(provider, model string) error {
	staticErr := validateStudioProviderModel(provider, model)
	if staticErr == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if providerDefinition(provider) == nil {
		return staticErr
	}
	if isFutureStudioModelID(provider, model) {
		return nil
	}
	return staticErr
}

func (s *Studio) validateAvailableStudioProviderModel(provider, model string) error {
	staticErr := validateStudioProviderModel(provider, model)
	if staticErr == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if providerDefinition(provider) == nil {
		return staticErr
	}
	if !isFutureStudioModelID(provider, model) {
		return staticErr
	}

	s.mu.RLock()
	if s.discoveredModels != nil && s.discoveredModels[provider][model] &&
		time.Since(s.discoveredModelsAt[provider]) <= studioModelDiscoveryTTL {
		s.mu.RUnlock()
		return nil
	}
	if s.config != nil && s.config.Settings.DefaultProvider == provider && s.config.Settings.DefaultModel == model {
		s.mu.RUnlock()
		return nil
	}
	for _, project := range s.projects {
		project.mu.RLock()
		matches := project.Provider == provider && project.Model == model
		project.mu.RUnlock()
		if matches {
			s.mu.RUnlock()
			return nil
		}
	}
	s.mu.RUnlock()
	return fmt.Errorf("model %q was not advertised for the connected %s account; test the API key before selecting it", model, strings.ToUpper(provider))
}

// normalizeStudioProviderModel repairs persisted legacy/hand-edited values.
// Unsupported providers move to the safe GLM default; retired aliases stay on
// their provider when a direct replacement exists.
func normalizeStudioProviderModel(provider, model string) (string, string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if providerDefinition(provider) == nil {
		return defaultStudioProvider, defaultStudioModel
	}

	switch provider {
	case "glm":
		switch model {
		case "", "glm-4-plus", "glm-4-air", "glm-4-flash", "glm-4-long", "glm-4.5":
			model = defaultModelForProvider(provider)
		}
	case "kimi":
		switch model {
		case "", "kimi-k2.5", "kimi-k2-thinking-turbo", "kimi-k2-turbo",
			"kimi-k2-turbo-preview", "kimi-latest", "moonshot-v1-auto",
			"moonshot-v1-128k", "moonshot-v1-32k", "moonshot-v1-8k":
			model = defaultModelForProvider(provider)
		}
	}

	if err := validateStudioProviderModelRuntime(provider, model); err != nil {
		model = defaultModelForProvider(provider)
	}
	return provider, model
}

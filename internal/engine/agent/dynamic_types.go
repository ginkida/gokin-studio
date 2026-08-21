package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxDynamicAgentNameBytes        = 64
	maxDynamicAgentDescriptionBytes = 4 << 10
	maxDynamicAgentPromptBytes      = 256 << 10
	maxDynamicAgentTools            = 256
	maxDynamicAgentToolNameBytes    = 128
)

// DynamicAgentType represents a user-defined agent type.
type DynamicAgentType struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	AllowedTools []string `json:"allowed_tools"`
	SystemPrompt string   `json:"system_prompt"`
	Priority     int      `json:"priority"` // Higher = evaluated first
}

// AgentTypeRegistry manages both built-in and dynamic agent types.
type AgentTypeRegistry struct {
	builtin map[AgentType]bool
	dynamic map[string]*DynamicAgentType
	mu      sync.RWMutex
}

// NewAgentTypeRegistry creates a new registry with built-in types.
func NewAgentTypeRegistry() *AgentTypeRegistry {
	return &AgentTypeRegistry{
		builtin: map[AgentType]bool{
			AgentTypeExplore: true,
			AgentTypeBash:    true,
			AgentTypeGeneral: true,
			AgentTypePlan:    true,
			AgentTypeGuide:   true,
		},
		dynamic: make(map[string]*DynamicAgentType),
	}
}

// RegisterDynamic registers a new dynamic agent type.
func (r *AgentTypeRegistry) RegisterDynamic(name, description string, tools []string, prompt string) error {
	name = normalizeDynamicAgentTypeName(name)
	if err := validateDynamicAgentTypeName(name); err != nil {
		return err
	}
	if err := validateDynamicAgentText("description", description, maxDynamicAgentDescriptionBytes); err != nil {
		return err
	}
	if err := validateDynamicAgentText("system prompt", prompt, maxDynamicAgentPromptBytes); err != nil {
		return err
	}
	if len(tools) > maxDynamicAgentTools {
		return fmt.Errorf("dynamic agent type may declare at most %d tools", maxDynamicAgentTools)
	}
	allowedTools := make([]string, 0, len(tools))
	seenTools := make(map[string]struct{}, len(tools))
	for _, toolName := range tools {
		toolName = strings.ToLower(strings.TrimSpace(toolName))
		if err := validateDynamicAgentToolName(toolName); err != nil {
			return err
		}
		if _, duplicate := seenTools[toolName]; duplicate {
			return fmt.Errorf("duplicate dynamic agent tool %q", toolName)
		}
		seenTools[toolName] = struct{}{}
		allowedTools = append(allowedTools, toolName)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check for conflict with built-in types
	if ParseAgentType(name) != "" {
		return fmt.Errorf("cannot override built-in agent type: %s", name)
	}
	if _, exists := r.dynamic[name]; exists {
		return fmt.Errorf("dynamic agent type already exists: %s", name)
	}

	r.dynamic[name] = &DynamicAgentType{
		Name:         name,
		Description:  description,
		AllowedTools: allowedTools,
		SystemPrompt: prompt,
	}

	return nil
}

// UnregisterDynamic removes a dynamic agent type.
func (r *AgentTypeRegistry) UnregisterDynamic(name string) error {
	name = normalizeDynamicAgentTypeName(name)
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.dynamic[name]; !ok {
		return fmt.Errorf("dynamic type not found: %s", name)
	}

	delete(r.dynamic, name)
	return nil
}

// GetDynamic returns a dynamic agent type by name.
func (r *AgentTypeRegistry) GetDynamic(name string) (*DynamicAgentType, bool) {
	name = normalizeDynamicAgentTypeName(name)
	r.mu.RLock()
	defer r.mu.RUnlock()

	dt, ok := r.dynamic[name]
	return cloneDynamicAgentType(dt), ok
}

// IsBuiltin checks if a type is a built-in type.
func (r *AgentTypeRegistry) IsBuiltin(name string) bool {
	return ParseAgentType(name) != ""
}

// IsDynamic checks if a type is a dynamic type.
func (r *AgentTypeRegistry) IsDynamic(name string) bool {
	name = normalizeDynamicAgentTypeName(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.dynamic[name]
	return ok
}

// Exists checks if a type (built-in or dynamic) exists.
func (r *AgentTypeRegistry) Exists(name string) bool {
	if ParseAgentType(name) != "" {
		return true
	}
	name = normalizeDynamicAgentTypeName(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dynamic[name] != nil
}

// ListDynamic returns all dynamic agent types.
func (r *AgentTypeRegistry) ListDynamic() []*DynamicAgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]*DynamicAgentType, 0, len(r.dynamic))
	for _, dt := range r.dynamic {
		types = append(types, cloneDynamicAgentType(dt))
	}
	sort.Slice(types, func(i, j int) bool {
		if types[i].Priority != types[j].Priority {
			return types[i].Priority > types[j].Priority
		}
		return types[i].Name < types[j].Name
	})
	return types
}

// ListAll returns all agent type names (both built-in and dynamic).
func (r *AgentTypeRegistry) ListAll() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.builtin)+len(r.dynamic))

	for t := range r.builtin {
		names = append(names, string(t))
	}
	for name := range r.dynamic {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// GetToolsForType returns the allowed tools for a type.
func (r *AgentTypeRegistry) GetToolsForType(name string) []string {
	// Check dynamic first
	if dt, ok := r.GetDynamic(name); ok {
		return append([]string(nil), dt.AllowedTools...)
	}

	// Fall back to built-in
	return ParseAgentType(name).AllowedTools()
}

func normalizeDynamicAgentTypeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func validateDynamicAgentTypeName(name string) error {
	if name == "" {
		return fmt.Errorf("dynamic agent type name must not be blank")
	}
	if len(name) > maxDynamicAgentNameBytes {
		return fmt.Errorf("dynamic agent type name must be at most %d bytes", maxDynamicAgentNameBytes)
	}
	for index, char := range name {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if index > 0 && ((char >= '0' && char <= '9') || char == '-' || char == '_') {
			continue
		}
		return fmt.Errorf("dynamic agent type name %q must start with a letter and contain only lowercase letters, digits, '-' or '_'", name)
	}
	return nil
}

func validateDynamicAgentToolName(name string) error {
	if name == "" || len(name) > maxDynamicAgentToolNameBytes {
		return fmt.Errorf("dynamic agent tool names must contain 1-%d bytes", maxDynamicAgentToolNameBytes)
	}
	for index, char := range name {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if index > 0 && ((char >= '0' && char <= '9') || char == '_' || char == '-') {
			continue
		}
		return fmt.Errorf("invalid dynamic agent tool name %q", name)
	}
	return nil
}

func validateDynamicAgentText(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxBytes {
		return fmt.Errorf("dynamic agent %s must be valid UTF-8 without NUL and at most %d bytes", field, maxBytes)
	}
	return nil
}

func cloneDynamicAgentType(agentType *DynamicAgentType) *DynamicAgentType {
	if agentType == nil {
		return nil
	}
	clone := *agentType
	clone.AllowedTools = append([]string(nil), agentType.AllowedTools...)
	return &clone
}

// GetPromptForType returns the system prompt for a type.
func (r *AgentTypeRegistry) GetPromptForType(name string) string {
	if dt, ok := r.GetDynamic(name); ok {
		return dt.SystemPrompt
	}
	return "" // Built-in types use their own prompt builders
}

// GetDescriptionForType returns the description for a type.
func (r *AgentTypeRegistry) GetDescriptionForType(name string) string {
	if dt, ok := r.GetDynamic(name); ok {
		return dt.Description
	}

	// Built-in descriptions
	switch ParseAgentType(name) {
	case AgentTypeExplore:
		return "Explore and analyze codebases"
	case AgentTypeBash:
		return "Execute shell commands"
	case AgentTypeGeneral:
		return "General-purpose agent with full tool access"
	case AgentTypePlan:
		return "Design implementation strategies"
	case AgentTypeGuide:
		return "Answer questions about the CLI"
	default:
		return ""
	}
}

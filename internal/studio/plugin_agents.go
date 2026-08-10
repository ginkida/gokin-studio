package studio

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

const maxPluginAgentTools = 64

type pluginAgentDefinition struct {
	Name           string
	Description    string
	Prompt         string
	Tools          []string
	Model          string
	PermissionMode string
}

type studioPluginAgentRunner struct {
	studio    *Studio
	projectID string
}

func enabledPluginAgentSpecs() []tools.PluginAgentSpec {
	pluginsMu.Lock()
	defer pluginsMu.Unlock()
	plugins, err := loadInstalledPluginsRaw()
	if err != nil {
		return nil
	}
	var specs []tools.PluginAgentSpec
	for _, plugin := range plugins {
		if !plugin.Enabled {
			continue
		}
		for _, agent := range plugin.Agents {
			specs = append(specs, tools.PluginAgentSpec{
				ID:          plugin.Name + ":" + agent.Name,
				Description: agent.Description,
			})
		}
	}
	return specs
}

func parsePluginAgentDefinition(content, fallbackName string) (pluginAgentDefinition, error) {
	if len(content) == 0 || len(content) > maxPluginTextBytes || !utf8.ValidString(content) {
		return pluginAgentDefinition{}, fmt.Errorf("agent definition must be UTF-8 text up to %d KiB", maxPluginTextBytes>>10)
	}
	content = strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\ufeff")
	fallbackName = strings.TrimSpace(fallbackName)
	def := pluginAgentDefinition{Name: fallbackName}
	body := strings.TrimSpace(content)
	if strings.HasPrefix(content, "---\n") {
		rest := content[4:]
		end := strings.Index(rest, "\n---\n")
		if end < 0 {
			return pluginAgentDefinition{}, fmt.Errorf("agent frontmatter is not terminated")
		}
		var meta struct {
			Name           string `yaml:"name"`
			Description    string `yaml:"description"`
			Tools          any    `yaml:"tools"`
			Model          string `yaml:"model"`
			PermissionMode string `yaml:"permissionMode"`
		}
		if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
			return pluginAgentDefinition{}, fmt.Errorf("parse agent frontmatter: %w", err)
		}
		if strings.TrimSpace(meta.Name) != "" {
			def.Name = strings.TrimSpace(meta.Name)
		}
		def.Description = strings.Join(strings.Fields(meta.Description), " ")
		def.Model = strings.TrimSpace(meta.Model)
		def.PermissionMode = strings.TrimSpace(meta.PermissionMode)
		parsedTools, err := parsePluginAgentTools(meta.Tools)
		if err != nil {
			return pluginAgentDefinition{}, err
		}
		def.Tools = parsedTools
		body = strings.TrimSpace(rest[end+5:])
	}
	if !pluginCommandNameRE.MatchString(def.Name) {
		return pluginAgentDefinition{}, fmt.Errorf("agent name must use lowercase letters, numbers, underscores, or hyphens")
	}
	if len([]rune(def.Description)) > 1000 || len(def.Model) > 100 || len(def.PermissionMode) > 100 {
		return pluginAgentDefinition{}, fmt.Errorf("agent metadata is too large")
	}
	if body == "" {
		return pluginAgentDefinition{}, fmt.Errorf("agent instructions cannot be empty")
	}
	if def.Description == "" {
		def.Description = pluginAgentDescription(body)
	}
	def.Prompt = body
	return def, nil
}

func parsePluginAgentTools(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var values []string
	switch typed := raw.(type) {
	case string:
		for _, value := range strings.Split(typed, ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
	case []any:
		for _, value := range typed {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("agent tools must be a string or list of strings")
			}
			values = append(values, strings.TrimSpace(text))
		}
	default:
		return nil, fmt.Errorf("agent tools must be a string or list of strings")
	}
	if len(values) > maxPluginAgentTools {
		return nil, fmt.Errorf("agent may declare at most %d tools", maxPluginAgentTools)
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(value) > 128 || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("invalid agent tool name")
		}
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func pluginAgentDescription(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line == "" {
			continue
		}
		return truncateUTF8(strings.Join(strings.Fields(line), " "), 240)
	}
	return "Plugin specialist"
}

func loadEnabledPluginAgentDefinition(agentID string) (string, pluginAgentDefinition, error) {
	pluginName, agentName, ok := strings.Cut(strings.TrimSpace(agentID), ":")
	if !ok || !pluginNameRE.MatchString(pluginName) || !pluginCommandNameRE.MatchString(agentName) {
		return "", pluginAgentDefinition{}, fmt.Errorf("invalid plugin agent identifier")
	}
	pluginsMu.Lock()
	plugins, err := loadInstalledPluginsRaw()
	pluginsMu.Unlock()
	if err != nil {
		return "", pluginAgentDefinition{}, err
	}
	var component *PluginComponentInfo
	for _, plugin := range plugins {
		if plugin.Name != pluginName {
			continue
		}
		if !plugin.Enabled {
			return "", pluginAgentDefinition{}, fmt.Errorf("plugin %q is disabled", pluginName)
		}
		for i := range plugin.Agents {
			if plugin.Agents[i].Name == agentName {
				copy := plugin.Agents[i]
				component = &copy
				break
			}
		}
		break
	}
	if component == nil {
		return "", pluginAgentDefinition{}, fmt.Errorf("enabled plugin agent not found: %s", agentID)
	}
	root, err := safeInstalledPluginRoot(pluginName)
	if err != nil {
		return "", pluginAgentDefinition{}, err
	}
	data, err := readInstalledPluginFile(root, component.Path, maxPluginTextBytes)
	if err != nil {
		return "", pluginAgentDefinition{}, err
	}
	fallback := strings.TrimSuffix(path.Base(component.Path), path.Ext(component.Path))
	def, err := parsePluginAgentDefinition(string(data), fallback)
	if err != nil {
		return "", pluginAgentDefinition{}, err
	}
	if def.Name != component.Name {
		return "", pluginAgentDefinition{}, fmt.Errorf("plugin agent identity changed after installation")
	}
	return pluginName, def, nil
}

func (r *studioPluginAgentRunner) RunPluginAgent(ctx context.Context, agentID, task string) (tools.PluginAgentRunResult, error) {
	if r == nil || r.studio == nil {
		return tools.PluginAgentRunResult{}, fmt.Errorf("plugin agent runner is unavailable")
	}
	_, def, err := loadEnabledPluginAgentDefinition(agentID)
	if err != nil {
		return tools.PluginAgentRunResult{}, err
	}
	if err := validateRPCText("plugin agent task", task, ChatMessageMaxBytes, true); err != nil {
		return tools.PluginAgentRunResult{}, err
	}

	r.studio.mu.RLock()
	project := r.studio.projects[r.projectID]
	settings := r.studio.config.Settings
	r.studio.mu.RUnlock()
	if project == nil {
		return tools.PluginAgentRunResult{}, fmt.Errorf("project not found")
	}
	if err := project.initClient(settings); err != nil {
		return tools.PluginAgentRunResult{}, err
	}
	parentProjectID, parentSessionID := askUserRouting(ctx)
	project.mu.RLock()
	parentSession := project.sessions[parentSessionID]
	if parentProjectID != project.ID || parentSession == nil {
		parentSessionID = "default"
		parentSession = project.sessions[parentSessionID]
	}
	project.mu.RUnlock()
	if parentSession == nil {
		return tools.PluginAgentRunResult{}, fmt.Errorf("parent chat session not found")
	}
	project.mu.RLock()
	provider, model, permissionMode := project.Provider, project.Model, project.PermissionMode
	baseRegistry := project.registry
	project.mu.RUnlock()
	parentSession.mu.RLock()
	if parentSession.pluginAgentChild {
		parentSession.mu.RUnlock()
		return tools.PluginAgentRunResult{}, fmt.Errorf("recursive plugin agent delegation is blocked")
	}
	if parentSession.executionProvider != "" {
		provider = parentSession.executionProvider
	}
	if parentSession.executionModel != "" {
		model = parentSession.executionModel
	}
	if parentSession.executionPermissionMode != "" {
		permissionMode = parentSession.executionPermissionMode
	}
	parentSession.mu.RUnlock()
	if provider == "" {
		provider = settings.DefaultProvider
	}
	if model == "" {
		model = settings.DefaultModel
	}
	if err := validateStudioProviderModelRuntime(provider, model); err != nil {
		return tools.PluginAgentRunResult{}, err
	}

	allowed, unavailable := resolvePluginAgentTools(def.Tools, baseRegistry)
	agentPrompt := buildPluginAgentSystemPrompt(agentID, def, unavailable)
	session, err := createPluginAgentSession(
		project, parentSessionID, agentID, provider, model, permissionMode,
		agentPrompt, allowed,
	)
	if err != nil {
		return tools.PluginAgentRunResult{}, err
	}
	project.emitEvent(r.studio.ctx, EventSessionsChanged, map[string]any{
		"projectID": project.ID, "sessionID": session.ID,
	})
	if err := r.studio.startMessage(project.ID, task, nil, session.ID); err != nil {
		return tools.PluginAgentRunResult{}, err
	}
	response, err := waitForPluginAgent(ctx, session)
	if err != nil {
		return tools.PluginAgentRunResult{}, err
	}
	project.emitEvent(r.studio.ctx, EventSessionsChanged, map[string]any{
		"projectID": project.ID, "sessionID": session.ID,
	})
	return tools.PluginAgentRunResult{
		Agent: agentID, SessionID: session.ID,
		Response: tools.TruncateToolResultContent(response, ""),
	}, nil
}

func createPluginAgentSession(
	project *Project,
	parentSessionID, agentID, provider, model, permissionMode, agentPrompt string,
	allowedTools map[string]bool,
) (*ChatSession, error) {
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()
	name := truncateUTF8("Agent · "+agentID+" · "+time.Now().Format("Jan 02 15:04"), 120)
	session := NewChatSession(name)
	session.ParentID = parentSessionID
	session.executionProvider = provider
	session.executionModel = model
	session.executionPermissionMode = permissionMode
	session.executionSystemPrompt = agentPrompt
	session.executionAllowedTools = cloneBoolMap(allowedTools)
	session.pluginAgentChild = true

	project.mu.RLock()
	for {
		if _, exists := project.sessions[session.ID]; !exists {
			break
		}
		session = NewChatSession(name)
		session.ParentID = parentSessionID
		session.executionProvider = provider
		session.executionModel = model
		session.executionPermissionMode = permissionMode
		session.executionSystemPrompt = agentPrompt
		session.executionAllowedTools = cloneBoolMap(allowedTools)
		session.pluginAgentChild = true
	}
	project.mu.RUnlock()
	startDir, err := worktreeStartDirForParent(project, parentSessionID)
	if err != nil {
		return nil, err
	}
	if err := provisionSessionWorktree(project, session, startDir); err != nil {
		return nil, err
	}
	if err := SaveNewHistoryWithMetadata(
		projectSessionStorageKey(project.ID, session.ID), name, parentSessionID, nil,
	); err != nil {
		_ = removeSessionWorktree(project, session)
		return nil, fmt.Errorf("persist plugin agent session: %w", err)
	}
	project.mu.Lock()
	if _, exists := project.sessions[session.ID]; exists {
		project.mu.Unlock()
		_ = removeSessionWorktree(project, session)
		_ = deleteHistoryChecked(projectSessionStorageKey(project.ID, session.ID))
		return nil, fmt.Errorf("plugin agent session ID collision: %s", session.ID)
	}
	project.sessions[session.ID] = session
	project.mu.Unlock()
	return session, nil
}

func waitForPluginAgent(ctx context.Context, session *ChatSession) (string, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			session.Stop()
			return "", ctx.Err()
		case <-ticker.C:
			session.mu.RLock()
			running := session.queueWorker
			response := lastSavedModelText(session.history)
			session.mu.RUnlock()
			if running {
				continue
			}
			if strings.TrimSpace(response) == "" {
				return "", fmt.Errorf("child chat ended before a model response was saved")
			}
			return response, nil
		}
	}
}

func lastSavedModelText(history []*genai.Content) string {
	for i := len(history) - 1; i >= 0; i-- {
		content := history[i]
		if content == nil || content.Role != "model" {
			continue
		}
		var text strings.Builder
		for _, part := range content.Parts {
			if part != nil && !part.Thought && strings.TrimSpace(part.Text) != "" {
				text.WriteString(part.Text)
			}
		}
		if text.Len() > 0 {
			return text.String()
		}
	}
	return ""
}

func resolvePluginAgentTools(requested []string, registry *tools.Registry) (map[string]bool, []string) {
	if len(requested) == 0 {
		return nil, nil
	}
	available := make(map[string]string)
	if registry != nil {
		for _, name := range registry.Names() {
			available[strings.ToLower(name)] = name
		}
	}
	aliases := map[string]string{
		"read": "read", "write": "write", "edit": "edit", "bash": "bash",
		"glob": "glob", "grep": "grep", "webfetch": "web_fetch",
		"websearch": "web_search", "askuserquestion": "ask_user",
		"askuser": "ask_user", "todowrite": "todo", "todo": "todo",
		"pluginresource": "plugin_resource",
	}
	allowed := make(map[string]bool)
	var unavailable []string
	for _, requestedName := range requested {
		normalized := strings.ToLower(strings.TrimSpace(requestedName))
		normalized = strings.ReplaceAll(normalized, "-", "")
		normalized = strings.ReplaceAll(normalized, "_", "")
		candidate := aliases[normalized]
		if candidate == "" {
			candidate = strings.ToLower(strings.TrimSpace(requestedName))
		}
		if exact, ok := available[candidate]; ok && exact != "plugin_agent" {
			allowed[exact] = true
			continue
		}
		unavailable = append(unavailable, requestedName)
	}
	return allowed, unavailable
}

func buildPluginAgentSystemPrompt(agentID string, def pluginAgentDefinition, unavailable []string) string {
	var b strings.Builder
	b.WriteString("## Active reviewed plugin specialist: ")
	b.WriteString(agentID)
	b.WriteString("\nYou are running in a separate inspectable child chat. Complete only the delegated task and return a concise evidence-based result to the parent agent. Do not attempt to spawn another plugin agent.\n\n")
	b.WriteString(def.Prompt)
	if len(def.Tools) > 0 {
		b.WriteString("\n\n## Tool restriction\nUse only the tools offered by this child session. The plugin requested: ")
		b.WriteString(strings.Join(def.Tools, ", "))
		b.WriteString(".")
	}
	if len(unavailable) > 0 {
		b.WriteString("\nUnsupported or unavailable tool names were not exposed: ")
		b.WriteString(strings.Join(unavailable, ", "))
		b.WriteString(".")
	}
	if def.Model != "" && !strings.EqualFold(def.Model, "inherit") {
		b.WriteString("\nThe plugin's model hint is ignored; this child uses the project's selected GLM or Kimi model.")
	}
	if def.PermissionMode != "" {
		b.WriteString("\nThe plugin's permission-mode hint cannot weaken the project's user-selected approval policy.")
	}
	return b.String()
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	if values == nil {
		return nil
	}
	out := make(map[string]bool, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

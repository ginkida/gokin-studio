package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/google/uuid"
)

const (
	mcpAppExtensionID          = "io.modelcontextprotocol/ui"
	mcpAppMIMEType             = "text/html;profile=mcp-app"
	mcpAppProtocolVersion      = "2026-01-26"
	maxMCPAppResourceBytes     = 2 << 20
	maxMCPAppToolPayloadBytes  = 1 << 20
	maxMCPAppResourceURIBytes  = 2048
	maxMCPAppCSPDomainsPerKind = 32
	maxMCPAppCSPDomainBytes    = 512
	maxMCPAppCallArgsBytes     = 256 << 10
	maxMCPAppInstances         = 256
	maxMCPAppCallsPerMinute    = 30
)

const mcpAppInstanceLifetime = 2 * time.Hour

var mcpAppWildcardOriginRE = regexp.MustCompile(`^(?:https|wss)://\*\.[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]{1,5})?$`)

// MCPAppCSP is a server-declared, validated allowlist used to construct the
// iframe CSP in the frontend. It never contains raw directives.
type MCPAppCSP struct {
	ConnectDomains  []string `json:"connectDomains,omitempty"`
	ResourceDomains []string `json:"resourceDomains,omitempty"`
	FrameDomains    []string `json:"frameDomains,omitempty"`
	BaseURIDomains  []string `json:"baseUriDomains,omitempty"`
}

// MCPAppPayload is UI-only data attached to a tool-result event. It is never
// added to GLM/Kimi model history: the regular textual MCP content remains the
// model fallback while structuredContent/_meta drive the sandboxed view.
type MCPAppPayload struct {
	InstanceID    string         `json:"instanceID,omitempty"`
	ResourceURI   string         `json:"resourceURI"`
	HTML          string         `json:"html"`
	CSP           MCPAppCSP      `json:"csp"`
	PrefersBorder bool           `json:"prefersBorder"`
	ToolName      string         `json:"toolName"`
	ToolArgs      map[string]any `json:"toolArgs"`
	ToolResult    map[string]any `json:"toolResult"`
}

type mcpAppInstance struct {
	id          string
	projectID   string
	sessionID   string
	resourceURI string
	client      *mcpClient
	expiresAt   time.Time
	windowStart time.Time
	windowCalls int
	inFlight    bool
	cancel      context.CancelFunc
}

type mcpAppRegistry struct {
	mu        sync.Mutex
	instances map[string]*mcpAppInstance
}

func newMCPAppRegistry() *mcpAppRegistry {
	return &mcpAppRegistry{instances: make(map[string]*mcpAppInstance)}
}

func (s *Studio) ensureMCPAppRegistry() *mcpAppRegistry {
	s.mcpAppsOnce.Do(func() {
		if s.mcpApps == nil {
			s.mcpApps = newMCPAppRegistry()
		}
	})
	return s.mcpApps
}

func (r *mcpAppRegistry) pruneLocked(now time.Time) {
	for id, instance := range r.instances {
		if !instance.expiresAt.After(now) {
			if instance.cancel != nil {
				instance.cancel()
			}
			delete(r.instances, id)
		}
	}
	for len(r.instances) >= maxMCPAppInstances {
		var oldestID string
		var oldest time.Time
		for id, instance := range r.instances {
			if oldestID == "" || instance.expiresAt.Before(oldest) {
				oldestID, oldest = id, instance.expiresAt
			}
		}
		if oldestID == "" {
			break
		}
		if instance := r.instances[oldestID]; instance != nil && instance.cancel != nil {
			instance.cancel()
		}
		delete(r.instances, oldestID)
	}
}

func (r *mcpAppRegistry) register(projectID, sessionID, resourceURI string, client *mcpClient) string {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	id := uuid.NewString()
	r.instances[id] = &mcpAppInstance{
		id: id, projectID: projectID, sessionID: sessionID,
		resourceURI: resourceURI, client: client,
		expiresAt: now.Add(mcpAppInstanceLifetime),
	}
	return id
}

func (r *mcpAppRegistry) beginCall(id string) (mcpAppInstance, error) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	instance := r.instances[id]
	if instance == nil || !instance.expiresAt.After(now) {
		delete(r.instances, id)
		return mcpAppInstance{}, fmt.Errorf("MCP App session expired; run the parent tool again")
	}
	if instance.inFlight {
		return mcpAppInstance{}, fmt.Errorf("this MCP App already has an action waiting or running")
	}
	if instance.windowStart.IsZero() || now.Sub(instance.windowStart) >= time.Minute {
		instance.windowStart = now
		instance.windowCalls = 0
	}
	if instance.windowCalls >= maxMCPAppCallsPerMinute {
		return mcpAppInstance{}, fmt.Errorf("MCP App action rate limit exceeded")
	}
	instance.windowCalls++
	instance.inFlight = true
	instance.expiresAt = now.Add(mcpAppInstanceLifetime)
	return *instance, nil
}

func (r *mcpAppRegistry) finishCall(id string) {
	r.mu.Lock()
	if instance := r.instances[id]; instance != nil {
		instance.inFlight = false
		instance.cancel = nil
	}
	r.mu.Unlock()
}

func (r *mcpAppRegistry) attachCallCancel(id string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	instance := r.instances[id]
	if instance == nil || !instance.inFlight {
		cancel()
		return false
	}
	instance.cancel = cancel
	return true
}

func (r *mcpAppRegistry) unregisterClient(client *mcpClient) {
	r.mu.Lock()
	for id, instance := range r.instances {
		if instance.client == client {
			if instance.cancel != nil {
				instance.cancel()
			}
			delete(r.instances, id)
		}
	}
	r.mu.Unlock()
}

type mcpAppResourceMeta struct {
	UI struct {
		CSP struct {
			ConnectDomains  []string `json:"connectDomains"`
			ResourceDomains []string `json:"resourceDomains"`
			FrameDomains    []string `json:"frameDomains"`
			BaseURIDomains  []string `json:"baseUriDomains"`
		} `json:"csp"`
		PrefersBorder bool `json:"prefersBorder"`
	} `json:"ui"`
}

func mcpAppCapabilities() map[string]any {
	return map[string]any{
		"extensions": map[string]any{
			mcpAppExtensionID: map[string]any{
				"mimeTypes": []string{mcpAppMIMEType},
			},
		},
	}
}

func mcpAppResourceURI(remote mcpRemoteTool) (string, error) {
	if remote.Meta == nil {
		return "", nil
	}
	if _, _, err := mcpToolVisibility(remote); err != nil {
		return "", err
	}
	var resourceURI string
	if ui, ok := remote.Meta["ui"].(map[string]any); ok {
		resourceURI, _ = ui["resourceUri"].(string)
	}
	if resourceURI == "" {
		resourceURI, _ = remote.Meta["ui/resourceUri"].(string)
	}
	resourceURI = strings.TrimSpace(resourceURI)
	if resourceURI == "" {
		return "", nil
	}
	if err := validateMCPAppResourceURI(resourceURI); err != nil {
		return "", fmt.Errorf("tool %s: %w", remote.Name, err)
	}
	return resourceURI, nil
}

func mcpToolVisibility(remote mcpRemoteTool) (model, app bool, err error) {
	model, app = true, true
	if remote.Meta == nil {
		return model, app, nil
	}
	ui, ok := remote.Meta["ui"].(map[string]any)
	if !ok {
		return model, app, nil
	}
	raw, exists := ui["visibility"]
	if !exists {
		return model, app, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return false, false, fmt.Errorf("tool %s has invalid MCP App visibility", remote.Name)
	}
	model, app = false, false
	for _, item := range items {
		scope, ok := item.(string)
		if !ok || (scope != "model" && scope != "app") {
			return false, false, fmt.Errorf("tool %s has invalid MCP App visibility", remote.Name)
		}
		if scope == "model" {
			model = true
		} else {
			app = true
		}
	}
	return model, app, nil
}

func mcpToolVisibleToModel(remote mcpRemoteTool) (bool, error) {
	model, _, err := mcpToolVisibility(remote)
	return model, err
}

func validateMCPAppResourceURI(resourceURI string) error {
	if !utf8.ValidString(resourceURI) || strings.ContainsRune(resourceURI, 0) ||
		len(resourceURI) > maxMCPAppResourceURIBytes {
		return fmt.Errorf("invalid MCP App resource URI")
	}
	parsed, err := url.Parse(resourceURI)
	if err != nil || parsed.Scheme != "ui" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("MCP App resource URI must use ui:// with no credentials or fragment")
	}
	return nil
}

func (c *mcpClient) readMCPApp(ctx context.Context, resourceURI, toolName string, args map[string]any, result mcpToolCallResult) (*MCPAppPayload, error) {
	if err := validateMCPAppResourceURI(resourceURI); err != nil {
		return nil, err
	}
	var response struct {
		Contents []struct {
			URI      string             `json:"uri"`
			MIMEType string             `json:"mimeType"`
			Text     string             `json:"text"`
			Blob     string             `json:"blob"`
			Meta     mcpAppResourceMeta `json:"_meta"`
		} `json:"contents"`
	}
	if err := c.request(ctx, "resources/read", map[string]any{"uri": resourceURI}, &response); err != nil {
		return nil, fmt.Errorf("read MCP App resource: %w", err)
	}
	if len(response.Contents) != 1 {
		return nil, fmt.Errorf("MCP App resources/read must return exactly one content item")
	}
	content := response.Contents[0]
	if content.URI != resourceURI || strings.ToLower(strings.TrimSpace(content.MIMEType)) != mcpAppMIMEType {
		return nil, fmt.Errorf("MCP App resource URI or MIME type does not match its declaration")
	}
	html := content.Text
	if content.Text != "" && content.Blob != "" {
		return nil, fmt.Errorf("MCP App resource must provide text or blob, not both")
	}
	if html == "" && content.Blob != "" {
		decoded, err := base64.StdEncoding.DecodeString(content.Blob)
		if err != nil {
			return nil, fmt.Errorf("decode MCP App HTML: %w", err)
		}
		html = string(decoded)
	}
	if html == "" || !utf8.ValidString(html) || strings.ContainsRune(html, 0) ||
		len(html) > maxMCPAppResourceBytes {
		return nil, fmt.Errorf("MCP App HTML must be valid UTF-8 and at most %d MiB", maxMCPAppResourceBytes>>20)
	}
	csp := MCPAppCSP{}
	var err error
	if csp.ConnectDomains, err = validateMCPAppDomains(content.Meta.UI.CSP.ConnectDomains, true); err != nil {
		return nil, fmt.Errorf("MCP App connectDomains: %w", err)
	}
	if csp.ResourceDomains, err = validateMCPAppDomains(content.Meta.UI.CSP.ResourceDomains, false); err != nil {
		return nil, fmt.Errorf("MCP App resourceDomains: %w", err)
	}
	if csp.FrameDomains, err = validateMCPAppDomains(content.Meta.UI.CSP.FrameDomains, false); err != nil {
		return nil, fmt.Errorf("MCP App frameDomains: %w", err)
	}
	if csp.BaseURIDomains, err = validateMCPAppDomains(content.Meta.UI.CSP.BaseURIDomains, false); err != nil {
		return nil, fmt.Errorf("MCP App baseUriDomains: %w", err)
	}

	resultMap, err := mcpAppResultMap(result)
	if err != nil {
		return nil, err
	}
	payload := &MCPAppPayload{
		ResourceURI: resourceURI, HTML: html, CSP: csp,
		PrefersBorder: content.Meta.UI.PrefersBorder,
		ToolName:      toolName, ToolArgs: args, ToolResult: resultMap,
	}
	if encoded, err := json.Marshal(payload); err != nil {
		return nil, fmt.Errorf("encode MCP App payload: %w", err)
	} else if len(encoded)-len(html) > maxMCPAppToolPayloadBytes {
		return nil, fmt.Errorf("MCP App tool data exceeds the %d MiB UI limit", maxMCPAppToolPayloadBytes>>20)
	}
	return payload, nil
}

func mcpAppResultMap(result mcpToolCallResult) (map[string]any, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode MCP App tool result: %w", err)
	}
	if len(raw) > maxMCPAppToolPayloadBytes {
		return nil, fmt.Errorf("MCP App tool result exceeds the %d MiB UI limit", maxMCPAppToolPayloadBytes>>20)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateMCPAppDomains(domains []string, allowWebSocket bool) ([]string, error) {
	if len(domains) > maxMCPAppCSPDomainsPerKind {
		return nil, fmt.Errorf("may contain at most %d origins", maxMCPAppCSPDomainsPerKind)
	}
	seen := make(map[string]bool, len(domains))
	out := make([]string, 0, len(domains))
	for _, raw := range domains {
		origin := strings.TrimSpace(raw)
		if origin == "" || len(origin) > maxMCPAppCSPDomainBytes || strings.ContainsAny(origin, " \t\r\n;,'\"") {
			return nil, fmt.Errorf("invalid origin %q", raw)
		}
		if mcpAppWildcardOriginRE.MatchString(origin) {
			if strings.HasPrefix(origin, "wss://") && !allowWebSocket {
				return nil, fmt.Errorf("origin must use HTTPS")
			}
			if !seen[origin] {
				seen[origin] = true
				out = append(out, origin)
			}
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid origin %q", raw)
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "https" && (!allowWebSocket || scheme != "wss") {
			return nil, fmt.Errorf("origin must use HTTPS%s", map[bool]string{true: " or WSS"}[allowWebSocket])
		}
		origin = strings.TrimSuffix(origin, "/")
		if !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out, nil
}

func mcpAppJSONTypeMatches(value any, typeName string) bool {
	switch typeName {
	case "", "any":
		return true
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch number := value.(type) {
		case float64:
			return !math.IsNaN(number) && !math.IsInf(number, 0)
		case float32:
			return !math.IsNaN(float64(number)) && !math.IsInf(float64(number), 0)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		}
	case "integer":
		switch number := value.(type) {
		case float64:
			return !math.IsNaN(number) && !math.IsInf(number, 0) && math.Trunc(number) == number
		case float32:
			return !math.IsNaN(float64(number)) && !math.IsInf(float64(number), 0) &&
				math.Trunc(float64(number)) == float64(number)
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		}
	}
	return false
}

func mcpAppSchemaTypes(schema map[string]any) []string {
	if typeName, ok := schema["type"].(string); ok {
		return []string{typeName}
	}
	if raw, ok := schema["type"].([]any); ok {
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if typeName, ok := item.(string); ok {
				out = append(out, typeName)
			}
		}
		return out
	}
	return nil
}

func validateMCPAppSchemaValue(value any, schema map[string]any, path string, depth int, nodes *int) error {
	if depth > maxMCPSchemaDepth {
		return fmt.Errorf("%s exceeds the schema nesting limit", path)
	}
	*nodes = *nodes + 1
	if *nodes > maxMCPSchemaNodes {
		return fmt.Errorf("arguments exceed the schema node limit")
	}
	if types := mcpAppSchemaTypes(schema); len(types) > 0 {
		matches := false
		for _, typeName := range types {
			if mcpAppJSONTypeMatches(value, typeName) {
				matches = true
				break
			}
		}
		if !matches {
			return fmt.Errorf("%s has the wrong type", path)
		}
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		matches := false
		for _, candidate := range enum {
			if reflect.DeepEqual(value, candidate) {
				matches = true
				break
			}
		}
		if !matches {
			return fmt.Errorf("%s is not one of the allowed values", path)
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		required := map[string]bool{}
		switch raw := schema["required"].(type) {
		case []any:
			for _, item := range raw {
				if name, ok := item.(string); ok {
					required[name] = true
				}
			}
		case []string:
			for _, name := range raw {
				required[name] = true
			}
		}
		for name := range required {
			if _, ok := typed[name]; !ok {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		additionalAllowed := true
		if allowed, ok := schema["additionalProperties"].(bool); ok {
			additionalAllowed = allowed
		}
		for name, childValue := range typed {
			childRaw, declared := properties[name]
			if !declared {
				if !additionalAllowed {
					return fmt.Errorf("%s.%s is not an allowed field", path, name)
				}
				continue
			}
			childSchema, ok := childRaw.(map[string]any)
			if ok {
				if err := validateMCPAppSchemaValue(childValue, childSchema, path+"."+name, depth+1, nodes); err != nil {
					return err
				}
			}
		}
	case []any:
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range typed {
				if err := validateMCPAppSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, index), depth+1, nodes); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateMCPAppToolArguments(schema map[string]any, args map[string]any) error {
	nodes := 0
	if err := validateMCPAppSchemaValue(args, schema, "arguments", 0, &nodes); err != nil {
		return fmt.Errorf("MCP App tool arguments: %w", err)
	}
	return nil
}

func mcpAppSensitiveArgumentKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, marker := range []string{
		"password", "passwd", "secret", "token", "authorization",
		"cookie", "credential", "api_key", "private_key",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactMCPAppApprovalValue(value any, key string, depth int) any {
	if mcpAppSensitiveArgumentKey(key) {
		return "<redacted>"
	}
	if depth > maxMCPSchemaDepth {
		return "<nested value omitted>"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, child := range typed {
			out[childKey] = redactMCPAppApprovalValue(child, childKey, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = redactMCPAppApprovalValue(child, key, depth+1)
		}
		return out
	default:
		return value
	}
}

func mcpAppApprovalDetails(instance mcpAppInstance, toolName string, args map[string]any) []ToolApprovalDetail {
	redacted := redactMCPAppApprovalValue(args, "", 0)
	redacted = security.NewSecretRedactor().RedactAny(redacted)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(redacted)
	arguments := previewApprovalText(strings.TrimSpace(encoded.String()), 2000)
	if arguments == "" {
		arguments = "{}"
	}
	return []ToolApprovalDetail{
		{Label: "Connector", Value: previewApprovalText(instance.client.cfg.Name, 160)},
		{Label: "Tool", Value: previewApprovalText(toolName, 160)},
		{Label: "App resource", Value: previewApprovalText(instance.resourceURI, 512)},
		{Label: "Arguments", Value: arguments},
	}
}

// CallMCPAppTool proxies one app-originated tools/call request to the exact MCP
// server connection that produced the iframe. App-only tools never enter the
// GLM/Kimi declaration list, and every call receives a fresh user approval.
func (s *Studio) CallMCPAppTool(instanceID, toolName string, args map[string]any) (map[string]any, error) {
	if _, err := uuid.Parse(instanceID); err != nil {
		return nil, fmt.Errorf("invalid MCP App session")
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || len([]rune(toolName)) > maxMCPRemoteToolNameRunes || strings.ContainsRune(toolName, 0) {
		return nil, fmt.Errorf("invalid MCP App tool name")
	}
	if args == nil {
		args = map[string]any{}
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode MCP App tool arguments: %w", err)
	}
	if len(rawArgs) > maxMCPAppCallArgsBytes {
		return nil, fmt.Errorf("MCP App tool arguments exceed the %d KiB limit", maxMCPAppCallArgsBytes>>10)
	}

	registry := s.ensureMCPAppRegistry()
	instance, err := registry.beginCall(instanceID)
	if err != nil {
		return nil, err
	}
	defer registry.finishCall(instanceID)
	remote, ok := instance.client.appTools[toolName]
	if !ok {
		return nil, fmt.Errorf("MCP App tool is not visible to apps on this server")
	}
	if err := validateMCPAppToolArguments(remote.InputSchema, args); err != nil {
		return nil, err
	}

	s.mu.RLock()
	project := s.projects[instance.projectID]
	s.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("MCP App project no longer exists")
	}
	project.mu.RLock()
	_, sessionExists := project.sessions[instance.sessionID]
	project.mu.RUnlock()
	if !sessionExists {
		return nil, fmt.Errorf("MCP App chat no longer exists")
	}

	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	lifecycleCtx, cancelLifecycle := context.WithCancel(baseCtx)
	if !registry.attachCallCancel(instanceID, cancelLifecycle) {
		return nil, fmt.Errorf("MCP App session closed")
	}
	defer cancelLifecycle()
	approvalCtx, cancelApproval := context.WithTimeout(lifecycleCtx, 2*time.Minute)
	defer cancelApproval()
	approved := false
	if s.testMCPAppApproval != nil {
		approved, err = s.testMCPAppApproval(
			approvalCtx, instance.projectID, instance.sessionID,
			instance.client.cfg.Name, toolName, args,
		)
	} else {
		if s.askUsers == nil {
			return nil, fmt.Errorf("MCP App approval is unavailable")
		}
		routedCtx := withAskUserRouting(approvalCtx, instance.projectID, instance.sessionID)
		answer, askErr := s.waitForUserAnswer(baseCtx, routedCtx, AskUserEvent{
			Kind:     "tool_approval",
			Tool:     "mcp_app:" + instance.client.cfg.Name + "/" + toolName,
			Scope:    "single_action",
			Question: fmt.Sprintf("MCP App requests a tool action through %s.", instance.client.cfg.Name),
			Options:  []string{"Run this app action", "Deny"},
			Default:  "Deny",
			Details:  mcpAppApprovalDetails(instance, toolName, args),
		})
		err = askErr
		approved = strings.EqualFold(strings.TrimSpace(answer), "Run this app action")
	}
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, fmt.Errorf("MCP App action denied by the user")
	}

	callCtx, cancelCall := context.WithTimeout(lifecycleCtx, mcpToolCallTimeout)
	defer cancelCall()
	var result mcpToolCallResult
	if err := instance.client.request(callCtx, "tools/call", map[string]any{
		"name": toolName, "arguments": args,
	}, &result); err != nil {
		var remoteErr *mcpRemoteCallError
		if !errors.As(err, &remoteErr) {
			registry.unregisterClient(instance.client)
		}
		return nil, fmt.Errorf("MCP App tool call failed: %s", previewApprovalText(err.Error(), 1000))
	}
	return mcpAppResultMap(result)
}

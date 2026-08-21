package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

const previewBrowserPayloadMaxBytes = 6 << 20

type previewBrowserRegistry struct {
	mu      sync.Mutex
	pending map[string]*previewBrowserPending
}

type previewBrowserPending struct {
	projectID     string
	sessionID     string
	configuration string
	bridgeToken   string
	ch            chan string
}

func newPreviewBrowserRegistry() *previewBrowserRegistry {
	return &previewBrowserRegistry{pending: make(map[string]*previewBrowserPending)}
}
func (r *previewBrowserRegistry) register(id string, pending *previewBrowserPending) (chan string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pending[id]; exists {
		return nil, fmt.Errorf("preview browser request identity already exists")
	}
	for _, existing := range r.pending {
		if existing.projectID == pending.projectID &&
			existing.sessionID == pending.sessionID &&
			existing.configuration == pending.configuration &&
			existing.bridgeToken == pending.bridgeToken {
			return nil, fmt.Errorf("another Preview browser action is already in progress for this frame")
		}
	}
	pending.ch = make(chan string, 1)
	r.pending[id] = pending
	return pending.ch, nil
}
func (r *previewBrowserRegistry) cleanup(id string, owner *previewBrowserPending) {
	r.mu.Lock()
	if r.pending[id] == owner {
		delete(r.pending, id)
	}
	r.mu.Unlock()
}
func (r *previewBrowserRegistry) resolve(id, payload string) bool {
	r.mu.Lock()
	pending, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	pending.ch <- payload
	close(pending.ch)
	return true
}

type previewBrowserTool struct {
	studio       *Studio
	attachVision bool
}

func (t *previewBrowserTool) Name() string { return "preview_browser" }
func (t *previewBrowserTool) Description() string {
	return "Inspect and interact with the active localhost app preview using live DOM, runtime errors, and a viewport screenshot."
}
func (t *previewBrowserTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: "Use after editing a running web app. Inspect before acting and inspect again before finishing.", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
		"action": {Type: genai.TypeString, Enum: []string{"inspect", "click", "fill", "scroll", "key"}},
		"x":      {Type: genai.TypeInteger, Description: "Viewport X coordinate from the latest inspection."}, "y": {Type: genai.TypeInteger, Description: "Viewport Y coordinate from the latest inspection."},
		"text": {Type: genai.TypeString, Description: "Text for fill."}, "key": {Type: genai.TypeString, Description: "ENTER, TAB, ESCAPE, SPACE, ARROWDOWN, or ARROWUP."},
		"deltaY": {Type: genai.TypeInteger, Description: "Scroll amount between -2000 and 2000."}, "screenshot": {Type: genai.TypeBoolean, Description: "Include a viewport screenshot when inspecting; defaults true."},
	}, Required: []string{"action"}}}
}
func (t *previewBrowserTool) Validate(args map[string]any) error {
	action := strings.ToLower(strings.TrimSpace(tools.GetStringDefault(args, "action", "")))
	switch action {
	case "inspect":
		return nil
	case "click":
		x, xOK := tools.GetInt(args, "x")
		y, yOK := tools.GetInt(args, "y")
		if !xOK || !yOK || x < 0 || y < 0 || x > 10000 || y > 10000 {
			return tools.NewValidationError("x/y", "must be viewport coordinates between 0 and 10000")
		}
	case "fill":
		text, ok := tools.GetString(args, "text")
		if !ok || text == "" || len(text) > 4000 || !utf8.ValidString(text) {
			return tools.NewValidationError("text", "must be non-empty valid UTF-8 up to 4000 bytes")
		}
		x, xOK := tools.GetInt(args, "x")
		y, yOK := tools.GetInt(args, "y")
		if !xOK || !yOK || x < 0 || y < 0 || x > 10000 || y > 10000 {
			return tools.NewValidationError("x/y", "must be viewport coordinates between 0 and 10000")
		}
	case "scroll":
		value, ok := tools.GetInt(args, "deltaY")
		if !ok || value < -2000 || value > 2000 {
			return tools.NewValidationError("deltaY", "must be between -2000 and 2000")
		}
	case "key":
		key := strings.ToUpper(strings.TrimSpace(tools.GetStringDefault(args, "key", "")))
		if !map[string]bool{"ENTER": true, "TAB": true, "ESCAPE": true, "SPACE": true, "ARROWDOWN": true, "ARROWUP": true}[key] {
			return tools.NewValidationError("key", "is not supported")
		}
	default:
		return tools.NewValidationError("action", "must be inspect, click, fill, scroll, or key")
	}
	return nil
}
func (t *previewBrowserTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return tools.NewErrorResult(err.Error()), nil
	}
	projectID, sessionID := askUserRouting(ctx)
	if sessionID == "" {
		sessionID = "default"
	}
	payload, err := t.studio.requestPreviewBrowser(ctx, projectID, sessionID, args)
	if err != nil {
		return tools.NewErrorResult(err.Error()), nil
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return tools.NewErrorResult("invalid preview response"), nil
	}
	if message, ok := data["error"].(string); ok && strings.TrimSpace(message) != "" {
		return tools.NewErrorResult(message), nil
	}
	var media []*tools.MultimodalPart
	if raw, ok := data["screenshotDataURL"].(string); ok {
		delete(data, "screenshotDataURL")
		const prefix = "data:image/png;base64,"
		if t.attachVision && strings.HasPrefix(raw, prefix) {
			if decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, prefix)); err == nil && len(decoded) <= 4<<20 {
				media = append(media, &tools.MultimodalPart{MimeType: "image/png", Data: decoded})
			}
		}
	}
	encoded, _ := json.MarshalIndent(data, "", "  ")
	return tools.ToolResult{Success: true, Content: string(encoded), Data: data, MultimodalParts: media}, nil
}

func (s *Studio) requestPreviewBrowser(ctx context.Context, projectID, sessionID string, args map[string]any) (string, error) {
	if s.previewBrowser == nil {
		return "", fmt.Errorf("preview browser registry is unavailable")
	}
	s.previewMu.Lock()
	var run *previewServerRun
	for _, candidate := range s.previewServers {
		candidate.mu.RLock()
		match := candidate.projectID == projectID && candidate.sessionID == sessionID && candidate.state == "running"
		static := candidate.staticPath != ""
		candidate.mu.RUnlock()
		if match {
			// A visible static-file preview supersedes a still-running dev server.
			// Closing the file removes this run and restores dev-server routing.
			if static {
				run = candidate
				break
			}
			if run == nil {
				run = candidate
			}
		}
	}
	s.previewMu.Unlock()
	if run == nil {
		return "", fmt.Errorf("no running app preview for this session; open Preview and start a server")
	}
	run.mu.RLock()
	configuration, token := run.config.Name, run.bridgeToken
	run.mu.RUnlock()
	id := uuid.NewString()[:12]
	pending := &previewBrowserPending{projectID: projectID, sessionID: sessionID, configuration: configuration, bridgeToken: token}
	ch, err := s.previewBrowser.register(id, pending)
	if err != nil {
		return "", err
	}
	defer s.previewBrowser.cleanup(id, pending)
	event := map[string]any{"requestID": id, "projectID": projectID, "sessionID": sessionID, "configuration": configuration, "bridgeToken": token, "args": args}
	if s.testPreviewBrowserEmitter != nil {
		s.testPreviewBrowserEmitter(event)
	} else {
		wailsRuntime.EventsEmit(s.ctx, EventPreviewBrowser, event)
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case payload := <-ch:
		return payload, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("preview browser request timed out; keep the Preview pane open")
	}
}

func (s *Studio) ResolvePreviewBrowserRequest(requestID, payload string) error {
	if len(requestID) < 1 || len(requestID) > 64 {
		return fmt.Errorf("invalid preview request ID")
	}
	if len(payload) > previewBrowserPayloadMaxBytes || !utf8.ValidString(payload) {
		return fmt.Errorf("preview response exceeds the allowed size")
	}
	var value map[string]any
	if json.Unmarshal([]byte(payload), &value) != nil || value == nil {
		return fmt.Errorf("preview response is invalid JSON")
	}
	if s.previewBrowser == nil || !s.previewBrowser.resolve(requestID, payload) {
		return fmt.Errorf("preview request not found")
	}
	return nil
}

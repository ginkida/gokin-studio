package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	SessionAgentIDMaxBytes      = 128
	SessionAgentMessageMaxBytes = 32 << 10
	SessionAgentNameMaxBytes    = 240
)

// SessionAgentHandler is supplied by the desktop runtime, which owns the
// connected-project/session catalog and the per-session message queues.
type SessionAgentHandler func(ctx context.Context, action string, args map[string]any) (ToolResult, error)

// SessionAgentTool lets one watched Studio chat coordinate with other local
// Studio chats. It deliberately has no generic filesystem or process access;
// every target is resolved again by the desktop runtime from opaque IDs.
type SessionAgentTool struct {
	handler SessionAgentHandler
}

func NewSessionAgentTool() *SessionAgentTool { return &SessionAgentTool{} }

func (t *SessionAgentTool) SetHandler(handler SessionAgentHandler) { t.handler = handler }

func (t *SessionAgentTool) Name() string { return "session_agent" }

func (t *SessionAgentTool) Description() string {
	return "List other local Gokin Studio sessions, optionally including archived chats; read their bounded recent transcript; send an attributed message; rename or archive one; or suggest an out-of-scope task as a user-clickable new-session chip. The current session is never a target. Archiving is reversible and always requires explicit user approval."
}

func (t *SessionAgentTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name: t.Name(), Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type: genai.TypeString, Enum: []string{"list", "read", "send", "rename", "archive", "suggest"},
					Description: "Operation to perform",
				},
				"include_archived": {
					Type:        genai.TypeBoolean,
					Description: "Include archived chats in list results (list only; default false)",
				},
				"project_id": {
					Type:        genai.TypeString,
					Description: "Exact target project ID returned by list",
				},
				"session_id": {
					Type:        genai.TypeString,
					Description: "Exact target session ID returned by list",
				},
				"message": {
					Type:        genai.TypeString,
					Description: "Message to deliver (send) or complete task prompt for the proposed new session (suggest)",
				},
				"name": {
					Type:        genai.TypeString,
					Description: "New visible session name (rename) or concise task-chip title (suggest)",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (t *SessionAgentTool) Validate(args map[string]any) error {
	action, ok := GetString(args, "action")
	action = strings.ToLower(strings.TrimSpace(action))
	if !ok || action == "" {
		return NewValidationError("action", "is required")
	}
	switch action {
	case "list":
		return nil
	case "suggest":
		if err := sessionAgentRequiredText(args, "name", SessionAgentNameMaxBytes); err != nil {
			return err
		}
		return sessionAgentRequiredText(args, "message", SessionAgentMessageMaxBytes)
	case "read", "send", "rename", "archive":
		if err := sessionAgentRequiredText(args, "project_id", SessionAgentIDMaxBytes); err != nil {
			return err
		}
		if err := sessionAgentRequiredText(args, "session_id", SessionAgentIDMaxBytes); err != nil {
			return err
		}
	default:
		return NewValidationError("action", "must be list, read, send, rename, archive, or suggest")
	}
	if action == "send" {
		return sessionAgentRequiredText(args, "message", SessionAgentMessageMaxBytes)
	}
	if action == "rename" {
		return sessionAgentRequiredText(args, "name", SessionAgentNameMaxBytes)
	}
	return nil
}

func (t *SessionAgentTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if t.handler == nil {
		return NewErrorResult("session coordination is unavailable"), nil
	}
	action, _ := GetString(args, "action")
	return t.handler(ctx, strings.ToLower(strings.TrimSpace(action)), args)
}

func sessionAgentRequiredText(args map[string]any, key string, maxBytes int) error {
	value, ok := GetString(args, key)
	if !ok || strings.TrimSpace(value) == "" {
		return NewValidationError(key, "is required")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxBytes {
		return NewValidationError(key, fmt.Sprintf("must be UTF-8 text up to %d bytes", maxBytes))
	}
	return nil
}

package tools

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/genai"
)

const ComputerTypeMaxBytes = 4000

type ComputerActionTool struct {
	click func(context.Context, int, int, string) error
	typ   func(context.Context, string) error
	key   func(context.Context, string) error
}

func NewComputerActionTool() *ComputerActionTool {
	return &ComputerActionTool{
		click: performComputerClick,
		typ:   performComputerType,
		key:   performComputerKey,
	}
}

func (*ComputerActionTool) Name() string { return "computer_action" }
func (*ComputerActionTool) Description() string {
	return "Interact with the currently approved foreground application by clicking coordinates, typing text, or pressing a key chord. Every action is shown to the user for explicit review."
}
func (*ComputerActionTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "computer_action",
		Description: "Perform one reviewed UI action after computer_screenshot. Never guess coordinates; capture the screen first.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Enum:        []string{"click", "type", "key"},
					Description: "Single action to perform.",
				},
				"x": {
					Type:        genai.TypeInteger,
					Description: "Absolute screen X coordinate for click; multi-monitor coordinates may be negative.",
				},
				"y": {
					Type:        genai.TypeInteger,
					Description: "Absolute screen Y coordinate for click; multi-monitor coordinates may be negative.",
				},
				"button": {
					Type:        genai.TypeString,
					Enum:        []string{"left", "right", "double"},
					Description: "Click kind; defaults to left.",
				},
				"text": {
					Type:        genai.TypeString,
					Description: "Literal text for the type action. It is displayed in the approval card.",
				},
				"keys": {
					Type:        genai.TypeString,
					Description: "Key chord such as CTRL+C, CMD+S, ENTER, or SHIFT+TAB.",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (*ComputerActionTool) Validate(args map[string]any) error {
	action, ok := GetString(args, "action")
	if !ok {
		return NewValidationError("action", "is required")
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "click":
		x, xOK := GetInt(args, "x")
		y, yOK := GetInt(args, "y")
		if !xOK || !yOK {
			return NewValidationError("coordinates", "x and y integers are required for click")
		}
		if x < -100000 || x > 100000 || y < -100000 || y > 100000 {
			return NewValidationError("coordinates", "x and y must be between -100000 and 100000")
		}
		button := strings.ToLower(GetStringDefault(args, "button", "left"))
		if button != "left" && button != "right" && button != "double" {
			return NewValidationError("button", "must be left, right, or double")
		}
	case "type":
		text, ok := GetString(args, "text")
		if !ok || text == "" {
			return NewValidationError("text", "non-empty text is required for type")
		}
		if len(text) > ComputerTypeMaxBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return NewValidationError("text", fmt.Sprintf("must be valid UTF-8 and at most %d bytes", ComputerTypeMaxBytes))
		}
		if strings.IndexFunc(text, func(r rune) bool {
			return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
		}) >= 0 {
			return NewValidationError("text", "contains unsupported control characters")
		}
	case "key":
		keys, ok := GetString(args, "keys")
		if !ok {
			return NewValidationError("keys", "is required for key")
		}
		if _, err := normalizeComputerKeyChord(keys); err != nil {
			return NewValidationError("keys", err.Error())
		}
	default:
		return NewValidationError("action", "must be click, type, or key")
	}
	return nil
}

func (t *ComputerActionTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult("invalid computer action: " + err.Error()), nil
	}
	action := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "action", "")))
	var err error
	var summary string
	switch action {
	case "click":
		x, _ := GetInt(args, "x")
		y, _ := GetInt(args, "y")
		button := strings.ToLower(GetStringDefault(args, "button", "left"))
		err = t.click(ctx, x, y, button)
		summary = fmt.Sprintf("%s click at (%d, %d)", button, x, y)
	case "type":
		text, _ := GetString(args, "text")
		err = t.typ(ctx, text)
		summary = fmt.Sprintf("typed %d characters", utf8.RuneCountInString(text))
	case "key":
		chord, _ := normalizeComputerKeyChord(GetStringDefault(args, "keys", ""))
		err = t.key(ctx, chord)
		summary = "pressed " + chord
	}
	if err != nil {
		return NewErrorResult("computer action failed: " + err.Error()), nil
	}
	return NewSuccessResult("Computer action completed: " + summary + "."), nil
}

func normalizeComputerKeyChord(value string) (string, error) {
	raw := strings.Split(strings.ToUpper(strings.TrimSpace(value)), "+")
	if len(raw) == 0 || len(raw) > 5 {
		return "", fmt.Errorf("key chord must contain one key and at most four modifiers")
	}
	modifiers := map[string]bool{}
	key := ""
	for _, token := range raw {
		token = strings.TrimSpace(token)
		switch token {
		case "CTRL", "CONTROL":
			modifiers["CTRL"] = true
		case "ALT", "OPTION":
			modifiers["ALT"] = true
		case "SHIFT":
			modifiers["SHIFT"] = true
		case "CMD", "COMMAND", "META", "WIN":
			modifiers["META"] = true
		default:
			if key != "" || !validComputerKey(token) {
				return "", fmt.Errorf("unsupported or ambiguous key chord")
			}
			key = token
		}
	}
	if key == "" {
		return "", fmt.Errorf("key chord requires a non-modifier key")
	}
	var out []string
	for _, modifier := range []string{"CTRL", "ALT", "SHIFT", "META"} {
		if modifiers[modifier] {
			out = append(out, modifier)
		}
	}
	return strings.Join(append(out, key), "+"), nil
}

func validComputerKey(key string) bool {
	if len(key) == 1 && ((key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9')) {
		return true
	}
	switch key {
	case "ENTER", "ESC", "ESCAPE", "TAB", "SPACE", "BACKSPACE", "DELETE",
		"UP", "DOWN", "LEFT", "RIGHT", "HOME", "END", "PAGEUP", "PAGEDOWN":
		return true
	}
	return false
}

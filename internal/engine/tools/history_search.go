package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/genai"
)

// HistoryGetterCtxKey is the context key used by project.go to supply a
// per-session history getter to the tool without storing it on the shared
// tool struct (which would race when two sessions of the same project run
// concurrently).
type HistoryGetterCtxKey struct{}

// HistorySearchTool searches through the agent's message history.
type HistorySearchTool struct {
	historyGetter func() []*genai.Content
}

// NewHistorySearchTool creates a new HistorySearchTool.
func NewHistorySearchTool(historyGetter func() []*genai.Content) *HistorySearchTool {
	return &HistorySearchTool{
		historyGetter: historyGetter,
	}
}

// SetHistoryGetter sets the function to retrieve message history.
func (t *HistorySearchTool) SetHistoryGetter(fn func() []*genai.Content) {
	t.historyGetter = fn
}

func (t *HistorySearchTool) Name() string {
	return "history_search"
}

func (t *HistorySearchTool) Description() string {
	return `Searches through the current session's message history using a regular expression.
Use this to recover specific details, file paths, or error messages from earlier in the conversation that might have been lost due to context truncation or summarization.

PARAMETERS:
- pattern (required): The regular expression pattern to search for in message history.

RETURNS:
- A list of matching message excerpts with their roles and order in history.`
}

func (t *HistorySearchTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"pattern": {
					Type:        genai.TypeString,
					Description: "Regex pattern to search for in history",
				},
			},
			Required: []string{"pattern"},
		},
	}
}

func (t *HistorySearchTool) Validate(args map[string]any) error {
	pattern, ok := GetString(args, "pattern")
	if !ok || pattern == "" {
		return NewValidationError("pattern", "is required")
	}
	_, err := regexp.Compile(pattern)
	if err != nil {
		return NewValidationError("pattern", fmt.Sprintf("invalid regex: %v", err))
	}
	return nil
}

func (t *HistorySearchTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	patternStr, _ := GetString(args, "pattern")
	// regexp.Compile, not MustCompile: the studio agent loop dispatches straight
	// to Execute without calling Validate, so an invalid pattern would panic the
	// whole turn (recovered, but surfaced as a confusing "Internal error").
	// Return a clean, actionable error instead.
	re, err := regexp.Compile("(?i)" + patternStr)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("invalid regex: %v", err)), nil
	}

	// Prefer a per-session getter injected via context (set by project.go's
	// SendMessage) over the stored field, which would be shared across sessions.
	getter := t.historyGetter
	if v, ok := ctx.Value(HistoryGetterCtxKey{}).(func() []*genai.Content); ok {
		getter = v
	}
	if getter == nil {
		return NewErrorResult("history search not supported by this agent"), nil
	}

	history := getter()
	var results []string

	for i, content := range history {
		for _, part := range content.Parts {
			var text string
			if part.Text != "" {
				text = part.Text
			} else if part.FunctionCall != nil {
				text = fmt.Sprintf("Tool Call: %s", part.FunctionCall.Name)
			} else if part.FunctionResponse != nil {
				text = fmt.Sprintf("Tool Response: %s", part.FunctionResponse.Name)
			}

			if text != "" && re.MatchString(text) {
				locs := re.FindAllStringIndex(text, -1)
				runes := []rune(text)
				for _, loc := range locs {
					// Convert byte offsets to rune offsets for safe slicing —
					// raw byte arithmetic on a UTF-8 string cuts multibyte
					// chars (CJK, emoji, Cyrillic) producing garbled excerpts.
					runeStart := len([]rune(text[:loc[0]]))
					runeEnd := len([]rune(text[:loc[1]]))
					start := runeStart - 50
					if start < 0 {
						start = 0
					}
					end := runeEnd + 50
					if end > len(runes) {
						end = len(runes)
					}
					excerpt := string(runes[start:end])
					results = append(results, fmt.Sprintf("[Message %d, Role: %s] ...%s...", i, content.Role, excerpt))
				}
			}
		}
	}

	if len(results) == 0 {
		return NewSuccessResult("No matches found in history."), nil
	}

	// Limit results
	if len(results) > 20 {
		results = append(results[:20], "... (truncated)")
	}

	return NewSuccessResult(strings.Join(results, "\n")), nil
}

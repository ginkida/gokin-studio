package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// User-defined chat snippets (iter 490+): /<name> shortcuts that EXPAND
// into the chat input rather than executing a built-in action. Different
// from system-prompt templates (one-time project config) — these are
// frequently-typed prompts that a user wants to alias.
//
// Storage: a single JSON file at `<configDir>/user_snippets.json` holding
// an array of records. Same flat-file pattern as drafts/pins/templates.
//
// The frontend slash autocomplete merges these with the built-in
// SLASH_COMMANDS array. Picking a snippet writes its body into the chat
// input — the user reviews and presses Enter to send. We deliberately
// don't auto-send so the user can still tweak the expansion before sending.

// UserSnippet is one /<name> → body alias. UpdatedAt is in unix millis.
type UserSnippet struct {
	ID        string `json:"id"`
	Name      string `json:"name"` // never starts with "/"; the frontend prepends it
	Body      string `json:"body"` // text inserted into chat input on selection
	UpdatedAt int64  `json:"updatedAt"`
}

const (
	UserSnippetNameMaxBytes  = 30
	UserSnippetBodyMaxBytes  = 10 * 1024 // 10 KB matches a generous prompt body
	UserSnippetsMaxCount     = 200
	UserSnippetsFileMaxBytes = 3 << 20
)

var userSnippetsMu sync.Mutex

func userSnippetsPath() string {
	return filepath.Join(configDir(), "user_snippets.json")
}

func loadUserSnippets() ([]UserSnippet, error) {
	data, err := readRegularFileLimited(userSnippetsPath(), UserSnippetsFileMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var snips []UserSnippet
	if err := json.Unmarshal(data, &snips); err != nil {
		return nil, fmt.Errorf("corrupt user_snippets.json: %w", err)
	}
	// Load is lenient on purpose. Save-time validation below stays strict, but
	// the set of reserved names grows between releases (this release added
	// /btw, /sessions and friends), so a library saved by an older build can
	// contain an entry that is only invalid now. Failing the whole file there
	// would silently wipe every snippet the user ever wrote; drop just the
	// entries that no longer qualify.
	return usableUserSnippets(snips), nil
}

// usableUserSnippets keeps the entries that still satisfy the current rules,
// preserving order and resolving ID/name collisions in favour of the first.
func usableUserSnippets(snips []UserSnippet) []UserSnippet {
	if len(snips) > UserSnippetsMaxCount {
		snips = snips[:UserSnippetsMaxCount]
	}
	ids := make(map[string]struct{}, len(snips))
	names := make(map[string]struct{}, len(snips))
	kept := make([]UserSnippet, 0, len(snips))
	for _, snippet := range snips {
		if !validUserSnippet(snippet) {
			continue
		}
		lname := strings.ToLower(snippet.Name)
		if _, duplicate := ids[snippet.ID]; duplicate {
			continue
		}
		if _, duplicate := names[lname]; duplicate {
			continue
		}
		ids[snippet.ID] = struct{}{}
		names[lname] = struct{}{}
		kept = append(kept, snippet)
	}
	return kept
}

func validUserSnippet(snippet UserSnippet) bool {
	validName := snippet.Name != "" && len(snippet.Name) <= UserSnippetNameMaxBytes && utf8.ValidString(snippet.Name)
	for _, r := range snippet.Name {
		validName = validName && validSnippetNameRune(r)
	}
	return snippet.ID != "" && len(snippet.ID) <= 128 && utf8.ValidString(snippet.ID) &&
		validName && !isReservedSnippetName(snippet.Name) &&
		strings.TrimSpace(snippet.Body) != "" && len(snippet.Body) <= UserSnippetBodyMaxBytes &&
		utf8.ValidString(snippet.Body) && snippet.UpdatedAt >= 0
}

func validateUserSnippets(snips []UserSnippet) error {
	if len(snips) > UserSnippetsMaxCount {
		return fmt.Errorf("too many user snippets (%d, maximum %d)", len(snips), UserSnippetsMaxCount)
	}
	ids := make(map[string]struct{}, len(snips))
	names := make(map[string]struct{}, len(snips))
	for i, snippet := range snips {
		if !validUserSnippet(snippet) {
			return fmt.Errorf("corrupt user_snippets.json: invalid snippet at index %d", i)
		}
		lname := strings.ToLower(snippet.Name)
		if _, duplicate := ids[snippet.ID]; duplicate {
			return fmt.Errorf("corrupt user_snippets.json: duplicate ID %q", snippet.ID)
		}
		if _, duplicate := names[lname]; duplicate {
			return fmt.Errorf("corrupt user_snippets.json: duplicate name %q", snippet.Name)
		}
		ids[snippet.ID] = struct{}{}
		names[lname] = struct{}{}
	}
	return nil
}

func saveUserSnippets(snips []UserSnippet) error {
	// Strict on the way out: loading tolerates entries an older build wrote,
	// but nothing invalid may be written back.
	if err := validateUserSnippets(snips); err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if len(snips) == 0 {
		// Empty list → remove the file rather than write `[]`.
		if err := os.Remove(userSnippetsPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(snips, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > UserSnippetsFileMaxBytes {
		return fmt.Errorf("user snippets file would exceed %d bytes", UserSnippetsFileMaxBytes)
	}
	return atomicWriteFile(userSnippetsPath(), data, 0o600)
}

// validSnippetNameRune restricts snippet names to characters that won't
// confuse the frontend slash parser. Letters, digits, and dashes are safe;
// spaces, slashes, and special chars would break /name parsing.
func validSnippetNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_'
}

func isReservedSnippetName(name string) bool {
	switch strings.ToLower(name) {
	case "btw", "clear", "export", "exportall", "summarize", "system", "search", "memory", "budget",
		"sessions", "exportjson", "importsession", "help":
		return true
	default:
		return false
	}
}

// SaveUserSnippet persists a new snippet OR updates one with the same Name
// (case-insensitive). Returns the snippet's ID.
//
// Validation:
//   - Name required, 1-30 chars, only [A-Za-z0-9_-]; leading "/" is stripped
//     so "/lint" and "lint" both work
//   - Body required, capped at UserSnippetBodyMaxBytes
//   - Built-in slash command names ("clear", "export", "help" etc.) are
//     reserved — refusing them prevents the user from accidentally shadowing
//     a built-in
func (s *Studio) SaveUserSnippet(name, body string) (string, error) {
	if !utf8.ValidString(name) || !utf8.ValidString(body) {
		return "", fmt.Errorf("snippet fields must be valid UTF-8")
	}
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", fmt.Errorf("snippet name cannot be empty")
	}
	if len(name) > UserSnippetNameMaxBytes {
		return "", fmt.Errorf("snippet name cannot exceed %d characters", UserSnippetNameMaxBytes)
	}
	for _, r := range name {
		if !validSnippetNameRune(r) {
			return "", fmt.Errorf("snippet name can only contain letters, digits, hyphens and underscores (got %q)", r)
		}
	}
	// Reserve names that conflict with built-in slash commands. Keep this in
	// sync with the SLASH_COMMANDS array in ChatPanel.tsx.
	if isReservedSnippetName(name) {
		return "", fmt.Errorf("%q is a built-in command name and cannot be used as a snippet", name)
	}
	body = strings.TrimRight(body, " \t\r\n")
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("snippet body cannot be empty")
	}
	if len(body) > UserSnippetBodyMaxBytes {
		body = truncateUTF8(body, UserSnippetBodyMaxBytes)
	}

	userSnippetsMu.Lock()
	defer userSnippetsMu.Unlock()
	snips, err := loadUserSnippets()
	if err != nil {
		return "", err
	}

	// Dedup by lowercased name.
	lname := strings.ToLower(name)
	now := time.Now().UnixMilli()
	for i := range snips {
		if strings.ToLower(snips[i].Name) == lname {
			snips[i].Name = name
			snips[i].Body = body
			snips[i].UpdatedAt = now
			if err := saveUserSnippets(snips); err != nil {
				return "", err
			}
			return snips[i].ID, nil
		}
	}

	if len(snips) >= UserSnippetsMaxCount {
		return "", fmt.Errorf("user snippet limit reached (%d)", UserSnippetsMaxCount)
	}
	newS := UserSnippet{
		ID:        uuid.New().String()[:12],
		Name:      name,
		Body:      body,
		UpdatedAt: now,
	}
	snips = append(snips, newS)
	if err := saveUserSnippets(snips); err != nil {
		return "", err
	}
	return newS.ID, nil
}

// DeleteUserSnippet removes a snippet by ID. Idempotent — removing a
// non-existent ID is not an error.
func (s *Studio) DeleteUserSnippet(id string) error {
	if id == "" {
		return fmt.Errorf("id cannot be empty")
	}
	userSnippetsMu.Lock()
	defer userSnippetsMu.Unlock()
	snips, err := loadUserSnippets()
	if err != nil {
		return err
	}
	out := snips[:0]
	for _, t := range snips {
		if t.ID == id {
			continue
		}
		out = append(out, t)
	}
	return saveUserSnippets(out)
}

// ListUserSnippets returns all saved snippets sorted alphabetically by name
// (slash autocomplete users want predictable ordering, not "recently
// edited"). Returns an empty slice rather than nil for easy frontend
// handling.
func (s *Studio) ListUserSnippets() ([]UserSnippet, error) {
	userSnippetsMu.Lock()
	defer userSnippetsMu.Unlock()
	snips, err := loadUserSnippets()
	if err != nil {
		return nil, err
	}
	if snips == nil {
		return []UserSnippet{}, nil
	}
	sort.SliceStable(snips, func(i, j int) bool {
		return strings.ToLower(snips[i].Name) < strings.ToLower(snips[j].Name)
	})
	return snips, nil
}

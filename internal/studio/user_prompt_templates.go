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

// User-defined system-prompt templates: extension of the curated library
// in prompt_templates.go that lets users save their own presets. Users
// typically tweak a curated template ("Code Reviewer") with project-
// specific bits (frameworks, conventions, repo-layout notes) and want
// that result available for the next project they work on.
//
// Storage: a single JSON file at `<configDir>/user_prompt_templates.json`
// holding an array of PromptTemplate records. Same flat-file pattern as
// drafts and pins. All user templates land in the implicit category
// "Yours" — we don't expose category as an editable field because that
// would multiply UI complexity for marginal benefit.

const userPromptTemplatesCategory = "Yours"

// UserPromptTemplate adds an editable timestamp on top of PromptTemplate
// so the picker can sort newest-first / show "saved 3 days ago".
type UserPromptTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	UpdatedAt   int64  `json:"updatedAt"` // unix millis
}

// UserPromptTemplateMaxFields keeps a runaway paste from filling the file.
// Frontend already caps the system-prompt textarea at 20k chars; this
// matches that for the prompt body and applies tighter caps to the
// metadata fields.
const (
	UserPromptNameMaxBytes          = 80
	UserPromptDescriptionMaxBytes   = 200
	UserPromptPromptMaxBytes        = 20 * 1024 // 20 KB matches frontend textarea cap
	UserPromptTemplatesMaxCount     = 100
	UserPromptTemplatesFileMaxBytes = 3 << 20
)

var userPromptTemplatesMu sync.Mutex

func userPromptTemplatesPath() string {
	return filepath.Join(configDir(), "user_prompt_templates.json")
}

func loadUserPromptTemplates() ([]UserPromptTemplate, error) {
	data, err := readRegularFileLimited(userPromptTemplatesPath(), UserPromptTemplatesFileMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var tmpls []UserPromptTemplate
	if err := json.Unmarshal(data, &tmpls); err != nil {
		return nil, fmt.Errorf("corrupt user_prompt_templates.json: %w", err)
	}
	if err := validateUserPromptTemplates(tmpls); err != nil {
		return nil, err
	}
	return tmpls, nil
}

func validateUserPromptTemplates(tmpls []UserPromptTemplate) error {
	if len(tmpls) > UserPromptTemplatesMaxCount {
		return fmt.Errorf("too many user prompt templates (%d, maximum %d)", len(tmpls), UserPromptTemplatesMaxCount)
	}
	ids := make(map[string]struct{}, len(tmpls))
	names := make(map[string]struct{}, len(tmpls))
	for i, tmpl := range tmpls {
		if tmpl.ID == "" || len(tmpl.ID) > 128 || !utf8.ValidString(tmpl.ID) ||
			strings.TrimSpace(tmpl.Name) == "" || len(tmpl.Name) > UserPromptNameMaxBytes || !utf8.ValidString(tmpl.Name) ||
			len(tmpl.Description) > UserPromptDescriptionMaxBytes || !utf8.ValidString(tmpl.Description) ||
			strings.TrimSpace(tmpl.Prompt) == "" || len(tmpl.Prompt) > UserPromptPromptMaxBytes || !utf8.ValidString(tmpl.Prompt) ||
			tmpl.UpdatedAt < 0 {
			return fmt.Errorf("corrupt user_prompt_templates.json: invalid template at index %d", i)
		}
		lname := strings.ToLower(tmpl.Name)
		if _, duplicate := ids[tmpl.ID]; duplicate {
			return fmt.Errorf("corrupt user_prompt_templates.json: duplicate ID %q", tmpl.ID)
		}
		if _, duplicate := names[lname]; duplicate {
			return fmt.Errorf("corrupt user_prompt_templates.json: duplicate name %q", tmpl.Name)
		}
		ids[tmpl.ID] = struct{}{}
		names[lname] = struct{}{}
	}
	return nil
}

func saveUserPromptTemplates(tmpls []UserPromptTemplate) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if len(tmpls) == 0 {
		// Empty list → remove the file rather than write `[]`.
		if err := os.Remove(userPromptTemplatesPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(tmpls, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > UserPromptTemplatesFileMaxBytes {
		return fmt.Errorf("user prompt templates file would exceed %d bytes", UserPromptTemplatesFileMaxBytes)
	}
	return atomicWriteFile(userPromptTemplatesPath(), data, 0o600)
}

// SaveUserPromptTemplate persists a new user-defined template OR updates an
// existing one with the same Name (case-insensitive). Returns the template's
// ID. Using Name as the dedup key matches user expectation — they typically
// "Save as <descriptive name>" and don't want a UUID-key forcing them to
// remember which copy is current.
//
// Validation:
//   - Name required (after TrimSpace), capped at UserPromptNameMaxBytes
//   - Prompt required, capped at UserPromptPromptMaxBytes
//   - Description optional, capped at UserPromptDescriptionMaxBytes
func (s *Studio) SaveUserPromptTemplate(name, description, prompt string) (string, error) {
	if !utf8.ValidString(name) || !utf8.ValidString(description) || !utf8.ValidString(prompt) {
		return "", fmt.Errorf("template fields must be valid UTF-8")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name cannot be empty")
	}
	if len(name) > UserPromptNameMaxBytes {
		name = truncateUTF8(name, UserPromptNameMaxBytes)
	}
	prompt = strings.TrimRight(prompt, " \t\r\n") // keep leading whitespace intact (might be intentional indentation)
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("prompt cannot be empty")
	}
	if len(prompt) > UserPromptPromptMaxBytes {
		prompt = truncateUTF8(prompt, UserPromptPromptMaxBytes)
	}
	description = strings.TrimSpace(description)
	if len(description) > UserPromptDescriptionMaxBytes {
		description = truncateUTF8(description, UserPromptDescriptionMaxBytes)
	}

	userPromptTemplatesMu.Lock()
	defer userPromptTemplatesMu.Unlock()
	tmpls, err := loadUserPromptTemplates()
	if err != nil {
		return "", err
	}

	// Dedup by lowercased name. If found, update in place (preserves ID so
	// callers holding a reference don't get a stale pointer).
	lname := strings.ToLower(name)
	now := time.Now().UnixMilli()
	for i := range tmpls {
		if strings.ToLower(tmpls[i].Name) == lname {
			tmpls[i].Name = name
			tmpls[i].Description = description
			tmpls[i].Prompt = prompt
			tmpls[i].UpdatedAt = now
			if err := saveUserPromptTemplates(tmpls); err != nil {
				return "", err
			}
			return tmpls[i].ID, nil
		}
	}

	// New template.
	if len(tmpls) >= UserPromptTemplatesMaxCount {
		return "", fmt.Errorf("user prompt template limit reached (%d)", UserPromptTemplatesMaxCount)
	}
	newT := UserPromptTemplate{
		ID:          uuid.New().String()[:12],
		Name:        name,
		Description: description,
		Prompt:      prompt,
		UpdatedAt:   now,
	}
	tmpls = append(tmpls, newT)
	if err := saveUserPromptTemplates(tmpls); err != nil {
		return "", err
	}
	return newT.ID, nil
}

// DeleteUserPromptTemplate removes a template by its ID. Idempotent:
// removing a non-existent ID is not an error (handles double-click race).
func (s *Studio) DeleteUserPromptTemplate(id string) error {
	if id == "" {
		return fmt.Errorf("id cannot be empty")
	}
	userPromptTemplatesMu.Lock()
	defer userPromptTemplatesMu.Unlock()
	tmpls, err := loadUserPromptTemplates()
	if err != nil {
		return err
	}
	out := tmpls[:0]
	for _, t := range tmpls {
		if t.ID == id {
			continue
		}
		out = append(out, t)
	}
	return saveUserPromptTemplates(out)
}

// ListUserPromptTemplates returns saved user templates sorted newest-first
// (matches the curated picker's "recently used" feel). Returns an empty
// slice rather than nil for easy frontend handling.
//
// Templates are projected onto the same PromptTemplate shape as the curated
// set so the frontend picker can mix them into a single list with category
// "Yours" — no separate code path needed.
func (s *Studio) ListUserPromptTemplates() ([]PromptTemplate, error) {
	userPromptTemplatesMu.Lock()
	defer userPromptTemplatesMu.Unlock()
	tmpls, err := loadUserPromptTemplates()
	if err != nil {
		return nil, err
	}
	if tmpls == nil {
		return []PromptTemplate{}, nil
	}
	sort.SliceStable(tmpls, func(i, j int) bool {
		return tmpls[i].UpdatedAt > tmpls[j].UpdatedAt
	})
	out := make([]PromptTemplate, 0, len(tmpls))
	for _, t := range tmpls {
		desc := t.Description
		if desc == "" {
			// Provide a fallback so the picker doesn't render an empty
			// row underneath the name. Format an absolute-but-cheap
			// "saved <date>" string the user can see at a glance.
			desc = "Saved " + time.UnixMilli(t.UpdatedAt).Format("2006-01-02")
		}
		out = append(out, PromptTemplate{
			ID:          t.ID,
			Name:        t.Name,
			Category:    userPromptTemplatesCategory,
			Description: desc,
			Prompt:      t.Prompt,
		})
	}
	return out, nil
}

package context

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/fileutil"
	"github.com/ginkida/gokin-studio/internal/engine/logging"

	"google.golang.org/genai"
)

const maxSessionMemoryBytes int64 = 4 << 20

// SessionMemoryConfig holds configuration for automatic session memory extraction.
type SessionMemoryConfig struct {
	Enabled                 bool `yaml:"enabled"`
	MinTokensToInit         int  `yaml:"min_tokens_to_init"`         // Start after N tokens (default: 10000)
	MinTokensBetweenUpdates int  `yaml:"min_tokens_between_updates"` // Update every N new tokens (default: 5000)
	ToolCallsBetweenUpdates int  `yaml:"tool_calls_between_updates"` // Or every N tool calls (default: 3)
}

// DefaultSessionMemoryConfig returns default session memory configuration.
func DefaultSessionMemoryConfig() SessionMemoryConfig {
	return SessionMemoryConfig{
		Enabled:                 true,
		MinTokensToInit:         10000,
		MinTokensBetweenUpdates: 5000,
		ToolCallsBetweenUpdates: 3,
	}
}

// SessionMemoryManager maintains a structured summary of the current session.
// It periodically extracts key information from the conversation history and
// writes it to .gokin/.session-memory.md for injection into the system prompt.
type SessionMemoryManager struct {
	workDir string
	config  SessionMemoryConfig

	// Tracking thresholds
	lastExtractionTokens int
	toolCallsSinceUpdate int
	extractionCount      int
	initialized          bool

	// LLM summarizer (optional — set via SetSummarizer)
	summarizer SessionSummarizer

	// Callback fired after each successful extraction (for UI notifications)
	onUpdate func()

	// Extracted content
	content  string
	revision uint64

	mu sync.RWMutex
}

// SetOnUpdate sets a callback invoked after each successful session memory extraction.
func (s *SessionMemoryManager) SetOnUpdate(cb func()) {
	s.mu.Lock()
	s.onUpdate = cb
	s.mu.Unlock()
}

// SessionSummarizer generates a text summary from conversation history.
type SessionSummarizer interface {
	Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error)
}

// SetSummarizer enables LLM-based session memory extraction.
// When set, every Nth extraction uses the LLM for a higher-quality summary.
func (s *SessionMemoryManager) SetSummarizer(sum SessionSummarizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summarizer = sum
}

// NewSessionMemoryManager creates a new session memory manager.
func NewSessionMemoryManager(workDir string, config SessionMemoryConfig) *SessionMemoryManager {
	return &SessionMemoryManager{
		workDir: workDir,
		config:  config,
	}
}

// ShouldExtract checks whether extraction thresholds have been met.
func (s *SessionMemoryManager) ShouldExtract(currentTokens int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.config.Enabled {
		return false
	}

	// First extraction: wait for minimum tokens
	if !s.initialized {
		return currentTokens >= s.config.MinTokensToInit
	}

	// Subsequent: check token delta or tool call count
	tokenDelta := currentTokens - s.lastExtractionTokens
	if tokenDelta >= s.config.MinTokensBetweenUpdates {
		return true
	}
	if s.toolCallsSinceUpdate >= s.config.ToolCallsBetweenUpdates {
		return true
	}

	return false
}

// RecordToolCall increments the tool call counter.
func (s *SessionMemoryManager) RecordToolCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCallsSinceUpdate++
}

// ExtractAsync copies the history slice and runs extraction in a background goroutine.
// This is the safe entry point — callers don't need to worry about race conditions.
func (s *SessionMemoryManager) ExtractAsync(history []*genai.Content, currentTokens int) {
	s.mu.Lock()
	s.revision++
	revision := s.revision
	s.mu.Unlock()
	snapshot := cloneSessionMemoryHistory(history)
	go s.extract(snapshot, currentTokens, revision)
}

func cloneSessionMemoryHistory(history []*genai.Content) []*genai.Content {
	snapshot := make([]*genai.Content, len(history))
	for i, content := range history {
		if content == nil {
			continue
		}
		data, err := json.Marshal(content)
		if err == nil {
			var cloned genai.Content
			if json.Unmarshal(data, &cloned) == nil {
				snapshot[i] = &cloned
				continue
			}
		}
		// genai.Content is JSON-shaped in normal operation. A serialization
		// failure is safer to omit than to retain mutable nested map pointers in
		// a background snapshot.
		snapshot[i] = &genai.Content{Role: content.Role}
	}
	return snapshot
}

// Extract extracts session memory from conversation history using heuristic analysis.
// This does NOT make any LLM calls — it uses pattern matching on the history.
func (s *SessionMemoryManager) Extract(history []*genai.Content, currentTokens int) {
	s.mu.Lock()
	s.revision++
	revision := s.revision
	s.mu.Unlock()
	s.extract(history, currentTokens, revision)
}

func (s *SessionMemoryManager) extract(history []*genai.Content, currentTokens int, revision uint64) {
	if len(history) < 4 {
		return // Too little history
	}

	var builder strings.Builder
	builder.WriteString("# Session Memory\n")
	builder.WriteString(fmt.Sprintf("_Updated: %s_\n\n", time.Now().Format("15:04")))

	// Extract current task from the last few user messages
	if task := extractCurrentTask(history); task != "" {
		builder.WriteString("## Current State\n")
		builder.WriteString(task)
		builder.WriteString("\n\n")
	}

	// Extract files mentioned (read/written/edited)
	files := extractFileActivity(history)
	if len(files) > 0 {
		builder.WriteString("## Files and Functions\n")
		for _, f := range files {
			builder.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Action))
		}
		builder.WriteString("\n")
	}

	// Extract tool usage patterns
	toolCounts := extractToolUsage(history)
	if len(toolCounts) > 0 {
		builder.WriteString("## Workflow\n")
		// Sort by count descending
		type tc struct {
			name  string
			count int
		}
		var sorted []tc
		for k, v := range toolCounts {
			sorted = append(sorted, tc{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
		var parts []string
		for _, t := range sorted {
			parts = append(parts, fmt.Sprintf("%s (%dx)", t.name, t.count))
		}
		builder.WriteString("Tools used: ")
		builder.WriteString(strings.Join(parts, ", "))
		builder.WriteString("\n\n")
	}

	// Extract errors encountered
	errors := extractErrors(history)
	if len(errors) > 0 {
		builder.WriteString("## Errors & Corrections\n")
		for _, e := range errors {
			builder.WriteString(fmt.Sprintf("- %s\n", e))
		}
		builder.WriteString("\n")
	}

	heuristicContent := builder.String()

	s.mu.Lock()
	if revision != s.revision {
		s.mu.Unlock()
		return
	}
	s.extractionCount++
	s.lastExtractionTokens = currentTokens
	s.toolCallsSinceUpdate = 0
	s.initialized = true
	s.content = heuristicContent
	s.writeToDiskLocked()

	// Every 3rd extraction, try LLM-based summarization for higher quality
	summarizer := s.summarizer
	useLLM := summarizer != nil && s.extractionCount%3 == 0 && len(history) >= 10
	onUpdate := s.onUpdate
	s.mu.Unlock()
	if useLLM {
		go s.extractWithLLM(history, revision, summarizer)
	}

	logging.Debug("session memory extracted",
		"files", len(files),
		"tools", len(toolCounts),
		"errors", len(errors),
		"tokens", currentTokens)

	// Notify UI about session memory update
	if onUpdate != nil {
		onUpdate()
	}
}

// extractWithLLM uses the Summarizer to create a higher-quality session summary.
// Runs in a goroutine; falls back to heuristic content on failure.
func (s *SessionMemoryManager) extractWithLLM(history []*genai.Content, revision uint64, summarizer SessionSummarizer) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prompt := `Summarize this coding session into a structured markdown format:

# Session Memory
## Current State
(What is being worked on right now?)
## Files and Functions
(List important files mentioned, with what was done to each)
## Workflow
(Key tools used, commands run)
## Errors & Corrections
(Errors encountered and how they were resolved)
## Key Decisions
(Important architectural or design decisions made)

Be concise. Each section should be 1-5 bullet points maximum.`

	summary, err := summarizer.Summarize(ctx, history, prompt)
	if err != nil || summary == "" {
		logging.Debug("LLM session memory extraction failed, keeping heuristic", "error", err)
		return
	}

	s.mu.Lock()
	if revision != s.revision {
		s.mu.Unlock()
		return
	}
	s.content = summary
	s.writeToDiskLocked()
	onUpdate := s.onUpdate
	s.mu.Unlock()
	logging.Debug("LLM session memory extraction completed", "size", len(summary))

	if onUpdate != nil {
		onUpdate()
	}
}

// GetContent returns the current session memory content for prompt injection.
func (s *SessionMemoryManager) GetContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.content
}

// LoadFromDisk loads previously saved session memory.
func (s *SessionMemoryManager) LoadFromDisk() {
	path := s.filePath()
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := fileutil.ReadRegularFileLimited(path, maxSessionMemoryBytes)
	if err != nil {
		return // File doesn't exist yet
	}
	s.revision++
	s.content = string(data)
	s.initialized = true
	logging.Debug("loaded session memory from disk", "path", path)
}

// Clear resets session memory (e.g., on new session).
func (s *SessionMemoryManager) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.content = ""
	s.initialized = false
	s.lastExtractionTokens = 0
	s.toolCallsSinceUpdate = 0
	os.Remove(s.filePath())
}

func (s *SessionMemoryManager) filePath() string {
	dir := filepath.Join(s.workDir, ".gokin")
	return filepath.Join(dir, ".session-memory.md")
}

// writeToDiskLocked persists the revision currently protected by s.mu.
func (s *SessionMemoryManager) writeToDiskLocked() {
	dir := filepath.Join(s.workDir, ".gokin")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		logging.Debug("failed to create .gokin dir for session memory", "error", err)
		return
	}
	if err := fileutil.AtomicWrite(s.filePath(), []byte(s.content), 0o640); err != nil {
		logging.Debug("failed to write session memory", "error", err)
	}
}

// --- Heuristic extraction helpers ---

// fileActivity represents a file path and what happened to it.
type fileActivity struct {
	Path   string
	Action string // "read", "edited", "created", "searched"
}

var filePathPattern = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])([a-zA-Z0-9_./\\-]+\.[a-zA-Z0-9]+)`)

func extractCurrentTask(history []*genai.Content) string {
	// Find the last substantive user message (skip short "ok", "yes" etc.)
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg == nil {
			continue
		}
		if msg.Role != genai.RoleUser {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.Text != "" && len(part.Text) > 20 {
				text := part.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				return text
			}
		}
	}
	return ""
}

func extractFileActivity(history []*genai.Content) []fileActivity {
	seen := make(map[string]string) // path -> action

	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil {
				fc := part.FunctionCall
				path, _ := fc.Args["file_path"].(string)
				if path == "" {
					path, _ = fc.Args["path"].(string)
				}
				if path == "" {
					continue
				}
				switch fc.Name {
				case "read":
					if _, exists := seen[path]; !exists {
						seen[path] = "read"
					}
				case "write":
					seen[path] = "created"
				case "edit":
					seen[path] = "edited"
				case "grep", "glob":
					if _, exists := seen[path]; !exists {
						seen[path] = "searched"
					}
				}
			}
		}
	}

	var result []fileActivity
	for path, action := range seen {
		result = append(result, fileActivity{Path: path, Action: action})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })

	// Limit to most important files
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

func extractToolUsage(history []*genai.Content) map[string]int {
	counts := make(map[string]int)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil {
				counts[part.FunctionCall.Name]++
			}
		}
	}
	return counts
}

var errorPatterns = []string{
	"error:", "Error:", "ERROR:",
	"failed:", "Failed:", "FAILED:",
	"panic:", "PANIC:",
	"permission denied",
	"no such file",
	"compilation failed",
	"test failed",
	"syntax error",
}

func extractErrors(history []*genai.Content) []string {
	var errors []string
	seen := make(map[string]bool)

	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionResponse == nil {
				continue
			}
			resp := part.FunctionResponse.Response
			if resp == nil {
				continue
			}
			errMsg, _ := resp["error"].(string)
			content, _ := resp["content"].(string)

			text := errMsg + " " + content
			for _, pattern := range errorPatterns {
				if strings.Contains(text, pattern) {
					// Extract the relevant line
					line := extractErrorLine(text, pattern)
					if line != "" && !seen[line] {
						seen[line] = true
						errors = append(errors, line)
					}
					break
				}
			}
		}
	}

	// Limit to last 20 errors
	if len(errors) > 20 {
		errors = errors[len(errors)-20:]
	}
	return errors
}

func extractErrorLine(text, pattern string) string {
	idx := strings.Index(text, pattern)
	if idx < 0 {
		return ""
	}

	// Extract from pattern to end of line
	start := idx
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		end = len(text) - start
	}
	line := strings.TrimSpace(text[start : start+end])
	if len(line) > 150 {
		line = line[:150] + "..."
	}
	return line
}

// ClientSessionSummarizer wraps a client.Client to implement SessionSummarizer.
// It sends the conversation history with a summarization prompt to the LLM
// and returns the generated text summary.
type ClientSessionSummarizer struct {
	client client.Client
}

// NewClientSessionSummarizer creates a summarizer adapter from a Client.
func NewClientSessionSummarizer(c client.Client) *ClientSessionSummarizer {
	return &ClientSessionSummarizer{client: c}
}

func (s *ClientSessionSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	// Take last 20 messages for summarization (avoid sending entire history)
	msgs := history
	if len(msgs) > 20 {
		msgs = msgs[len(msgs)-20:]
	}

	stream, err := s.client.SendMessageWithHistory(ctx, msgs, prompt)
	if err != nil {
		return "", err
	}

	resp, err := stream.Collect()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}

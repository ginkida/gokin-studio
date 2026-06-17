package studio

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// stagnationLimit is the number of consecutive identical tool-call fingerprints
// that triggers the loop-guard. Mirrors gokin's executor default.
const stagnationLimit = 5

// stagnationFingerprintArg extracts a positional integer argument from a tool
// call, tolerating both int and float64 (JSON unmarshal picks float64 by
// default). Missing or unparseable values return 0.
func stagnationFingerprintArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// stagnationFingerprint returns a short string that distinguishes different
// invocations of the same tool. Identical fingerprints mean true stagnation
// (e.g. writing the same file 5 times), not forward progress (writing 5 files).
//
// Ported from gokin internal/tools/executor.go.
func stagnationFingerprint(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		fp, ok := args["file_path"].(string)
		if !ok || fp == "" {
			return ""
		}
		return fmt.Sprintf("%s@%d+%d",
			filepath.Base(fp),
			stagnationFingerprintArg(args, "offset"),
			stagnationFingerprintArg(args, "limit"))
	case "write", "delete":
		if fp, ok := args["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "edit":
		fp, _ := args["file_path"].(string)
		if fp == "" {
			return ""
		}
		base := filepath.Base(fp)
		if old, _ := args["old_string"].(string); old != "" {
			h := sha256.Sum256([]byte(old))
			return fmt.Sprintf("%s@%x", base, h[:4])
		}
		if edits, ok := args["edits"].([]any); ok && len(edits) > 0 {
			h := sha256.Sum256([]byte(fmt.Sprintf("%v", edits)))
			return fmt.Sprintf("%s@edits:%x", base, h[:4])
		}
		if ls, le := stagnationFingerprintArg(args, "line_start"), stagnationFingerprintArg(args, "line_end"); ls > 0 || le > 0 {
			return fmt.Sprintf("%s@L%d-%d", base, ls, le)
		}
		return base
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if _, after, ok := strings.Cut(cmd, " && "); ok && strings.HasPrefix(strings.TrimSpace(cmd), "cd ") {
				cmd = after
			}
			if runes := []rune(cmd); len(runes) > 60 {
				cmd = string(runes[:60])
			}
			return cmd
		}
	case "grep":
		if p, ok := args["pattern"].(string); ok {
			path, _ := args["path"].(string)
			if path == "" {
				return p
			}
			return p + "@" + path
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok {
			path, _ := args["path"].(string)
			if path == "" {
				return p
			}
			return p + "@" + path
		}
	case "copy", "move":
		src, _ := args["source"].(string)
		dst, _ := args["destination"].(string)
		if src != "" {
			return filepath.Base(src) + "->" + filepath.Base(dst)
		}
	case "git_add":
		if p, ok := args["path"].(string); ok {
			return p
		}
	case "web_fetch":
		if u, ok := args["url"].(string); ok {
			if runes := []rune(u); len(runes) > 50 {
				u = string(runes[:50])
			}
			return u
		}
	case "web_search":
		if q, ok := args["query"].(string); ok {
			return q
		}
	case "run_tests", "verify_code":
		path, _ := args["path"].(string)
		filter, _ := args["filter"].(string)
		if path == "" && filter == "" {
			return ""
		}
		return path + "|" + filter
	}
	return ""
}

// stagnationKey returns "toolName:fingerprint" — the full loop-guard key used
// to track recent patterns in recentToolPatterns.
func stagnationKey(toolName string, args map[string]any) string {
	fp := stagnationFingerprint(toolName, args)
	if fp == "" {
		return toolName
	}
	return toolName + ":" + fp
}

// checkStagnation returns true when the last stagnationLimit entries of
// patterns are all equal to pattern. Used by the project.go tool loop to
// detect stuck repetitions before executing the tool.
func checkStagnation(patterns []string, pattern string) bool {
	if len(patterns) < stagnationLimit {
		return false
	}
	tail := patterns[len(patterns)-stagnationLimit:]
	for _, p := range tail {
		if p != pattern {
			return false
		}
	}
	return true
}

// buildStagnationMessage builds a loop-guard error message targeted to the
// specific tool class so the model gets actionable recovery advice.
func buildStagnationMessage(toolName string, args map[string]any, count int) string {
	target := describeStagnationTarget(toolName, args)
	switch toolName {
	case "read":
		return fmt.Sprintf(
			"Loop guard: identical %s repeated %d times. Do not call it again — you already have this file's content in the conversation above; reuse it. To see other code, change offset/limit or read a different file. If you were about to edit, make the edit now.",
			target, count,
		)
	case "grep", "glob", "list_dir", "tree":
		return fmt.Sprintf(
			"Loop guard: identical %s repeated %d times. Do not call it again — the results are already above; reuse them. Re-running the same search will not help: try a different pattern or path, or proceed with what you found.",
			target, count,
		)
	case "edit":
		if old, _ := args["old_string"].(string); old != "" {
			return fmt.Sprintf(
				"Loop guard: the same %s was attempted %d times and keeps failing. Do not call it again unchanged — old_string matching is exact and whitespace-sensitive. Read the current file to copy the exact text, then retry; or switch to line_start/line_end mode.",
				target, count,
			)
		}
		return fmt.Sprintf(
			"Loop guard: the same %s was attempted %d times and is not making progress. Do not call it again unchanged — re-read the current file to recheck exact line numbers and content, then retry with corrected coordinates or take a different approach.",
			target, count,
		)
	default:
		return fmt.Sprintf(
			"Loop guard: identical %s request repeated %d times. This exact tool call already ran and repeating it will not make progress. Do not call it again. Reuse the earlier result, choose a different target, or answer the user.",
			target, count,
		)
	}
}

func describeStagnationTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		filePath, _ := args["file_path"].(string)
		target := "read"
		if filePath != "" {
			target += " " + filepath.Base(filePath)
		}
		offset := stagnationFingerprintArg(args, "offset")
		limit := stagnationFingerprintArg(args, "limit")
		if offset > 0 || limit > 0 {
			target = fmt.Sprintf("%s (offset %d, limit %d)", target, offset, limit)
		}
		return target
	case "grep":
		pattern, _ := args["pattern"].(string)
		target := "grep"
		if pattern != "" {
			target += fmt.Sprintf(" %q", pattern)
		}
		if path, ok := args["path"].(string); ok && path != "" {
			target += " in " + path
		}
		return target
	case "glob":
		pattern, _ := args["pattern"].(string)
		target := "glob"
		if pattern != "" {
			target += fmt.Sprintf(" %q", pattern)
		}
		if path, ok := args["path"].(string); ok && path != "" {
			target += " in " + path
		}
		return target
	default:
		if fp := stagnationFingerprint(toolName, args); fp != "" {
			return fmt.Sprintf("%s (%s)", toolName, fp)
		}
		return toolName
	}
}

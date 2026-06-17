package tools

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/genai"

	"github.com/ginkida/gokin-studio/internal/engine/client"
)

// maxStagnationRecoveryAttempts returns how many recovery hints a looping tool
// earns before the executor falls back to the hard abort. The recovery itself
// NEVER re-executes the tool — it returns a "stop looping" hint (plus cached
// content for read-only tools) — so handing out hints is always side-effect free.
//   - read-only idempotent tools: 3. A re-read/re-search loop is benign and the
//     model almost always course-corrects once it gets the content back.
//   - edit: 1, paired with a tailored "old_string isn't matching" hint.
//   - everything else: 0 — repeating an identical mutating/opaque call is treated
//     as an immediate abort.
func maxStagnationRecoveryAttempts(toolName string) int {
	switch toolName {
	case "read", "grep", "glob", "list_dir", "tree":
		return 3
	case "edit":
		return 1
	default:
		return 0
	}
}

// shouldAttemptStagnationRecovery reports whether the current looping batch
// should get another recovery hint instead of aborting the turn. The per-batch
// budget is the most restrictive maxStagnationRecoveryAttempts across all calls,
// so a mixed batch containing any non-recoverable tool aborts immediately.
func shouldAttemptStagnationRecovery(calls []*genai.FunctionCall, attempts int) bool {
	if len(calls) == 0 {
		return false
	}
	budget := -1
	for _, call := range calls {
		if call == nil {
			return false
		}
		m := maxStagnationRecoveryAttempts(call.Name)
		if m == 0 {
			return false
		}
		if budget < 0 || m < budget {
			budget = m
		}
	}
	return attempts < budget
}

// buildStagnationRecoveryMessage builds the hint returned in place of the
// looping tool call. It is tool-class aware so the model gets actionable advice
// for the specific way it's stuck — not a one-size-fits-all "stop" — while every
// branch keeps the literal "Do not call it again" phrase.
func buildStagnationRecoveryMessage(toolName string, args map[string]any, repeatCount int) string {
	target := describeStagnationTarget(toolName, args)
	switch toolName {
	case "read":
		return fmt.Sprintf(
			"Loop guard: identical %s repeated %d times. Do not call it again — you already have this file's content in the conversation above; reuse it. To see other code, change offset/limit or read a different file. If you were about to edit, make the edit now.",
			target, repeatCount,
		)
	case "grep", "glob", "list_dir", "tree":
		return fmt.Sprintf(
			"Loop guard: identical %s repeated %d times. Do not call it again — the results are already above; reuse them. Re-running the same search will not help: try a different pattern or path, or proceed with what you found.",
			target, repeatCount,
		)
	case "edit":
		if old, _ := args["old_string"].(string); old != "" {
			return fmt.Sprintf(
				"Loop guard: the same %s was attempted %d times and keeps failing. Do not call it again unchanged — old_string matching is exact and whitespace-sensitive, so it is not matching the file. Read the current file to copy the exact text, then retry; or switch to line_start/line_end or regex mode.",
				target, repeatCount,
			)
		}
		return fmt.Sprintf(
			"Loop guard: the same %s was attempted %d times and is not making progress. Do not call it again unchanged — re-read the current file to recheck the exact line numbers and content, then retry with corrected coordinates or take a different approach.",
			target, repeatCount,
		)
	default:
		return fmt.Sprintf(
			"Loop guard: identical %s request repeated %d times. This exact tool call already ran and repeating it will not make progress. Do not call it again. Reuse the earlier result, choose a different target, or answer the user.",
			target, repeatCount,
		)
	}
}

func buildStagnationWarningMessage(calls []*genai.FunctionCall, repeatCount int) string {
	if len(calls) == 0 || calls[0] == nil {
		return fmt.Sprintf(
			"Loop guard: repeated the same tool pattern %d times. Sent a recovery hint instead of rerunning it.",
			repeatCount,
		)
	}
	if len(calls) == 1 {
		return fmt.Sprintf(
			"Loop guard: repeated %s %d times. Sent a recovery hint instead of rerunning it.",
			describeStagnationTarget(calls[0].Name, calls[0].Args),
			repeatCount,
		)
	}
	return fmt.Sprintf(
		"Loop guard: repeated the same %s pattern %d times. Sent a recovery hint instead of rerunning it.",
		describeStagnationTarget(calls[0].Name, calls[0].Args),
		repeatCount,
	)
}

func describeStagnationTarget(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		fp, ok := args["file_path"].(string)
		target := "read"
		if ok && fp != "" {
			target += " " + filepath.Base(fp)
		}
		offset := readIntArg(args, "offset")
		limit := readIntArg(args, "limit")
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
		} else if glob, ok := args["glob"].(string); ok && glob != "" {
			target += " matching " + glob
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
	case "list_dir", "tree":
		path, _ := args["path"].(string)
		if path == "" {
			return toolName
		}
		return fmt.Sprintf("%s %s", toolName, path)
	default:
		if fp := stagnationFingerprint(toolName, args); fp != "" {
			return fmt.Sprintf("%s (%s)", toolName, fp)
		}
		return toolName
	}
}

// stagnationFingerprint returns a short string that distinguishes different
// invocations of the same tool. For example, writing 5 different files should
// produce 5 different fingerprints, while writing the same file 5 times
// produces the same fingerprint (true stagnation).
func stagnationFingerprint(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		fp, ok := args["file_path"].(string)
		if !ok || fp == "" {
			return ""
		}
		return fmt.Sprintf("%s@%d+%d",
			filepath.Base(fp),
			readIntArg(args, "offset"),
			readIntArg(args, "limit"))
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
		if ls, le := readIntArg(args, "line_start"), readIntArg(args, "line_end"); ls > 0 || le > 0 {
			return fmt.Sprintf("%s@L%d-%d", base, ls, le)
		}
		if ia, ok := GetInt(args, "insert_after_line"); ok {
			return fmt.Sprintf("%s@ins%d", base, ia)
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

// readIntArg extracts an integer from args, handling float64 JSON deserialization.
func readIntArg(args map[string]any, key string) int {
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

// synthesisNudgeThreshold is the tool-call count at which the nudge fires.
// Three tools is the sweet spot: enough signal to consolidate
// (Read + grep + another read, or two greps + a read), not so many
// that the drift is already irreversible.
const synthesisNudgeThreshold = 3

// shouldInjectSynthesisNudge gates the per-turn synthesis reminder.
// Only families where empirical drift-after-3-tool-calls was observed —
// kimi and deepseek. GLM-5/Opus-class models self-consolidate and a
// reminder regresses their behaviour.
func shouldInjectSynthesisNudge(model string, totalToolsThisTurn int) bool {
	if totalToolsThisTurn < synthesisNudgeThreshold {
		return false
	}
	return isNudgeEligibleFamily(model)
}

// isNudgeEligibleFamily reports whether the given model belongs to a
// family that benefits from explicit runtime nudges (synthesis checkpoint,
// intent announcement, error-recovery hints).
func isNudgeEligibleFamily(model string) bool {
	family := client.GetModelProfile(model).Family
	return family == "kimi" || family == "deepseek"
}

func buildSynthesisNudgeMessage(toolsCount int) string {
	return fmt.Sprintf(
		"Consolidation checkpoint: you've issued %d tool calls this turn without writing a final answer. Before any more tools, pause and write 2-3 concise lines: (1) Established — concrete facts you've verified, (2) Unknown — open questions the tools couldn't answer, (3) Next — the single next step. If Established already covers the user's request, STOP and write the final answer instead.",
		toolsCount,
	)
}

const todoNudgeMinToolCalls = 2

const todoNudgeMessage = "Todo reminder: this has become multi-step coding work. Before continuing, call todo with the remaining plan and keep exactly one item in_progress, so the user can see progress like Claude Code."

func shouldInjectTodoNudge(model string, toolsUsed []string) bool {
	if len(toolsUsed) < todoNudgeMinToolCalls {
		return false
	}
	if !isNudgeEligibleFamily(model) {
		return false
	}

	hasMutation := false
	for _, toolName := range toolsUsed {
		switch toolName {
		case "todo":
			return false
		case "write", "edit", "move", "copy", "delete", "mkdir", "batch", "refactor":
			hasMutation = true
		}
	}
	return hasMutation
}

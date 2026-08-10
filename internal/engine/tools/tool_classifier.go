package tools

import (
	"strings"
	"time"

	"google.golang.org/genai"
)

// parallelSerializationSuccessThreshold is the success rate under which a
// tool is considered unreliable enough to serialize the whole group it's in.
const parallelSerializationSuccessThreshold = 0.5

// shouldSerializeGroup reports whether a parallel group should be downgraded
// to sequential execution based on observed per-tool reliability.
// statsLookup is called once per unique tool name in the group; tools with
// not-enough-samples (ok=false) don't count against reliability.
func shouldSerializeGroup(calls []*genai.FunctionCall, statsLookup func(string) (time.Duration, float64, bool)) bool {
	if statsLookup == nil || len(calls) <= 1 {
		return false
	}
	seen := make(map[string]bool, len(calls))
	for _, c := range calls {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		if _, rate, ok := statsLookup(c.Name); ok && rate < parallelSerializationSuccessThreshold {
			return true
		}
	}
	return false
}

// toolDependencyClassifier determines which tools can run in parallel.
type toolDependencyClassifier struct {
	writeTools map[string]bool
}

// toolGroup represents a group of tool calls that can be executed together.
type toolGroup struct {
	Calls    []*genai.FunctionCall
	Parallel bool
}

var defaultClassifier = &toolDependencyClassifier{
	writeTools: map[string]bool{
		"write":            true,
		"edit":             true,
		"bash":             true,
		"delete":           true,
		"move":             true,
		"copy":             true,
		"mkdir":            true,
		"git_commit":       true,
		"git_add":          true,
		"ssh":              true,
		"run_tests":        true,
		"batch":            true,
		"refactor":         true,
		"atomicwrite":      true,
		"document_create":  true,
		"plugin_agent":     true,
		"scheduled_task":   true,
		"preview_browser":  true,
		"external_browser": true,
		"session_agent":    true,
	},
}

// IsWriteTool reports whether a tool modifies state and must run SEQUENTIALLY
// (never in parallel with reads or other writes). This is the single source of
// truth for write-vs-read classification across the executor and any external
// callers — prevents drift where sub-systems each maintain their own list.
func IsWriteTool(name string) bool {
	return defaultClassifier.writeTools[name]
}

// RequiresUserApproval classifies operations that may mutate user files,
// repositories, processes, or external systems. It is intentionally separate
// from IsWriteTool: dependency serialization includes run_tests, while the
// permission gate should allow tests and read-only git subcommands.
func RequiresUserApproval(name string, args map[string]any) bool {
	if strings.HasPrefix(name, "mcp_") {
		return true // remote tools have unknown effects
	}
	switch name {
	case "computer_screenshot", "computer_action":
		return true // screen contents are sensitive even though capture is read-only
	case "preview_browser":
		return false // constrained to the active loopback preview origin
	case "external_browser":
		action, _ := args["action"].(string)
		return !strings.EqualFold(strings.TrimSpace(action), "list")
	case "session_agent":
		// Listing and reading are bounded and attributed. Everything else
		// crosses into another chat: "send" starts a full tool-enabled turn
		// there under THAT session's permission mode, "archive" changes the
		// user's visible catalog, "rename" edits it. The weaker one-shot
		// ask_agent is already gated below, so these must be too. "suggest"
		// only offers a chip the user must click, so it stays ungated.
		action, _ := args["action"].(string)
		switch strings.ToLower(strings.TrimSpace(action)) {
		case "send", "rename", "archive":
			return true
		}
		return false
	case "write", "edit", "document_create", "delete", "move", "copy", "mkdir",
		"git_add", "git_commit", "bash", "ssh", "refactor", "atomicwrite",
		"kill_shell", "task_stop", "ask_agent":
		return true
	case "plugin_agent":
		return true
	case "scheduled_task":
		action, _ := args["action"].(string)
		return strings.ToLower(strings.TrimSpace(action)) != "list"
	case "batch":
		dryRun, _ := args["dry_run"].(bool)
		return !dryRun
	case "git_branch":
		action, _ := args["action"].(string)
		return action != "list" && action != "current"
	case "git_pr":
		action, _ := args["action"].(string)
		return action != "list" && action != "view" && action != "checks"
	case "task":
		kind, _ := args["subagent_type"].(string)
		return kind != "explore" && kind != "plan" && kind != "claude-code-guide"
	case "coordinate":
		tasks, ok := args["tasks"].([]any)
		if !ok || len(tasks) == 0 {
			return true
		}
		for _, raw := range tasks {
			task, ok := raw.(map[string]any)
			if !ok {
				return true
			}
			kind, _ := task["agent_type"].(string)
			if kind != "explore" && kind != "plan" {
				return true
			}
		}
		return false
	case "read", "glob", "grep", "list_dir", "tree", "diff",
		"git_status", "git_diff", "git_log", "git_blame",
		"web_fetch", "web_search", "run_tests", "check_impact", "verify_code", "review_changes", "submit_code_review",
		"go_to_definition", "find_references", "env", "task_output",
		"ask_user", "todo", "tools_list", "request_tool",
		"plugin_resource",
		"search_session_transcripts",
		"memory", "memorize", "pin_context", "history_search", "shared_memory", "update_scratchpad",
		"enter_plan_mode", "update_plan_progress", "get_plan_status", "exit_plan_mode":
		return false
	default:
		return true // fail closed for newly-added or dynamically-requested tools
	}
}

// ExtractFilePaths returns the file paths referenced by a tool call based on
// well-known argument keys. Used by callers that need to run post-write
// validation (e.g. semantic validators) without touching the genai type.
func ExtractFilePaths(args map[string]any) []string {
	var paths []string
	for _, key := range []string{"file_path", "path", "source", "destination", "new_path"} {
		if p, ok := args[key].(string); ok && p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// classifyDependencies groups tool calls by read/write dependency.
// Consecutive read-only tools get Parallel=true; write tools get their own group.
func (c *toolDependencyClassifier) classifyDependencies(calls []*genai.FunctionCall) []toolGroup {
	if len(calls) <= 1 {
		return []toolGroup{{Calls: calls, Parallel: false}}
	}

	var groups []toolGroup
	var readGroup []*genai.FunctionCall

	for _, call := range calls {
		if c.writeTools[call.Name] {
			if len(readGroup) > 0 {
				groups = append(groups, toolGroup{Calls: readGroup, Parallel: true})
				readGroup = nil
			}
			groups = append(groups, toolGroup{Calls: []*genai.FunctionCall{call}, Parallel: false})
		} else {
			readGroup = append(readGroup, call)
		}
	}

	if len(readGroup) > 0 {
		groups = append(groups, toolGroup{Calls: readGroup, Parallel: len(readGroup) > 1})
	}

	return groups
}

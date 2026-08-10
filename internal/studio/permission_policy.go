package studio

import (
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

type permissionDecision uint8

const (
	permissionAllow permissionDecision = iota
	permissionAskTurn
	permissionAskAction
	permissionDeny
)

// normalizePermissionMode keeps old projects compatible while exposing the
// current Claude Desktop-style vocabulary. Empty and the legacy "auto" both
// mean reviewed Auto; "ask" migrates to Manual. Both acceptEdits (the Claude
// settings spelling) and accept_edits normalize to the persisted local form.
func normalizePermissionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "manual":
		return "manual"
	case "acceptedits", "accept_edits", "accept-edits":
		return "accept_edits"
	case "skip":
		return "skip"
	case "plan":
		return "plan"
	default:
		return "auto"
	}
}

// permissionForTool is the runtime policy, independent of what the model says
// it intends to do. Manual asks once for ordinary mutations. Accept edits
// automatically permits only bounded file/document changes and common local
// filesystem operations. Auto additionally permits reviewed local Git
// operations. Skip removes the ordinary gate.
//
// Destructive, external, and screen operations remain exact-action gates in
// every mode. This prevents a prior approval for a harmless edit from being
// reused for a later delete or connector mutation.
func permissionForTool(mode, toolName string, args map[string]any) permissionDecision {
	if normalizePermissionMode(mode) == "plan" {
		if tools.IsReadOnlyForPlanMode(toolName) {
			return permissionAllow
		}
		return permissionDeny
	}
	if hardGatedTool(toolName, args) {
		return permissionAskAction
	}
	if !tools.RequiresUserApproval(toolName, args) {
		return permissionAllow
	}
	switch normalizePermissionMode(mode) {
	case "manual":
		return permissionAskTurn
	case "accept_edits":
		if acceptEditsApprovedTool(toolName, args) {
			return permissionAllow
		}
		return permissionAskTurn
	case "skip":
		return permissionAllow
	default: // reviewed Auto
		if autoApprovedTool(toolName, args) {
			return permissionAllow
		}
		return permissionAskTurn
	}
}

// acceptEditsApprovedTool is the narrow intermediate policy from Claude
// Desktop: source/document edits and common filesystem organization proceed,
// while shell, Git state, processes, and external work still
// ask. Hard-gated variants are classified before this allowlist.
func acceptEditsApprovedTool(name string, _ map[string]any) bool {
	switch name {
	case "write", "edit", "document_create", "move", "copy", "mkdir", "atomicwrite", "refactor":
		return true
	default:
		return false
	}
}

func hardGatedTool(name string, args map[string]any) bool {
	if strings.HasPrefix(name, "computer_") || strings.HasPrefix(name, "mcp_") {
		return true
	}
	if networkAccess, _ := args["network_access"].(bool); networkAccess {
		switch name {
		case "bash", "run_tests", "verify_code":
			return true
		}
	}
	switch name {
	case "delete", "ssh":
		return true
	case "external_browser":
		return !strings.EqualFold(strings.TrimSpace(stringArg(args, "action")), "list")
	case "scheduled_task":
		action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
		return action != "list"
	case "session_agent":
		// "send" runs another agent loop in a sibling chat under that chat's
		// permission mode, "rename"/"archive" mutate the visible catalog. All
		// three are cross-session, so they get exact-action review and stay
		// out of the rememberable set. "list"/"read"/"suggest" are inert.
		switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
		case "send", "rename", "archive":
			return true
		}
		return false
	case "document_create":
		replace, _ := args["replace"].(bool)
		return replace
	case "bash":
		return destructiveShellCommand(stringArg(args, "command"))
	case "batch":
		dryRun, _ := args["dry_run"].(bool)
		return !dryRun
	case "git_pr":
		action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
		return action != "" && action != "list" && action != "view" && action != "checks"
	case "git_branch":
		action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
		return action == "delete"
	}
	return false
}

func destructiveShellCommand(command string) bool {
	command = " " + strings.ToLower(strings.TrimSpace(command)) + " "
	for _, marker := range []string{
		" rm ", " rm\t", " unlink ", " rmdir ", " shred ", " truncate ",
		" git clean ", " git reset --hard", " drop database", " drop table",
		" remove-item ", " del ", " erase ",
	} {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

// autoApprovedTool is intentionally an allowlist. File tools are already
// project-root anchored and edit enforces read-before-write. Unknown tools,
// arbitrary shell, process control, and cross-project agents require review.
func autoApprovedTool(name string, args map[string]any) bool {
	switch name {
	case "write", "edit", "document_create", "move", "copy", "mkdir", "atomicwrite",
		"git_add", "git_commit", "refactor":
		return true
	case "git_branch":
		action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
		if action == "switch" {
			// A forced switch runs `git checkout -f`, which discards every
			// uncommitted change in the session worktree with no reflog to
			// recover from. That is destructive, so it goes to exact review
			// like the other destructive Git operations.
			if force, _ := args["force"].(bool); force {
				return false
			}
			return true
		}
		return action == "create"
	case "batch":
		dryRun, _ := args["dry_run"].(bool)
		return dryRun
	default:
		return false
	}
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func permissionHookHandlers(mode string, handlers []pluginHookHandler) []pluginHookHandler {
	if normalizePermissionMode(mode) == "plan" {
		return nil
	}
	return handlers
}

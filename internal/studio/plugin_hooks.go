package studio

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPluginHookConfigBytes  = 512 << 10
	maxPluginHookCommandBytes = 32 << 10
	maxPluginHookOutputBytes  = 256 << 10
	maxPluginHookErrorBytes   = 64 << 10
	maxPluginHookHandlers     = 256
	defaultPluginHookTimeout  = 60 * time.Second
	maxPluginHookTimeout      = 10 * time.Minute
)

var supportedPluginHookEvents = map[string]bool{
	"PreToolUse":         true,
	"PostToolUse":        true,
	"PostToolUseFailure": true,
}

type PluginHookHandlerReview struct {
	Event     string   `json:"event"`
	Matcher   string   `json:"matcher,omitempty"`
	Type      string   `json:"type"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Timeout   int      `json:"timeout"`
	Supported bool     `json:"supported"`
	Warnings  []string `json:"warnings,omitempty"`
}

type PluginHookReview struct {
	Plugin   string                    `json:"plugin"`
	Digest   string                    `json:"digest"`
	Path     string                    `json:"path"`
	Armed    bool                      `json:"armed"`
	Handlers []PluginHookHandlerReview `json:"handlers"`
	Warnings []string                  `json:"warnings,omitempty"`
}

type pluginHookHandler struct {
	Plugin  string
	Root    string
	Event   string
	Matcher string
	Type    string
	Command string
	Args    []string
	Timeout time.Duration
}

type pluginHookGroup struct {
	Matcher string                  `json:"matcher"`
	Hooks   []pluginHookHandlerJSON `json:"hooks"`
}

type pluginHookHandlerJSON struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
	If      string   `json:"if"`
}

type pluginHookDocument struct {
	Description string                       `json:"description"`
	Hooks       map[string][]pluginHookGroup `json:"hooks"`
}

type pluginHookInput struct {
	SessionID      string         `json:"session_id"`
	TranscriptPath string         `json:"transcript_path"`
	CWD            string         `json:"cwd"`
	PermissionMode string         `json:"permission_mode"`
	HookEventName  string         `json:"hook_event_name"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	ToolUseID      string         `json:"tool_use_id,omitempty"`
	ToolResponse   any            `json:"tool_response,omitempty"`
	Error          string         `json:"error,omitempty"`
}

type pluginHookJSONOutput struct {
	Continue           *bool          `json:"continue"`
	Decision           string         `json:"decision"`
	Reason             string         `json:"reason"`
	UpdatedInput       map[string]any `json:"updatedInput"`
	AdditionalContext  string         `json:"additionalContext"`
	HookSpecificOutput struct {
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
		AdditionalContext        string         `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

type pluginHookOutcome struct {
	UpdatedInput      map[string]any
	DenyReason        string
	ForceAsk          bool
	AdditionalContext []string
}

func (s *Studio) InspectPluginHooks(name string) (*PluginHookReview, error) {
	installed, root, sourcePath, data, err := loadPluginHookSource(name, false)
	if err != nil {
		return nil, err
	}
	handlers, warnings, err := parsePluginHookDocument(name, root, data)
	if err != nil {
		return nil, err
	}
	digest := pluginHookReviewDigest(installed.Digest, sourcePath, data)
	armed := installed.HooksEnabled && installed.HooksDigest == digest
	if installed.HooksEnabled && !armed {
		warnings = appendUniqueString(warnings, "Hook configuration changed after it was armed. Runtime execution is blocked until you review and arm it again.")
	}
	return &PluginHookReview{
		Plugin: name, Digest: digest,
		Path: sourcePath, Armed: armed,
		Handlers: reviewPluginHookHandlers(handlers), Warnings: warnings,
	}, nil
}

func (s *Studio) SetPluginHooksEnabled(name, reviewedDigest string, enabled bool) error {
	if !pluginNameRE.MatchString(name) {
		return fmt.Errorf("invalid plugin name")
	}

	pluginsMu.Lock()
	plugins, err := loadInstalledPluginsRaw()
	if err != nil {
		pluginsMu.Unlock()
		return err
	}
	found := -1
	for i := range plugins {
		if plugins[i].Name != name {
			continue
		}
		if enabled && !plugins[i].Enabled {
			pluginsMu.Unlock()
			return fmt.Errorf("enable plugin %q before arming its hooks", name)
		}
		found = i
		break
	}
	if found < 0 {
		pluginsMu.Unlock()
		return fmt.Errorf("plugin not found: %s", name)
	}
	if enabled {
		root, sourcePath, data, sourceErr := readPluginHookSource(plugins[found])
		if sourceErr != nil {
			pluginsMu.Unlock()
			return sourceErr
		}
		expected := pluginHookReviewDigest(plugins[found].Digest, sourcePath, data)
		if len(reviewedDigest) != sha256.Size*2 ||
			subtle.ConstantTimeCompare([]byte(strings.ToLower(reviewedDigest)), []byte(expected)) != 1 {
			pluginsMu.Unlock()
			return fmt.Errorf("plugin hooks changed after review; inspect them again")
		}
		handlers, _, parseErr := parsePluginHookDocument(name, root, data)
		if parseErr != nil {
			pluginsMu.Unlock()
			return parseErr
		}
		supported := 0
		for _, handler := range handlers {
			if pluginHookHandlerSupported(handler) {
				supported++
			}
		}
		if supported == 0 {
			pluginsMu.Unlock()
			return fmt.Errorf("plugin declares no supported command hooks")
		}
		plugins[found].HooksDigest = expected
	} else {
		plugins[found].HooksDigest = ""
	}
	plugins[found].HooksEnabled = enabled
	err = saveInstalledPluginsRaw(plugins)
	pluginsMu.Unlock()
	if err == nil {
		s.resetAllProjectClientsForPlugins()
	}
	return err
}

func loadPluginHookSource(name string, requireEnabled bool) (InstalledPlugin, string, string, []byte, error) {
	if !pluginNameRE.MatchString(name) {
		return InstalledPlugin{}, "", "", nil, fmt.Errorf("invalid plugin name")
	}
	pluginsMu.Lock()
	plugins, err := loadInstalledPluginsRaw()
	pluginsMu.Unlock()
	if err != nil {
		return InstalledPlugin{}, "", "", nil, err
	}
	var installed *InstalledPlugin
	for i := range plugins {
		if plugins[i].Name == name {
			copy := plugins[i]
			installed = &copy
			break
		}
	}
	if installed == nil {
		return InstalledPlugin{}, "", "", nil, fmt.Errorf("plugin not found: %s", name)
	}
	if !installed.HasHooks {
		return InstalledPlugin{}, "", "", nil, fmt.Errorf("plugin %q declares no hooks", name)
	}
	if requireEnabled && !installed.Enabled {
		return InstalledPlugin{}, "", "", nil, fmt.Errorf("plugin %q is disabled", name)
	}
	root, sourcePath, data, err := readPluginHookSource(*installed)
	if err != nil {
		return InstalledPlugin{}, "", "", nil, err
	}
	return *installed, root, sourcePath, data, nil
}

func readPluginHookSource(installed InstalledPlugin) (string, string, []byte, error) {
	root, err := safeInstalledPluginRoot(installed.Name)
	if err != nil {
		return "", "", nil, err
	}
	for _, candidate := range []string{"hooks/hooks.json", "hooks.json"} {
		data, readErr := readInstalledPluginFile(root, candidate, maxPluginHookConfigBytes)
		if readErr == nil {
			return root, candidate, data, nil
		}
		if !os.IsNotExist(readErr) {
			return "", "", nil, readErr
		}
	}
	return "", "", nil, fmt.Errorf("plugin hook configuration not found")
}

func pluginHookReviewDigest(pluginDigest, sourcePath string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(pluginDigest))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(sourcePath))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func parsePluginHookDocument(plugin, root string, data []byte) ([]pluginHookHandler, []string, error) {
	if len(data) == 0 || len(data) > maxPluginHookConfigBytes || !utf8.Valid(data) {
		return nil, nil, fmt.Errorf("plugin hooks must be UTF-8 JSON up to %d KiB", maxPluginHookConfigBytes>>10)
	}
	var doc pluginHookDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse plugin hooks: %w", err)
	}
	if doc.Hooks == nil {
		// Older portable bundles sometimes omit the outer "hooks" object.
		if err := json.Unmarshal(data, &doc.Hooks); err != nil || doc.Hooks == nil {
			return nil, nil, fmt.Errorf("plugin hooks must contain an object named hooks")
		}
	}
	var handlers []pluginHookHandler
	var warnings []string
	events := make([]string, 0, len(doc.Hooks))
	for event := range doc.Hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		for _, group := range doc.Hooks[event] {
			if len(group.Matcher) > 1024 {
				return nil, nil, fmt.Errorf("hook matcher is too large")
			}
			for _, raw := range group.Hooks {
				if len(handlers) >= maxPluginHookHandlers {
					return nil, nil, fmt.Errorf("plugin declares more than %d hook handlers", maxPluginHookHandlers)
				}
				timeout := defaultPluginHookTimeout
				if raw.Timeout > 0 {
					timeout = time.Duration(raw.Timeout) * time.Second
				}
				handler := pluginHookHandler{
					Plugin: plugin, Root: root, Event: event, Matcher: strings.TrimSpace(group.Matcher),
					Type: strings.TrimSpace(raw.Type), Command: raw.Command,
					Args: append([]string(nil), raw.Args...), Timeout: timeout,
				}
				if len(handler.Command) > maxPluginHookCommandBytes || !utf8.ValidString(handler.Command) {
					return nil, nil, fmt.Errorf("hook command is too large or invalid")
				}
				if raw.If != "" {
					handler.Type = "unsupported:conditional-" + handler.Type
				}
				if timeout > maxPluginHookTimeout {
					handler.Type = "unsupported:timeout-" + handler.Type
				}
				if !supportedPluginHookEvents[event] {
					warnings = appendUniqueString(warnings, fmt.Sprintf("%s hooks are retained but not run by this Studio version.", event))
				}
				handlers = append(handlers, handler)
			}
		}
	}
	return handlers, warnings, nil
}

func reviewPluginHookHandlers(handlers []pluginHookHandler) []PluginHookHandlerReview {
	out := make([]PluginHookHandlerReview, 0, len(handlers))
	for _, handler := range handlers {
		review := PluginHookHandlerReview{
			Event: handler.Event, Matcher: handler.Matcher, Type: handler.Type,
			Command: handler.Command, Args: append([]string(nil), handler.Args...),
			Timeout: int(handler.Timeout / time.Second), Supported: pluginHookHandlerSupported(handler),
		}
		if !supportedPluginHookEvents[handler.Event] {
			review.Warnings = append(review.Warnings, "Lifecycle event is not supported.")
		}
		if handler.Type != "command" {
			review.Warnings = append(review.Warnings, "Only unconditional command handlers are supported.")
		}
		if handler.Matcher != "" && handler.Matcher != "*" {
			if _, err := regexp.Compile(handler.Matcher); err != nil {
				review.Warnings = append(review.Warnings, "Matcher is not a valid RE2 expression.")
				review.Supported = false
			}
		}
		out = append(out, review)
	}
	return out
}

func pluginHookHandlerSupported(handler pluginHookHandler) bool {
	if !supportedPluginHookEvents[handler.Event] || handler.Type != "command" ||
		strings.TrimSpace(handler.Command) == "" || handler.Timeout <= 0 || handler.Timeout > maxPluginHookTimeout {
		return false
	}
	if handler.Matcher == "" || handler.Matcher == "*" {
		return true
	}
	_, err := regexp.Compile(handler.Matcher)
	return err == nil
}

func loadEnabledPluginHooks() []pluginHookHandler {
	pluginsMu.Lock()
	plugins, err := loadInstalledPluginsRaw()
	pluginsMu.Unlock()
	if err != nil {
		return nil
	}
	var out []pluginHookHandler
	for _, plugin := range plugins {
		if !plugin.Enabled || !plugin.HooksEnabled || !plugin.HasHooks {
			continue
		}
		_, root, sourcePath, data, err := loadPluginHookSource(plugin.Name, true)
		if err != nil {
			continue
		}
		if plugin.HooksDigest != pluginHookReviewDigest(plugin.Digest, sourcePath, data) {
			continue
		}
		handlers, _, err := parsePluginHookDocument(plugin.Name, root, data)
		if err != nil {
			continue
		}
		for _, handler := range handlers {
			if pluginHookHandlerSupported(handler) {
				out = append(out, handler)
			}
		}
	}
	return out
}

func runPluginToolHooks(ctx context.Context, handlers []pluginHookHandler, input pluginHookInput) pluginHookOutcome {
	out := pluginHookOutcome{UpdatedInput: cloneHookInput(input.ToolInput)}
	input.ToolInput = out.UpdatedInput
	for _, handler := range handlers {
		if handler.Event != input.HookEventName || !pluginHookMatches(handler.Matcher, input.ToolName) {
			continue
		}
		input.ToolInput = out.UpdatedInput
		result, err := executePluginCommandHook(ctx, handler, input)
		if err != nil {
			out.AdditionalContext = append(out.AdditionalContext, fmt.Sprintf("Plugin hook %s/%s failed non-blockingly: %v", handler.Plugin, handler.Event, err))
			continue
		}
		if result.UpdatedInput != nil && input.HookEventName == "PreToolUse" {
			out.UpdatedInput = cloneHookInput(result.UpdatedInput)
		}
		if len(result.AdditionalContext) > 0 {
			out.AdditionalContext = append(out.AdditionalContext, result.AdditionalContext...)
		}
		if result.DenyReason != "" {
			out.DenyReason = result.DenyReason
			break
		}
		if result.ForceAsk {
			out.ForceAsk = true
		}
	}
	return out
}

func executePluginCommandHook(parent context.Context, handler pluginHookHandler, input pluginHookInput) (pluginHookOutcome, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return pluginHookOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(parent, handler.Timeout)
	defer cancel()
	cmd := pluginHookCommand(ctx, handler.Command, handler.Args)
	cmd.Dir = input.CWD
	cmd.Env = append(os.Environ(),
		"CLAUDE_PLUGIN_ROOT="+handler.Root,
		"CLAUDE_PROJECT_DIR="+input.CWD,
	)
	// A repository hook in a WSL project is Linux shell script written for the
	// distro; running it through cmd.exe against a UNC path would fail or, worse,
	// do something unintended. The two CLAUDE_* variables have to be translated
	// too, because inside the distro a Windows path names nothing.
	if target := wsl.DetectFor(input.CWD); target.IsWSL() {
		inject := security.WorkspaceEnvironmentSnapshot()
		if root, ok := wsl.LinuxPathFor(target, handler.Root); ok {
			inject["CLAUDE_PLUGIN_ROOT"] = root
		}
		if cwd, ok := wsl.LinuxPathFor(target, input.CWD); ok {
			inject["CLAUDE_PROJECT_DIR"] = cwd
		}
		script := handler.Command
		if len(handler.Args) > 0 {
			// The host form passes args after `--`; preserve that contract by
			// quoting them into the script's positional parameters.
			script = handler.Command + " " + wsl.JoinArgv(handler.Args)
		}
		wsl.ApplyShell(cmd, target, script, inject)
	}
	cmd.Stdin = strings.NewReader(string(payload))
	stdout := &cappedCommandOutput{limit: maxPluginHookOutputBytes}
	stderr := &cappedCommandOutput{limit: maxPluginHookErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return pluginHookOutcome{}, fmt.Errorf("timed out after %s", handler.Timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = fmt.Sprintf("Plugin hook %s blocked this tool call", handler.Plugin)
			}
			return pluginHookOutcome{DenyReason: truncateUTF8(reason, 4000)}, nil
		}
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return pluginHookOutcome{}, fmt.Errorf("%w: %s", err, truncateUTF8(message, 1000))
		}
		return pluginHookOutcome{}, err
	}
	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return pluginHookOutcome{}, nil
	}
	var parsed pluginHookJSONOutput
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return pluginHookOutcome{}, fmt.Errorf("invalid JSON output: %w", err)
	}
	decision := strings.ToLower(strings.TrimSpace(parsed.HookSpecificOutput.PermissionDecision))
	if decision == "" {
		decision = strings.ToLower(strings.TrimSpace(parsed.Decision))
	}
	reason := strings.TrimSpace(parsed.HookSpecificOutput.PermissionDecisionReason)
	if reason == "" {
		reason = strings.TrimSpace(parsed.Reason)
	}
	updated := parsed.HookSpecificOutput.UpdatedInput
	if updated == nil {
		updated = parsed.UpdatedInput
	}
	additional := strings.TrimSpace(parsed.HookSpecificOutput.AdditionalContext)
	if additional == "" {
		additional = strings.TrimSpace(parsed.AdditionalContext)
	}
	out := pluginHookOutcome{UpdatedInput: updated, AdditionalContext: nil}
	if additional != "" {
		out.AdditionalContext = []string{truncateUTF8(additional, 16000)}
	}
	if decision == "deny" || decision == "block" || (parsed.Continue != nil && !*parsed.Continue) {
		if reason == "" {
			reason = fmt.Sprintf("Plugin hook %s blocked this tool call", handler.Plugin)
		}
		out.DenyReason = truncateUTF8(reason, 4000)
	}
	if decision == "ask" {
		out.ForceAsk = true
	}
	// "allow"/"approve" is intentionally advisory. Runtime permission policy
	// still evaluates the final input and may require user approval.
	return out, nil
}

func pluginHookMatches(matcher, toolName string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		return false
	}
	canonical := canonicalPluginHookToolName(toolName)
	return re.MatchString(canonical) || re.MatchString(toolName)
}

func canonicalPluginHookToolName(name string) string {
	if strings.HasPrefix(name, "mcp_") {
		return "MCP"
	}
	switch name {
	case "bash":
		return "Bash"
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "edit":
		return "Edit"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "plugin_agent", "delegate", "session_agent":
		return "Agent"
	case "web_fetch":
		return "WebFetch"
	case "web_search":
		return "WebSearch"
	case "ask_user":
		return "AskUserQuestion"
	default:
		return name
	}
}

func cloneHookInput(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendPluginHookContext(content string, values []string) string {
	var clean []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return content
	}
	block := "[Plugin hook context]\n" + strings.Join(clean, "\n")
	if strings.TrimSpace(content) == "" {
		return block
	}
	return content + "\n\n" + block
}

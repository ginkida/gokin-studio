package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	maxPluginResourceBytes = 512 << 10
	maxPluginResourceFiles = 512
)

// PluginResourceTool exposes text resources from enabled, reviewed plugins.
// It is deliberately read-only and rooted outside the user's project so
// plugins do not have to copy generated files into the workspace.
type PluginResourceTool struct {
	root    string
	allowed map[string]bool
}

func NewPluginResourceTool(root string, enabledPluginNames []string) *PluginResourceTool {
	allowed := make(map[string]bool, len(enabledPluginNames))
	for _, name := range enabledPluginNames {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	return &PluginResourceTool{root: root, allowed: allowed}
}

func (t *PluginResourceTool) Name() string { return "plugin_resource" }

func (t *PluginResourceTool) Description() string {
	return "Reads reviewed text resources from enabled Claude-compatible plugins. Use action=list with a plugin name to inspect available paths, then action=read with a path such as plugin-name/skills/report/SKILL.md. It never executes plugin scripts."
}

func (t *PluginResourceTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name: t.Name(), Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {Type: genai.TypeString, Enum: []string{"list", "read"}, Description: "List plugin resources or read one text file"},
				"plugin": {Type: genai.TypeString, Description: "Enabled plugin name (required for list)"},
				"path":   {Type: genai.TypeString, Description: "Plugin-relative resource path including the plugin name (required for read)"},
			},
			Required: []string{"action"},
		},
	}
}

func (t *PluginResourceTool) Validate(args map[string]any) error {
	action, _ := GetString(args, "action")
	switch action {
	case "list":
		if plugin, _ := GetString(args, "plugin"); strings.TrimSpace(plugin) == "" {
			return NewValidationError("plugin", "is required for list")
		}
	case "read":
		if path, _ := GetString(args, "path"); strings.TrimSpace(path) == "" {
			return NewValidationError("path", "is required for read")
		}
	default:
		return NewValidationError("action", "must be list or read")
	}
	return nil
}

func (t *PluginResourceTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return NewErrorResult("plugin_resource cancelled"), nil
	}
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	action, _ := GetString(args, "action")
	if action == "list" {
		plugin, _ := GetString(args, "plugin")
		files, err := t.list(plugin)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(strings.Join(files, "\n")), nil
	}
	path, _ := GetString(args, "path")
	data, err := t.read(path)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	return NewSuccessResult(string(data)), nil
}

func (t *PluginResourceTool) list(plugin string) ([]string, error) {
	plugin = strings.TrimSpace(plugin)
	if !t.allowed[plugin] {
		return nil, fmt.Errorf("plugin %q is not enabled", plugin)
	}
	root, err := t.safePath(plugin, true)
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(out) >= maxPluginResourceFiles {
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > maxPluginResourceBytes {
			return nil
		}
		rel, err := filepath.Rel(t.root, path)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list plugin resources: %w", err)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"(no readable resources)"}, nil
	}
	return out, nil
}

func (t *PluginResourceTool) read(relative string) ([]byte, error) {
	path, err := t.safePath(relative, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin resource: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin resource must be a regular non-symlink file")
	}
	if info.Size() > maxPluginResourceBytes {
		return nil, fmt.Errorf("plugin resource exceeds %d KiB", maxPluginResourceBytes>>10)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("plugin resource changed during validation")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPluginResourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPluginResourceBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("plugin resource must be UTF-8 text up to %d KiB", maxPluginResourceBytes>>10)
	}
	return data, nil
}

func (t *PluginResourceTool) safePath(relative string, wantDir bool) (string, error) {
	if t.root == "" || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("plugin resource storage is unavailable")
	}
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid plugin resource path")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) == 0 || !t.allowed[parts[0]] {
		return "", fmt.Errorf("plugin %q is not enabled", parts[0])
	}
	current := t.root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("plugin resource paths may not contain symlinks")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("plugin resource parent is not a directory")
		}
	}
	if wantDir {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("plugin resource root is not a directory")
		}
	}
	return current, nil
}

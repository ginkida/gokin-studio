package studio

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxPluginMCPSourceBytes = 256 << 10
	maxPluginMCPServers     = 32
	maxPluginMCPWarnings    = 32
)

// PluginMCPServerSummary is safe to retain with plugin metadata. It deliberately
// excludes command arguments, environment values, and headers.
type PluginMCPServerSummary struct {
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	Importable bool     `json:"importable"`
	Warnings   []string `json:"warnings,omitempty"`
}

// PluginMCPServerReview contains the exact connector snapshot shown in
// Settings. ImportPluginMCPConnector re-reads the installed plugin and verifies
// the review digest before saving this configuration.
type PluginMCPServerReview struct {
	SourceName     string            `json:"sourceName"`
	SuggestedName  string            `json:"suggestedName"`
	Transport      string            `json:"transport"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	AuthType       string            `json:"authType,omitempty"`
	OAuthClientID  string            `json:"oauthClientID,omitempty"`
	Importable     bool              `json:"importable"`
	ExistingServer bool              `json:"existingServer,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
}

type PluginMCPReview struct {
	Plugin   string                  `json:"plugin"`
	Digest   string                  `json:"digest"`
	Servers  []PluginMCPServerReview `json:"servers"`
	Warnings []string                `json:"warnings,omitempty"`
}

type pluginMCPRawServer struct {
	Type          string                     `json:"type"`
	Command       string                     `json:"command"`
	Args          []string                   `json:"args"`
	Env           map[string]string          `json:"env"`
	URL           string                     `json:"url"`
	Headers       map[string]string          `json:"headers"`
	HeadersHelper json.RawMessage            `json:"headersHelper"`
	OAuth         map[string]json.RawMessage `json:"oauth"`
}

type pluginMCPSource struct {
	label string
	data  []byte
}

// InspectPluginMCPConnectors reads installed plugin metadata only after an
// explicit Settings action. Nothing is started, saved, or enabled.
func (s *Studio) InspectPluginMCPConnectors(name string) (*PluginMCPReview, error) {
	return inspectInstalledPluginMCP(name)
}

// ImportPluginMCPConnector performs a second read and digest comparison, then
// saves exactly one reviewed connector in a disabled state. Enabling and
// testing remain separate user actions in the MCP settings section.
func (s *Studio) ImportPluginMCPConnector(pluginName, sourceName, reviewedDigest string) (*MCPServerConfig, error) {
	if len(reviewedDigest) != sha256.Size*2 {
		return nil, fmt.Errorf("plugin connector review digest is invalid")
	}
	review, err := inspectInstalledPluginMCP(pluginName)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(reviewedDigest)), []byte(review.Digest)) != 1 {
		return nil, fmt.Errorf("plugin connector definition changed after review; inspect it again")
	}
	for _, server := range review.Servers {
		if server.SourceName != sourceName {
			continue
		}
		if !server.Importable {
			return nil, fmt.Errorf("plugin connector %q uses unsupported configuration", sourceName)
		}
		cfg := MCPServerConfig{
			Name:          server.SuggestedName,
			Transport:     server.Transport,
			Command:       server.Command,
			Args:          append([]string(nil), server.Args...),
			Env:           cloneStringMap(server.Env),
			URL:           server.URL,
			Headers:       cloneStringMap(server.Headers),
			AuthType:      server.AuthType,
			OAuthClientID: server.OAuthClientID,
			Enabled:       false,
		}
		if err := s.SaveMCPServer(cfg); err != nil {
			return nil, err
		}
		return &cfg, nil
	}
	return nil, fmt.Errorf("plugin connector not found: %s", sourceName)
}

func inspectInstalledPluginMCP(name string) (*PluginMCPReview, error) {
	if !pluginNameRE.MatchString(name) {
		return nil, fmt.Errorf("invalid plugin name")
	}

	pluginsMu.Lock()
	plugins, err := loadInstalledPluginsRaw()
	pluginsMu.Unlock()
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("plugin not found: %s", name)
	}
	if !installed.HasMCP {
		return nil, fmt.Errorf("plugin %q has no MCP connector definitions", name)
	}

	root, err := safeInstalledPluginRoot(name)
	if err != nil {
		return nil, err
	}
	manifestData, err := readInstalledPluginFile(root, ".claude-plugin/plugin.json", maxPluginManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("read installed plugin manifest: %w", err)
	}
	var manifest claudePluginManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse installed plugin manifest: %w", err)
	}
	if strings.TrimSpace(manifest.Name) != name {
		return nil, fmt.Errorf("installed plugin manifest identity changed")
	}

	sources := make([]pluginMCPSource, 0, 2)
	dedicatedPath := filepath.Join(root, ".mcp.json")
	if _, statErr := os.Lstat(dedicatedPath); statErr == nil {
		data, readErr := readInstalledPluginFile(root, ".mcp.json", maxPluginMCPSourceBytes)
		if readErr != nil {
			return nil, readErr
		}
		sources = append(sources, pluginMCPSource{label: ".mcp.json", data: data})
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if hasJSONValue(manifest.MCPServers) {
		sources = append(sources, pluginMCPSource{label: "plugin.json:mcpServers", data: manifest.MCPServers})
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("plugin MCP definitions are missing")
	}

	servers, warnings, err := parsePluginMCPSources(name, root, sources)
	if err != nil {
		return nil, err
	}
	digest := pluginMCPReviewDigest(installed.Digest, sources)
	existing, err := loadMCPServersRaw()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		for _, cfg := range existing {
			if strings.EqualFold(strings.TrimSpace(cfg.Name), servers[i].SuggestedName) {
				servers[i].ExistingServer = true
				servers[i].Warnings = appendBoundedWarning(
					servers[i].Warnings,
					"Importing will replace the existing connector with this namespaced name; the replacement will be disabled.",
				)
				break
			}
		}
	}
	return &PluginMCPReview{Plugin: name, Digest: digest, Servers: servers, Warnings: warnings}, nil
}

func safeInstalledPluginRoot(name string) (string, error) {
	base := pluginsDir()
	baseInfo, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if baseInfo.Mode()&os.ModeSymlink != 0 || !baseInfo.IsDir() {
		return "", fmt.Errorf("plugin storage root must be a non-symlink directory")
	}
	root := filepath.Join(base, name)
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("installed plugin root must be a non-symlink directory")
	}
	return root, nil
}

func readInstalledPluginFile(root, relative string, maxBytes int64) ([]byte, error) {
	clean, err := cleanPluginEntry(relative)
	if err != nil || clean != filepath.ToSlash(relative) {
		return nil, fmt.Errorf("invalid installed plugin path")
	}
	current := root
	parts := strings.Split(filepath.FromSlash(clean), string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("installed plugin paths may not contain symlinks")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("installed plugin parent is not a directory")
		}
	}
	data, err := readRegularFileLimited(current, maxBytes)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("plugin connector metadata must be UTF-8")
	}
	return data, nil
}

func parsePluginMCPSources(pluginName, pluginRoot string, sources []pluginMCPSource) ([]PluginMCPServerReview, []string, error) {
	if len(sources) == 0 {
		return []PluginMCPServerReview{}, nil, nil
	}
	var out []PluginMCPServerReview
	var globalWarnings []string
	seen := map[string]bool{}
	for _, source := range sources {
		entries, wrapped, err := decodePluginMCPMap(source.data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", source.label, err)
		}
		if wrapped {
			globalWarnings = appendBoundedWarning(globalWarnings,
				source.label+" uses the legacy mcpServers wrapper; it was normalized during review.")
		}
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, sourceName := range names {
			if len(out) >= maxPluginMCPServers {
				return nil, nil, fmt.Errorf("plugin may declare at most %d MCP servers", maxPluginMCPServers)
			}
			key := strings.ToLower(strings.TrimSpace(sourceName))
			if key == "" || seen[key] {
				return nil, nil, fmt.Errorf("duplicate or empty plugin MCP server name %q", sourceName)
			}
			seen[key] = true
			server, err := parsePluginMCPServer(pluginName, pluginRoot, sourceName, entries[sourceName])
			if err != nil {
				return nil, nil, fmt.Errorf("%s connector %q: %w", source.label, sourceName, err)
			}
			out = append(out, server)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].SourceName) < strings.ToLower(out[j].SourceName)
	})
	return out, globalWarnings, nil
}

func decodePluginMCPMap(data []byte) (map[string]json.RawMessage, bool, error) {
	if len(data) == 0 || len(data) > maxPluginMCPSourceBytes || !utf8.Valid(data) {
		return nil, false, fmt.Errorf("connector definition must be UTF-8 JSON up to %d KiB", maxPluginMCPSourceBytes>>10)
	}
	var top map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&top); err != nil {
		return nil, false, fmt.Errorf("parse connector JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, false, fmt.Errorf("connector definition must contain one JSON object")
		}
		return nil, false, fmt.Errorf("parse trailing connector JSON: %w", err)
	}
	if top == nil {
		return nil, false, fmt.Errorf("connector definition must be a JSON object")
	}
	if wrapped, ok := top["mcpServers"]; ok {
		if len(top) != 1 {
			return nil, false, fmt.Errorf("mcpServers wrapper cannot be mixed with sibling keys")
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(wrapped, &entries); err != nil || entries == nil {
			return nil, false, fmt.Errorf("mcpServers must be a JSON object")
		}
		return entries, true, nil
	}
	return top, false, nil
}

func parsePluginMCPServer(pluginName, pluginRoot, sourceName string, data json.RawMessage) (PluginMCPServerReview, error) {
	sourceName = strings.TrimSpace(sourceName)
	if !mcpBundleNameRE.MatchString(sourceName) || len(sourceName) > 100 {
		return PluginMCPServerReview{}, fmt.Errorf("server name must use letters, numbers, dots, underscores, or hyphens")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return PluginMCPServerReview{}, fmt.Errorf("configuration must be a JSON object")
	}
	var raw pluginMCPRawServer
	if err := json.Unmarshal(data, &raw); err != nil {
		return PluginMCPServerReview{}, fmt.Errorf("parse configuration: %w", err)
	}
	review := PluginMCPServerReview{
		SourceName:    sourceName,
		SuggestedName: pluginMCPSuggestedName(pluginName, sourceName),
		Importable:    true,
	}
	known := map[string]bool{
		"type": true, "command": true, "args": true, "env": true, "url": true,
		"headers": true, "headersHelper": true, "oauth": true,
	}
	for key := range fields {
		if !known[key] {
			review.Importable = false
			review.Warnings = appendBoundedWarning(review.Warnings,
				fmt.Sprintf("Unsupported connector field %q must be configured manually.", key))
		}
	}
	if hasJSONValue(raw.HeadersHelper) {
		review.Importable = false
		review.Warnings = appendBoundedWarning(review.Warnings,
			"headersHelper is executable code and is never imported or run automatically.")
	}

	kind := strings.ToLower(strings.TrimSpace(raw.Type))
	if kind == "" {
		switch {
		case strings.TrimSpace(raw.Command) != "":
			kind = mcpTransportStdio
		case strings.TrimSpace(raw.URL) != "":
			kind = mcpTransportHTTP
		}
	}
	switch kind {
	case mcpTransportStdio:
		review.Transport = mcpTransportStdio
		review.Command = replacePluginRoot(raw.Command, pluginRoot)
		review.Args = replacePluginRootSlice(raw.Args, pluginRoot)
		review.Env = replacePluginRootMap(raw.Env, pluginRoot)
	case mcpTransportHTTP, "streamable-http", "sse":
		review.Transport = mcpTransportHTTP
		review.URL = replacePluginRoot(raw.URL, pluginRoot)
		review.Headers = replacePluginRootMap(raw.Headers, pluginRoot)
		review.AuthType = mcpAuthHeaders
		if kind == "sse" {
			review.Warnings = appendBoundedWarning(review.Warnings,
				"Legacy SSE was mapped to the HTTP transport; test the endpoint before enabling it.")
		}
		if raw.OAuth != nil {
			review.AuthType = mcpAuthOAuth
			for key, value := range raw.OAuth {
				switch key {
				case "clientId":
					if err := json.Unmarshal(value, &review.OAuthClientID); err != nil {
						return PluginMCPServerReview{}, fmt.Errorf("oauth.clientId must be a string")
					}
					review.OAuthClientID = replacePluginRoot(review.OAuthClientID, pluginRoot)
				case "callbackPort":
					review.Warnings = appendBoundedWarning(review.Warnings,
						"oauth.callbackPort is ignored; Gokin uses an ephemeral loopback callback for PKCE.")
				default:
					review.Importable = false
					review.Warnings = appendBoundedWarning(review.Warnings,
						fmt.Sprintf("Unsupported OAuth field %q must be configured manually.", key))
				}
			}
		}
	default:
		review.Importable = false
		review.Transport = kind
		review.Warnings = appendBoundedWarning(review.Warnings,
			fmt.Sprintf("Unsupported transport %q.", kind))
		return review, nil
	}

	cfg := MCPServerConfig{
		Name: review.SuggestedName, Transport: review.Transport,
		Command: review.Command, Args: review.Args, Env: review.Env,
		URL: review.URL, Headers: review.Headers, AuthType: review.AuthType,
		OAuthClientID: review.OAuthClientID, Enabled: false,
	}
	if _, err := validateMCPConfig(cfg); err != nil {
		review.Importable = false
		review.Warnings = appendBoundedWarning(review.Warnings,
			"Configuration needs manual changes before import: "+err.Error())
	}
	if review.Transport == mcpTransportStdio && pluginRoot != "" &&
		pathInside(pluginRoot, review.Command) {
		review.Warnings = appendBoundedWarning(review.Warnings,
			"Bundled files stay non-executable; use an explicit interpreter command or review permissions manually.")
	}
	return review, nil
}

func pluginMCPSuggestedName(pluginName, sourceName string) string {
	base := strings.TrimSpace(pluginName + "-" + sourceName)
	if len([]rune(base)) <= 50 {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	suffix := "-" + hex.EncodeToString(sum[:4])
	runes := []rune(base)
	return string(runes[:50-len([]rune(suffix))]) + suffix
}

func replacePluginRoot(value, root string) string {
	if root == "" {
		return value
	}
	return strings.ReplaceAll(value, "${CLAUDE_PLUGIN_ROOT}", root)
}

func replacePluginRootSlice(values []string, root string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = replacePluginRoot(value, root)
	}
	return out
}

func replacePluginRootMap(values map[string]string, root string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = replacePluginRoot(value, root)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("{}"))
}

func pluginMCPReviewDigest(pluginDigest string, sources []pluginMCPSource) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(pluginDigest))
	for _, source := range sources {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(source.label))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(source.data)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func appendBoundedWarning(warnings []string, warning string) []string {
	warning = strings.TrimSpace(warning)
	if warning == "" || len(warnings) >= maxPluginMCPWarnings {
		return warnings
	}
	return append(warnings, truncateUTF8(warning, 2000))
}

func pathInside(root, candidate string) bool {
	if root == "" || candidate == "" || !filepath.IsAbs(candidate) {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

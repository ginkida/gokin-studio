package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginMCPReviewAndDigestBoundDisabledImport(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	archivePath := filepath.Join(t.TempDir(), "connectors.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"connectors/.claude-plugin/plugin.json": `{
			"name":"connectors","version":"1.0.0","description":"Connector test"
		}`,
		"connectors/server.js": "console.log('reviewed')",
		"connectors/.mcp.json": `{
			"local-data": {
				"type": "stdio",
				"command": "node",
				"args": ["${CLAUDE_PLUGIN_ROOT}/server.js"],
				"env": {"PLUGIN_TOKEN": "${PLUGIN_TOKEN}"}
			},
			"remote-data": {
				"type": "http",
				"url": "https://mcp.example.test/api",
				"headers": {"X-Workspace": "${WORKSPACE_ID}"},
				"oauth": {"clientId": "public-client", "callbackPort": 3118}
			}
		}`,
	})

	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.HasMCP || len(preview.MCPServers) != 2 {
		t.Fatalf("plugin MCP preview = %#v", preview)
	}
	if _, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	}

	review, err := s.InspectPluginMCPConnectors("connectors")
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Servers) != 2 || len(review.Digest) != 64 {
		t.Fatalf("connector review = %#v", review)
	}
	local := review.Servers[0]
	if local.SourceName != "local-data" || local.SuggestedName != "connectors-local-data" ||
		local.Transport != mcpTransportStdio || !local.Importable {
		t.Fatalf("local review = %#v", local)
	}
	wantScript := filepath.Join(pluginsDir(), "connectors", "server.js")
	if len(local.Args) != 1 || local.Args[0] != wantScript {
		t.Fatalf("plugin root arg = %#v, want %q", local.Args, wantScript)
	}
	if local.Env["PLUGIN_TOKEN"] != "${PLUGIN_TOKEN}" {
		t.Fatalf("secret placeholder was resolved during review: %#v", local.Env)
	}
	remote := review.Servers[1]
	if remote.AuthType != mcpAuthOAuth || remote.OAuthClientID != "public-client" ||
		remote.Headers["X-Workspace"] != "${WORKSPACE_ID}" || !remote.Importable {
		t.Fatalf("remote review = %#v", remote)
	}

	if _, err := s.ImportPluginMCPConnector("connectors", "local-data", strings.Repeat("0", 64)); err == nil {
		t.Fatal("connector digest mismatch was accepted")
	}
	imported, err := s.ImportPluginMCPConnector("connectors", "local-data", review.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Enabled || imported.Name != "connectors-local-data" {
		t.Fatalf("imported connector = %#v", imported)
	}
	saved, err := loadMCPServersRaw()
	if err != nil || len(saved) != 1 {
		t.Fatalf("saved connectors = %#v, %v", saved, err)
	}
	if saved[0].Enabled || saved[0].Env["PLUGIN_TOKEN"] != "${PLUGIN_TOKEN}" {
		t.Fatalf("connector was enabled or secret placeholder changed: %#v", saved[0])
	}
	persisted, err := os.ReadFile(mcpConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "secret-value") {
		t.Fatal("resolved secret leaked into MCP config")
	}

	changed := filepath.Join(pluginsDir(), "connectors", ".mcp.json")
	if err := os.WriteFile(changed, []byte(`{"changed":{"command":"node"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportPluginMCPConnector("connectors", "remote-data", review.Digest); err == nil ||
		!strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("changed connector definition was accepted: %v", err)
	}
}

func TestPluginMCPInlineDefinitionAndUnsupportedHeaderHelper(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	archivePath := filepath.Join(t.TempDir(), "inline.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"inline/.claude-plugin/plugin.json": `{
			"name":"inline",
			"mcpServers":{
				"safe":{"type":"http","url":"https://mcp.example.test","oauth":{"clientId":"client"}},
				"dynamic":{"type":"http","url":"https://mcp.example.test","headersHelper":"./headers.sh"}
			}
		}`,
	})
	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.HasMCP || len(preview.MCPServers) != 2 {
		t.Fatalf("inline preview = %#v", preview)
	}
	if _, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	}
	review, err := s.InspectPluginMCPConnectors("inline")
	if err != nil {
		t.Fatal(err)
	}
	var dynamic *PluginMCPServerReview
	for i := range review.Servers {
		if review.Servers[i].SourceName == "dynamic" {
			dynamic = &review.Servers[i]
		}
	}
	if dynamic == nil || dynamic.Importable || !warningsContain(dynamic.Warnings, "headersHelper") {
		t.Fatalf("dynamic header review = %#v", dynamic)
	}
	if _, err := s.ImportPluginMCPConnector("inline", "dynamic", review.Digest); err == nil {
		t.Fatal("headersHelper connector was imported")
	}
}

func TestPluginMCPRejectsAmbiguousWrapperAndDuplicateSources(t *testing.T) {
	if _, _, err := decodePluginMCPMap([]byte(`{"mcpServers":{},"other":{}}`)); err == nil {
		t.Fatal("mixed mcpServers wrapper was accepted")
	}
	if _, _, err := decodePluginMCPMap([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing connector JSON was accepted")
	}
	_, _, err := parsePluginMCPSources("plugin", "", []pluginMCPSource{
		{label: ".mcp.json", data: []byte(`{"same":{"command":"node"}}`)},
		{label: "inline", data: []byte(`{"same":{"command":"node"}}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate connector sources were accepted: %v", err)
	}
	if _, _, err := parsePluginMCPSources("plugin", "", []pluginMCPSource{{
		label: ".mcp.json", data: []byte("{\"bad\\nname\":{\"command\":\"node\"}}"),
	}}); err == nil {
		t.Fatal("control characters in connector name were accepted")
	}
}

func TestExpandMCPEnvironmentAndResolverKeepStoredPlaceholders(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{"TOKEN": "secret-value", "EMPTY": ""}
		value, ok := values[name]
		return value, ok
	}
	got, err := expandMCPEnvironment("Bearer ${TOKEN} / ${MISSING:-fallback} / ${EMPTY:-default}", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Bearer secret-value / fallback / default" {
		t.Fatalf("expanded = %q", got)
	}
	if _, err := expandMCPEnvironment("${MISSING}", lookup); err == nil ||
		!strings.Contains(err.Error(), "MISSING") {
		t.Fatalf("missing variable error = %v", err)
	}
	if _, err := expandMCPEnvironment("${BAD-NAME}", lookup); err == nil {
		t.Fatal("invalid environment placeholder was accepted")
	}

	t.Setenv("PLUGIN_TOKEN", "secret-value")
	original := MCPServerConfig{
		Name: "remote", Transport: mcpTransportHTTP,
		URL: "https://mcp.example.test",
		Headers: map[string]string{
			"Authorization": "Bearer ${PLUGIN_TOKEN}",
		},
	}
	resolved, err := resolveMCPConfigEnvironment(original)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Headers["Authorization"] != "Bearer secret-value" {
		t.Fatalf("resolved headers = %#v", resolved.Headers)
	}
	if original.Headers["Authorization"] != "Bearer ${PLUGIN_TOKEN}" {
		t.Fatalf("stored config map was mutated: %#v", original.Headers)
	}
}

func warningsContain(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}

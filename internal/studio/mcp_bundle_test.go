package studio

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type testMCPBundleEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func writeTestMCPBundle(t *testing.T, manifest map[string]any, entries ...testMCPBundleEntry) string {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "extension.mcpb")
	file, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]testMCPBundleEntry{{name: "manifest.json", data: manifestData, mode: 0o600}}, entries...)
	for _, entry := range all {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return bundlePath
}

func validTestMCPBundleManifest() map[string]any {
	return map[string]any{
		"manifest_version": "0.4",
		"name":             "local-demo",
		"display_name":     "Local Demo",
		"version":          "1.2.3",
		"description":      "A local test connector",
		"author":           map[string]any{"name": "Gokin Test"},
		"server": map[string]any{
			"type":        "node",
			"entry_point": "server/index.js",
			"mcp_config": map[string]any{
				"command": "node",
				"args":    []any{"${__dirname}/server/index.js", "${user_config.allowed_directories}"},
				"env": map[string]any{
					"API_KEY":   "${user_config.api_key}",
					"READ_ONLY": "${user_config.read_only}",
					"TIMEOUT":   "${user_config.timeout}",
				},
			},
		},
		"user_config": map[string]any{
			"allowed_directories": map[string]any{
				"type": "directory", "title": "Allowed directories", "required": true, "multiple": true,
			},
			"api_key": map[string]any{
				"type": "string", "title": "API key", "required": true, "sensitive": true,
			},
			"read_only": map[string]any{
				"type": "boolean", "title": "Read only", "default": true,
			},
			"timeout": map[string]any{
				"type": "number", "title": "Timeout", "default": 30, "min": 1, "max": 60,
			},
		},
		"tools": []any{
			map[string]any{"name": "demo_read"},
		},
	}
}

func TestMCPBundlePreviewAndInstall(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	allowed := t.TempDir()
	bundlePath := writeTestMCPBundle(t, validTestMCPBundleManifest(),
		testMCPBundleEntry{name: "server/index.js", data: []byte("console.log('demo')\n"), mode: 0o755},
	)
	studio := newStudioForTest(t)
	preview, err := studio.previewMCPBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "local-demo" || preview.DisplayName != "Local Demo" ||
		preview.Version != "1.2.3" || preview.ServerType != "node" {
		t.Fatalf("preview = %#v", preview)
	}
	if len(preview.Digest) != 64 || len(preview.ConfigFields) != 4 ||
		len(preview.Tools) != 1 || preview.Tools[0] != "demo_read" {
		t.Fatalf("incomplete preview = %#v", preview)
	}

	status, err := studio.InstallMCPBundle(bundlePath, preview.Digest, map[string]any{
		"allowed_directories": []any{allowed},
		"api_key":             "secret-value",
		"read_only":           false,
		"timeout":             float64(45),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Name != "local-demo" || status.Transport != "stdio" || status.Enabled {
		t.Fatalf("installed status = %#v", status)
	}
	if status.Command != "node" || len(status.Args) != 2 || status.Args[1] != allowed ||
		!filepath.IsAbs(status.Args[0]) || !strings.HasSuffix(filepath.ToSlash(status.Args[0]), "/server/index.js") {
		t.Fatalf("installed command = %q %#v", status.Command, status.Args)
	}
	if status.Env["API_KEY"] != "secret-value" || status.Env["READ_ONLY"] != "false" ||
		status.Env["TIMEOUT"] != "45" {
		t.Fatalf("installed env = %#v", status.Env)
	}
	if _, err := os.Stat(status.Args[0]); err != nil {
		t.Fatalf("installed entry point: %v", err)
	}
	configs, err := loadMCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != "local-demo" {
		t.Fatalf("saved configs = %#v", configs)
	}
}

func TestMCPBundleRejectsChangedDigestAndInvalidValues(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	bundlePath := writeTestMCPBundle(t, validTestMCPBundleManifest(),
		testMCPBundleEntry{name: "server/index.js", data: []byte("ok"), mode: 0o755},
	)
	studio := newStudioForTest(t)
	preview, err := studio.previewMCPBundle(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := studio.InstallMCPBundle(bundlePath, strings.Repeat("0", 64), nil, false); err == nil ||
		!strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("digest error = %v", err)
	}
	if _, err := studio.InstallMCPBundle(bundlePath, preview.Digest, map[string]any{
		"allowed_directories": []any{t.TempDir()},
		"api_key":             "key",
		"timeout":             float64(100),
	}, false); err == nil || !strings.Contains(err.Error(), "at most 60") {
		t.Fatalf("number bound error = %v", err)
	}
	if _, err := studio.InstallMCPBundle(bundlePath, preview.Digest, map[string]any{
		"allowed_directories": []any{"relative/path"},
		"api_key":             "key",
	}, false); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("path error = %v", err)
	}
}

func TestMCPBundleArchiveAndManifestSecurity(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(map[string]any)
		entries  []testMCPBundleEntry
		wantText string
	}{
		{
			name: "traversal",
			entries: []testMCPBundleEntry{
				{name: "server/index.js", data: []byte("ok"), mode: 0o755},
				{name: "../escape", data: []byte("bad"), mode: 0o600},
			},
			wantText: "escapes",
		},
		{
			name: "symlink",
			entries: []testMCPBundleEntry{
				{name: "server/index.js", data: []byte("ok"), mode: 0o755},
				{name: "server/link", data: []byte("/tmp"), mode: os.ModeSymlink | 0o777},
			},
			wantText: "unsupported",
		},
		{
			name: "normalized duplicate",
			entries: []testMCPBundleEntry{
				{name: "server/index.js", data: []byte("ok"), mode: 0o755},
				{name: "server/./index.js", data: []byte("duplicate"), mode: 0o755},
			},
			wantText: "duplicate",
		},
		{
			name: "missing entry point",
			entries: []testMCPBundleEntry{
				{name: "server/other.js", data: []byte("ok"), mode: 0o755},
			},
			wantText: "entry_point is missing",
		},
		{
			name: "unsupported uv",
			mutate: func(manifest map[string]any) {
				manifest["server"].(map[string]any)["type"] = "uv"
			},
			entries: []testMCPBundleEntry{
				{name: "server/index.js", data: []byte("ok"), mode: 0o755},
			},
			wantText: "UV-based",
		},
		{
			name: "wrong platform",
			mutate: func(manifest map[string]any) {
				other := "win32"
				if runtime.GOOS == "windows" {
					other = "darwin"
				}
				manifest["compatibility"] = map[string]any{"platforms": []any{other}}
			},
			entries: []testMCPBundleEntry{
				{name: "server/index.js", data: []byte("ok"), mode: 0o755},
			},
			wantText: "does not support this platform",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validTestMCPBundleManifest()
			if tc.mutate != nil {
				tc.mutate(manifest)
			}
			bundlePath := writeTestMCPBundle(t, manifest, tc.entries...)
			studio := newStudioForTest(t)
			if _, err := studio.previewMCPBundle(bundlePath); err == nil ||
				!strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantText)
			}
		})
	}
}

package studio

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPreviewHTTPHelperProcess is inert in the normal suite. A lifecycle test
// starts the current test binary with a marker after `--`, giving us a real,
// dependency-free HTTP dev server on every supported OS.
func TestPreviewHTTPHelperProcess(t *testing.T) {
	marker := slices.Index(os.Args, "gokin-preview-http-helper")
	if marker < 0 || marker+1 >= len(os.Args) {
		return
	}
	portText := os.Args[marker+1]
	if portText == "env" {
		portText = os.Getenv("PORT")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		os.Exit(91)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'")
		_, _ = io.WriteString(w, "<!doctype html><html><head><title>Fixture</title></head><body>gokin preview ready</body></html>")
	})
	if err := http.ListenAndServe(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), handler); err != nil {
		os.Exit(92)
	}
}

func TestSessionPreviewServerAutoPortAndURLOnlyAttach(t *testing.T) {
	s := newStudioForTest(t)
	s.ctx = context.Background()
	dir := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferredPort := occupied.Addr().(*net.TCPAddr).Port
	existing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>existing preview</body></html>")
	}))
	defer existing.Close()
	existingURL, _ := url.Parse(existing.URL)
	existingPort, _ := strconv.Atoi(existingURL.Port())
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"version": "0.0.1", "configurations": []map[string]any{
		{
			"name": "auto", "runtimeExecutable": os.Args[0],
			"runtimeArgs": []string{"-test.run=^TestPreviewHTTPHelperProcess$", "--", "gokin-preview-http-helper", "env"},
			"port":        preferredPort, "autoPort": true,
		},
		{"name": "existing", "url": existing.URL, "port": existingPort},
	}}
	data, _ := json.Marshal(config)
	if err := writeFile(filepath.Join(dir, ".claude", "launch.json"), string(data)); err != nil {
		t.Fatal(err)
	}
	project, err := s.AddProject("modern-preview", dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetSessionPreviewConfig(project.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	autoStatus, err := s.StartSessionPreviewServer(project.ID, "default", "auto", loaded.Configurations[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	if autoStatus.Port == preferredPort {
		t.Fatal("autoPort did not replace an occupied preferred port")
	}
	deadline := time.Now().Add(5 * time.Second)
	for autoStatus.State != "running" && time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		autoStatus, _ = s.GetSessionPreviewServerStatus(project.ID, "default", "auto")
	}
	if autoStatus.State != "running" {
		t.Fatalf("auto-port server never became ready: %+v", autoStatus)
	}
	attachStatus, err := s.StartSessionPreviewServer(project.ID, "default", "existing", loaded.Configurations[1].Command)
	if err != nil || attachStatus.State != "running" || attachStatus.PID != 0 {
		t.Fatalf("url-only attach = %+v, %v", attachStatus, err)
	}
	response, err := http.Get(attachStatus.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(body), "existing preview") || !strings.Contains(string(body), "gokin-preview-result") {
		t.Fatalf("attached proxy body = %q", body)
	}
	s.stopPreviewServers("", "", true)
}

func TestStripJSONCommentsPreservesStrings(t *testing.T) {
	raw := []byte("{\n// comment\n\"url\":\"http://localhost/*ok*/\",/* block */\"value\":1\n}")
	clean, err := stripJSONComments(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean)
	if !strings.Contains(text, "http://localhost/*ok*/") || strings.Contains(text, "comment") || strings.Contains(text, " block ") {
		t.Fatalf("JSONC stripping corrupted content: %s", text)
	}
	if _, err := stripJSONComments([]byte("{/* never closes")); err == nil {
		t.Fatal("unterminated block comment was accepted")
	}
}

func TestPreviewBridgeElementSelectionContract(t *testing.T) {
	token := "AbCdEfGhIjKlMnOpQrStUvWx"
	script := previewBridgeScript(token, previewStorageBootstrap{})
	for _, marker := range []string{
		"select_element",
		"cancel_element_selection",
		"gokin-preview-select-request",
		"__gokin_preview_selector",
		"stopImmediatePropagation",
		"selectedElement",
		"ancestors.length<4",
		"slice(0,512)",
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("preview element-selection bridge missing %q", marker)
		}
	}
	if !strings.Contains(script, `const token="`+token+`"`) {
		t.Fatal("bridge token was not JSON-encoded into the selection bridge")
	}
	if node, err := exec.LookPath("node"); err == nil {
		command := exec.Command(node, "--check", "-")
		command.Stdin = strings.NewReader(script)
		if output, checkErr := command.CombinedOutput(); checkErr != nil {
			t.Fatalf("generated preview bridge is not valid JavaScript: %v\n%s", checkErr, output)
		}
	}

	policy := addPreviewBridgeNonce("default-src 'self'; script-src-elem 'self'; style-src-elem 'self'", token)
	for _, directive := range []string{"script-src-elem 'self' 'nonce-" + token + "'", "style-src-elem 'self' 'nonce-" + token + "'"} {
		if !strings.Contains(policy, directive) {
			t.Errorf("selection CSP missing %q: %q", directive, policy)
		}
	}
}

func TestLoadSessionPreviewConfigJSONCAndDetection(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{
  // Claude Desktop compatible preview configuration.
  "version": "0.0.1",
  "autoVerify": false,
  "configurations": [{
    "name": "web",
    "runtimeExecutable": "npm",
    "runtimeArgs": ["run", "dev"],
    "port": 4173
  }]
}`
	if err := writeFile(filepath.Join(dir, ".claude", "launch.json"), config); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSessionPreviewConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Source != "file" || loaded.AutoVerify || len(loaded.Configurations) != 1 || loaded.Configurations[0].Port != 4173 || !strings.Contains(loaded.Configurations[0].Command, "npm") {
		t.Fatalf("preview config = %+v", loaded)
	}

	detectedDir := t.TempDir()
	if err := writeFile(filepath.Join(detectedDir, "package.json"), `{"scripts":{"dev":"vite"}}`); err != nil {
		t.Fatal(err)
	}
	detected, err := loadSessionPreviewConfig(detectedDir)
	if err != nil || detected.Source != "detected" || len(detected.Configurations) != 1 || detected.Configurations[0].RuntimeExecutable != "npm" {
		t.Fatalf("detected preview config = %+v, %v", detected, err)
	}
}

func TestLoadSessionPreviewConfigSupportsCurrentClaudeFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{
  "version": "0.0.1",
  "configurations": [
    {
      "name": "web",
      "runtimeExecutable": "npm",
      "runtimeArgs": ["run", "dev"],
      "cwd": "${workspaceFolder}/apps/web",
      "env": { "NODE_ENV": "development" },
      "port": 4173,
      "autoPort": true,
      "url": "http://app.localhost:4173"
    },
    { "name": "node-script", "program": "server.js", "args": ["--verbose"], "port": 4000 },
    { "name": "existing", "url": "https://localhost:8443", "port": 8443 }
  ]
}`
	if err := writeFile(filepath.Join(dir, ".claude", "launch.json"), config); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSessionPreviewConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Configurations) != 3 {
		t.Fatalf("configurations = %+v", loaded.Configurations)
	}
	web := loaded.Configurations[0]
	if web.Cwd != "apps/web" || web.Env["NODE_ENV"] != "development" || web.AutoPort == nil || !*web.AutoPort || web.URL != "http://app.localhost:4173" || !strings.Contains(web.Command, "env NODE_ENV") {
		t.Fatalf("current web fields were not preserved: %+v", web)
	}
	script := loaded.Configurations[1]
	if script.RuntimeExecutable != "node" || !slices.Equal(script.RuntimeArgs, []string{"server.js", "--verbose"}) || script.Program != "server.js" {
		t.Fatalf("program/args were not normalized: %+v", script)
	}
	if loaded.Configurations[2].RuntimeExecutable != "" || !strings.Contains(loaded.Configurations[2].Command, "attach to existing server") {
		t.Fatalf("url-only configuration was not accepted: %+v", loaded.Configurations[2])
	}
}

func TestLoadSessionPreviewConfigRejectsUnsafeShape(t *testing.T) {
	for name, content := range map[string]string{
		"unknown":         `{"version":"0.0.1","surprise":true}`,
		"bad-port":        `{"configurations":[{"name":"web","runtimeExecutable":"npm","port":70000}]}`,
		"duplicate":       `{"configurations":[{"name":"web","runtimeExecutable":"npm","port":3000},{"name":"web","runtimeExecutable":"npm","port":3001}]}`,
		"cwd-escape":      `{"configurations":[{"name":"web","runtimeExecutable":"npm","cwd":"../outside"}]}`,
		"protected-env":   `{"configurations":[{"name":"web","runtimeExecutable":"npm","env":{"HOME":"/tmp/escape"}}]}`,
		"external-url":    `{"configurations":[{"name":"web","url":"https://example.com","port":443}]}`,
		"url-path":        `{"configurations":[{"name":"web","url":"http://localhost:3000/admin"}]}`,
		"url-port":        `{"configurations":[{"name":"web","url":"http://localhost:4000","port":3000}]}`,
		"args-only":       `{"configurations":[{"name":"web","args":["--bad"]}]}`,
		"two-executables": `{"configurations":[{"name":"web","runtimeExecutable":"npm","program":"server.js"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := writeFile(filepath.Join(dir, ".claude", "launch.json"), content); err != nil {
				t.Fatal(err)
			}
			if _, err := loadSessionPreviewConfig(dir); err == nil {
				t.Fatal("invalid preview config was accepted")
			}
		})
	}
}

func TestPreviewWorkingDirectoryAndEnvironmentStayScoped(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "apps", "web")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolvePreviewWorkingDirectory(dir, "apps/web")
	expected, _ := filepath.EvalSymlinks(inside)
	if err != nil || resolved != expected {
		t.Fatalf("resolved cwd = %q, %v", resolved, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err == nil {
		if _, err := resolvePreviewWorkingDirectory(dir, "escape"); err == nil {
			t.Fatal("symlink cwd escaped the session workspace")
		}
	}
	env := mergePreviewEnvironment([]string{"PATH=/safe", "PORT=1"}, map[string]string{"NODE_ENV": "development"}, "PORT=4173")
	if !slices.Contains(env, "PATH=/safe") || !slices.Contains(env, "NODE_ENV=development") || !slices.Contains(env, "PORT=4173") || slices.Contains(env, "PORT=1") {
		t.Fatalf("merged environment = %#v", env)
	}
}

func TestSaveDetectedSessionPreviewConfigIsReviewBoundAndDoesNotOverwrite(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "package.json"), `{"scripts":{"dev":"vite"}}`); err != nil {
		t.Fatal(err)
	}
	info, err := s.AddProject("detected-preview", dir)
	if err != nil {
		t.Fatal(err)
	}
	detected, err := s.GetSessionPreviewConfig(info.ID, "default")
	if err != nil || detected.Source != "detected" || len(detected.Configurations) != 1 {
		t.Fatalf("detected = %+v, %v", detected, err)
	}
	selected := detected.Configurations[0]
	if _, err := s.SaveDetectedSessionPreviewConfig(info.ID, "default", selected.Name, "different command"); err == nil {
		t.Fatal("save accepted an unreviewed detected command")
	}
	saved, err := s.SaveDetectedSessionPreviewConfig(info.ID, "default", selected.Name, selected.Command)
	if err != nil || saved.Source != "file" || len(saved.Configurations) != 1 {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	before, err := os.ReadFile(filepath.Join(dir, ".claude", "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDetectedSessionPreviewConfig(info.ID, "default", selected.Name, selected.Command); err == nil {
		t.Fatal("save overwrote an existing launch.json")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".claude", "launch.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("existing launch.json changed after rejected save")
	}
}

func TestSessionPreviewServerBecomesReachableAndProjectRemovalStopsIt(t *testing.T) {
	s := newStudioForTest(t)
	s.ctx = context.Background()
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"version": "0.0.1",
		"configurations": []map[string]any{{
			"name":              "test-http",
			"runtimeExecutable": os.Args[0],
			"runtimeArgs":       []string{"-test.run=^TestPreviewHTTPHelperProcess$", "--", "gokin-preview-http-helper", strconv.Itoa(port)},
			"port":              port,
		}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "launch.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := s.AddProject("http-preview", dir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetSessionPreviewConfig(info.ID, "default")
	if err != nil || len(loaded.Configurations) != 1 {
		t.Fatalf("config = %+v, %v", loaded, err)
	}
	status, err := s.StartSessionPreviewServer(info.ID, "default", "test-http", loaded.Configurations[0].Command)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for status.State != "running" && time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		status, err = s.GetSessionPreviewServerStatus(info.ID, "default", "test-http")
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "running" {
		t.Fatalf("server never became ready: %+v", status)
	}
	response, err := http.Get(status.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !strings.Contains(string(body), "gokin preview ready") ||
		!strings.Contains(string(body), "gokin-preview-result") || status.BridgeToken == "" ||
		!strings.Contains(response.Header.Get("Content-Security-Policy"), "nonce-"+status.BridgeToken) {
		t.Fatalf("preview response = %q, %v", body, err)
	}
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatal(err)
	}
	s.previewMu.Lock()
	remaining := len(s.previewServers)
	s.previewMu.Unlock()
	if remaining != 0 {
		t.Fatalf("project removal left %d preview runs registered", remaining)
	}
	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if response, requestErr := client.Get(status.URL); requestErr != nil {
			return
		} else {
			_ = response.Body.Close()
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("preview server still reachable after project removal: %s", status.URL)
}

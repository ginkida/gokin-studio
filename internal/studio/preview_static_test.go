package studio

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func staticPreviewClient(t *testing.T, browserURL, proxyURL string) *http.Client {
	t.Helper()
	browser, err := url.Parse(browserURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == browser.Host {
			address = proxy.Host
		}
		return dialer.DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Jar: jar}
}

func TestSessionStaticPreviewServesRelativeAssetsBehindSecretOrigin(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Static Preview")
	root := project.Directory
	if err := os.MkdirAll(filepath.Join(root, "site", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "site", "index.html"), []byte(`<!doctype html><link rel="stylesheet" href="assets/site.css"><h1>Static fixture</h1>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "site", "assets", "site.css"), []byte("h1{color:green}"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.css")
	if err := os.WriteFile(outside, []byte("must-not-leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "site", "assets", "outside.css")); err != nil {
		t.Fatal(err)
	}

	status, err := s.OpenSessionPreviewFile(project.ID, "default", "site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseSessionPreviewFile(project.ID, "default", status.BridgeToken) })
	if status.State != "running" || status.BrowserURL == "" || status.BridgeToken == "" || !strings.Contains(status.BrowserURL, staticPreviewQueryToken+"=") {
		t.Fatalf("unexpected static preview status: %+v", status)
	}
	run := s.previewServers[previewServerKey(project.ID, "default", staticPreviewConfiguration)]
	if run == nil || run.staticPath != "site/index.html" {
		t.Fatalf("static run was not registered: %#v", run)
	}

	unauthorized, err := http.Get(run.proxyURL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusNotFound {
		t.Fatalf("proxy accepted a request without its secret: %d", unauthorized.StatusCode)
	}
	direct, err := http.Get(run.targetURL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	_ = direct.Body.Close()
	if direct.StatusCode != http.StatusNotFound {
		t.Fatalf("internal file server accepted a request without its proxy token: %d", direct.StatusCode)
	}

	client := staticPreviewClient(t, status.BrowserURL, run.proxyURL)
	response, err := client.Get(status.BrowserURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(response.Request.URL.RawQuery, staticPreviewQueryToken) {
		t.Fatalf("bootstrap redirect did not remove the access token: status=%d url=%s", response.StatusCode, response.Request.URL)
	}
	if !strings.Contains(string(body), "Static fixture") || !strings.Contains(string(body), "gokin-preview-ready") {
		t.Fatalf("HTML or diagnostics bridge missing: %s", body)
	}
	if policy := response.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "connect-src 'self'") || !strings.Contains(policy, "nonce-") {
		t.Fatalf("offline CSP was not retained and nonce-extended: %q", policy)
	}

	assetURL := response.Request.URL.ResolveReference(&url.URL{Path: "assets/site.css"})
	asset, err := client.Get(assetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	assetBody, _ := io.ReadAll(asset.Body)
	_ = asset.Body.Close()
	if asset.StatusCode != http.StatusOK || string(assetBody) != "h1{color:green}" {
		t.Fatalf("relative asset unavailable: status=%d body=%q", asset.StatusCode, assetBody)
	}
	leakURL := response.Request.URL.ResolveReference(&url.URL{Path: "assets/outside.css"})
	leak, err := client.Get(leakURL.String())
	if err != nil {
		t.Fatal(err)
	}
	leakBody, _ := io.ReadAll(leak.Body)
	_ = leak.Body.Close()
	if leak.StatusCode != http.StatusNotFound || strings.Contains(string(leakBody), "must-not-leak") {
		t.Fatalf("static preview followed an outward symlink: status=%d body=%q", leak.StatusCode, leakBody)
	}
}

func TestSessionStaticPreviewRejectsUnsupportedAndIsolatesSessions(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Static Isolation")
	if err := os.WriteFile(filepath.Join(project.Directory, "secret.go"), []byte("package secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenSessionPreviewFile(project.ID, "default", "secret.go"); err == nil {
		t.Fatal("static preview accepted a source file entry")
	}
	if _, err := s.OpenSessionPreviewFile(project.ID, "default", "../outside.html"); err == nil {
		t.Fatal("static preview accepted traversal")
	}
	if err := os.WriteFile(filepath.Join(project.Directory, "one.png"), []byte("not-an-image-but-bounded"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := s.OpenSessionPreviewFile(project.ID, "default", "one.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.OpenSessionPreviewFile(project.ID, "default", "one.png")
	if err != nil {
		t.Fatal(err)
	}
	if first.BrowserURL == second.BrowserURL || first.BridgeToken == second.BridgeToken {
		t.Fatal("reopened static previews reused a browser origin or bridge token")
	}
	run := s.previewServers[previewServerKey(project.ID, "default", staticPreviewConfiguration)]
	client := staticPreviewClient(t, second.BrowserURL, run.proxyURL)
	response, err := client.Get(second.BrowserURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `<img src="./one.png"`) || !strings.Contains(string(body), "gokin-preview-ready") {
		t.Fatalf("image preview did not use the inspectable HTML wrapper: status=%d body=%q", response.StatusCode, body)
	}
	if err := s.CloseSessionPreviewFile(project.ID, "default", first.BridgeToken); err != nil {
		t.Fatal(err)
	}
	if status, err := s.GetSessionPreviewFileStatus(project.ID, "default"); err != nil || status.State != "running" {
		t.Fatalf("stale token closed the replacement preview: %+v, %v", status, err)
	}
	if err := s.CloseSessionPreviewFile(project.ID, "default", second.BridgeToken); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetSessionPreviewFileStatus(project.ID, "default")
	if err != nil || status.State != "stopped" {
		t.Fatalf("static preview did not close: %+v, %v", status, err)
	}
}

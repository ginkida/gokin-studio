package studio

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

type externalBrowserRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn externalBrowserRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func externalBrowserTestClient(t *testing.T, browserURL string) *http.Client {
	t.Helper()
	parsed, err := url.Parse(browserURL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", parsed.Port()))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Jar: jar}
}

func TestNormalizeExternalBrowserURLAndSSRFReview(t *testing.T) {
	parsed, origin, err := normalizeExternalBrowserURL("https://ExAmple.COM.:443/docs?q=1")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.String() != "https://example.com/docs?q=1" || origin != "https://example.com" {
		t.Fatalf("normalized URL = %q, origin = %q", parsed, origin)
	}
	for _, raw := range []string{"file:///etc/passwd", "https://user:pass@example.com", "http://example.com:99999", "javascript:alert(1)"} {
		if _, _, err := normalizeExternalBrowserURL(raw); err == nil {
			t.Errorf("unsafe URL accepted: %q", raw)
		}
	}
	s := NewStudio()
	if _, err := s.ReviewExternalBrowserNavigation("http://127.0.0.1:8080"); err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("localhost review error = %v", err)
	}
}

func TestExternalBrowserScriptPolicyRequiresNativeGuard(t *testing.T) {
	base, _ := url.Parse("https://example.com/page")
	fixture := []byte(`<!doctype html><html><head><script src="https://cdn.example.net/app.js"></script></head><body onload="start()"><a href="javascript:start()">Run</a><iframe src="https://other.example/frame"></iframe></body></html>`)
	run := &externalBrowserRun{id: "tab", origin: "https://example.com", bridgeToken: "bridge-token", accessToken: "resource-secret"}

	run.activeScripts = false
	safe, err := run.rewriteHTML(fixture, base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(safe), `<script src=`) || strings.Contains(string(safe), `<body onload=`) || !strings.Contains(string(safe), `<template`) || !strings.Contains(string(safe), `href="#"`) || strings.Contains(string(safe), `<iframe`) {
		t.Fatalf("safe-mode HTML policy failed: %s", safe)
	}
	if csp := externalBrowserCSP(run.bridgeToken, false); !strings.Contains(csp, "script-src 'nonce-bridge-token'") || strings.Contains(csp, "'unsafe-eval'") {
		t.Fatalf("safe-mode CSP = %q", csp)
	}

	run.activeScripts = true
	interactive, err := run.rewriteHTML(fixture, base)
	if err != nil {
		t.Fatal(err)
	}
	interactiveText := string(interactive)
	if !strings.Contains(interactiveText, `<script src="/__gokin_external_resource?`) || !strings.Contains(interactiveText, `s=`) || !strings.Contains(interactiveText, `<body onload="start()"`) || !strings.Contains(interactiveText, `href="javascript:start()"`) || strings.Contains(interactiveText, `<iframe`) {
		t.Fatalf("native-guard HTML policy failed: %s", interactiveText)
	}
	if csp := externalBrowserCSP(run.bridgeToken, true); !strings.Contains(csp, "script-src 'self' 'unsafe-inline' 'unsafe-eval'") || strings.Contains(csp, "script-src 'nonce-") {
		t.Fatalf("native-guard CSP = %q", csp)
	}
}

func TestExternalBrowserResourceURLsAreTargetBound(t *testing.T) {
	run := &externalBrowserRun{accessToken: "resource-secret", target: mustExternalBrowserURL(t, "https://example.com/start")}
	target := mustExternalBrowserURL(t, "https://cdn.example.net/app.js#fragment")
	resource := run.proxyResource(target)
	parsed, err := url.Parse(resource)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := run.upstreamURL(parsed)
	if err != nil || resolved.String() != "https://cdn.example.net/app.js" {
		t.Fatalf("signed resource = %v, %v", resolved, err)
	}
	query := parsed.Query()
	query.Set("u", base64.RawURLEncoding.EncodeToString([]byte("http://127.0.0.1/private")))
	parsed.RawQuery = query.Encode()
	if _, err := run.upstreamURL(parsed); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("target substitution error = %v", err)
	}
}

func mustExternalBrowserURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestExternalBrowserPermissionPersistsExactOrigin(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.testExternalBrowserValidate = func(string) error { return nil }
	if err := s.approveBrowserOrigin("https://docs.example.com"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStudio()
	reloaded.testExternalBrowserValidate = func(string) error { return nil }
	review, err := reloaded.ReviewExternalBrowserNavigation("https://docs.example.com/guide")
	if err != nil || !review.Approved {
		t.Fatalf("persisted review = %+v, %v", review, err)
	}
	sibling, err := reloaded.ReviewExternalBrowserNavigation("https://api.docs.example.com/")
	if err != nil || sibling.Approved {
		t.Fatalf("subdomain inherited exact-origin permission: %+v, %v", sibling, err)
	}
	if err := reloaded.RevokeExternalBrowserPermission("https://docs.example.com"); err != nil {
		t.Fatal(err)
	}
	permissions, err := reloaded.ListExternalBrowserPermissions()
	if err != nil || len(permissions) != 0 {
		t.Fatalf("permissions after revoke = %+v, %v", permissions, err)
	}
}

func TestExternalBrowserProxyIsolationRewritingAndNavigation(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "External Browser")
	s.testExternalBrowserValidate = func(string) error { return nil }
	s.testExternalBrowserClient = &http.Client{Transport: externalBrowserRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := "asset"
		contentType := "text/plain"
		if request.URL.Host == "example.com" && request.URL.Path == "/docs" {
			contentType = "text/html; charset=utf-8"
			body = `<!doctype html><html><head><title>Fixture</title><script>location.href="http://127.0.0.1/private"</script><link rel="stylesheet" href="https://cdn.example.net/site.css"></head><body onload="location.href='http://127.0.0.1/'"><a id="same" href="/next">Next</a><a id="cross" href="https://other.example.org/page">Other</a><a id="js" href="javascript:alert(1)">JS</a><iframe src="http://127.0.0.1/private"></iframe><img src="https://cdn.example.net/image.png"></body></html>`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{contentType}, "X-Frame-Options": []string{"DENY"}, "Refresh": []string{"0; url=http://127.0.0.1/private"}, "Link": []string{"<http://127.0.0.1/private>; rel=preload"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	tab, err := s.OpenExternalBrowserTab(project.ID, "default", "https://example.com/docs", "once")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseExternalBrowserTab(project.ID, "default", tab.ID) })
	if tab.BrowserURL == "" || tab.BridgeToken == "" || !strings.Contains(tab.BrowserURL, "__gokin_browser_token=") {
		t.Fatalf("unexpected tab: %+v", tab)
	}
	unauthorized, err := externalBrowserTestClient(t, tab.BrowserURL).Get(strings.Split(tab.BrowserURL, "?")[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusForbidden {
		t.Fatalf("proxy accepted missing access cookie: %d", unauthorized.StatusCode)
	}
	client := externalBrowserTestClient(t, tab.BrowserURL)
	response, err := client.Get(tab.BrowserURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	text := string(body)
	if response.StatusCode != http.StatusOK || strings.Contains(response.Request.URL.RawQuery, "__gokin_browser_token") {
		t.Fatalf("bootstrap response = %d at %s", response.StatusCode, response.Request.URL)
	}
	for _, expected := range []string{"gokin-external-navigation", `href="/next"`, "/__gokin_external_resource?", "https://other.example.org/page", `href="#"`, "<template>location.href="} {
		if !strings.Contains(text, expected) {
			t.Errorf("rewritten page missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "<body onload") || strings.Contains(text, "<iframe") {
		t.Fatalf("active page content survived isolation: %s", text)
	}
	csp := response.Header.Get("Content-Security-Policy")
	if response.Header.Get("X-Frame-Options") != "" || response.Header.Get("Refresh") != "" || response.Header.Get("Link") != "" || !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "script-src 'nonce-") || !strings.Contains(csp, "frame-src 'none'") {
		t.Fatalf("unsafe response headers: %#v", response.Header)
	}
	s.externalBrowserMu.Lock()
	run := s.externalBrowserTabs[tab.ID]
	resourceTarget, _ := url.Parse("https://cdn.example.net/image.png")
	signedResource := run.proxyResource(resourceTarget)
	localBase := run.localBase
	s.externalBrowserMu.Unlock()
	resourceResponse, err := client.Get(localBase + signedResource)
	if err != nil {
		t.Fatal(err)
	}
	resourceBody, _ := io.ReadAll(resourceResponse.Body)
	_ = resourceResponse.Body.Close()
	if resourceResponse.StatusCode != http.StatusOK || string(resourceBody) != "asset" {
		t.Fatalf("signed resource response = %d %q", resourceResponse.StatusCode, resourceBody)
	}
	tampered, _ := url.Parse(localBase + signedResource)
	tamperedQuery := tampered.Query()
	tamperedQuery.Set("s", "invalid-signature")
	tampered.RawQuery = tamperedQuery.Encode()
	tamperedResponse, err := client.Get(tampered.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = tamperedResponse.Body.Close()
	if tamperedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered resource status = %d", tamperedResponse.StatusCode)
	}
	otherTarget, _ := url.Parse("https://other.example.org/page")
	s.externalBrowserMu.Lock()
	signedNavigation := s.externalBrowserTabs[tab.ID].proxyResource(otherTarget)
	s.externalBrowserMu.Unlock()
	navigationRequest, _ := http.NewRequest(http.MethodGet, localBase+signedNavigation, nil)
	navigationRequest.Header.Set("Sec-Fetch-Mode", "navigate")
	navigationRequest.Header.Set("Sec-Fetch-Dest", "document")
	navigationResponse, err := client.Do(navigationRequest)
	if err != nil {
		t.Fatal(err)
	}
	navigationBody, _ := io.ReadAll(navigationResponse.Body)
	_ = navigationResponse.Body.Close()
	if navigationResponse.StatusCode != http.StatusOK || !strings.Contains(string(navigationBody), "Domain approval required") {
		t.Fatalf("resource document bypass response = %d %q", navigationResponse.StatusCode, navigationBody)
	}
	same, err := s.NavigateExternalBrowserTab(project.ID, "default", tab.ID, "https://example.com/next", "existing")
	if err != nil || same.Origin != tab.Origin {
		t.Fatalf("same-origin navigation = %+v, %v", same, err)
	}
	if _, err := s.NavigateExternalBrowserTab(project.ID, "default", tab.ID, "https://other.example.org/page", "existing"); err == nil {
		t.Fatal("cross-origin navigation did not require approval")
	}
	cross, err := s.NavigateExternalBrowserTab(project.ID, "default", tab.ID, "https://other.example.org/page", "once")
	if err != nil {
		t.Fatal(err)
	}
	if cross.Origin == tab.Origin || cross.BrowserURL == same.BrowserURL {
		t.Fatalf("cross-origin navigation reused local origin: before=%+v after=%+v", same, cross)
	}
	listed, err := s.ListExternalBrowserTabs(project.ID, "default")
	if err != nil || len(listed) != 1 || listed[0].Origin != cross.Origin {
		t.Fatalf("listed tabs = %+v, %v", listed, err)
	}
}

func TestArchivingSessionClosesExternalBrowserTabs(t *testing.T) {
	s := newStudioForTest(t)
	project := addTestProject(t, s, "Archived Browser")
	s.projects[project.ID].testEmitter = func(string, any) {}
	extra, err := s.CreateChatSession(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.testExternalBrowserValidate = func(string) error { return nil }
	s.testExternalBrowserClient = &http.Client{Transport: externalBrowserRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>Archive fixture</title>")), Request: request}, nil
	})}
	tab, err := s.OpenExternalBrowserTab(project.ID, extra.ID, "https://example.com/", "once")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveChatSession(project.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	s.externalBrowserMu.Lock()
	_, stillOpen := s.externalBrowserTabs[tab.ID]
	s.externalBrowserMu.Unlock()
	if stillOpen {
		t.Fatal("archived session retained its external browser listener")
	}
}

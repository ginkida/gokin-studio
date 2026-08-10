package studio

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/google/uuid"
	"golang.org/x/net/html"
	"golang.org/x/net/idna"
)

const (
	externalBrowserPermissionVersion = 1
	externalBrowserPermissionMax     = 256
	externalBrowserPermissionBytes   = 64 << 10
	externalBrowserTabsPerSession    = 8
	externalBrowserRequestBytes      = 8 << 20
	externalBrowserResponseBytes     = 32 << 20
	permissionsFileName              = "browser-permissions.json"
)

type ExternalBrowserNavigationReview struct {
	URL      string `json:"url"`
	Origin   string `json:"origin"`
	Hostname string `json:"hostname"`
	Approved bool   `json:"approved"`
}

type ExternalBrowserTab struct {
	ID            string `json:"id"`
	ProjectID     string `json:"projectID"`
	SessionID     string `json:"sessionID"`
	URL           string `json:"url"`
	Origin        string `json:"origin"`
	BrowserURL    string `json:"browserURL"`
	BridgeToken   string `json:"bridgeToken"`
	Title         string `json:"title"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	Active        bool   `json:"active"`
	ActiveScripts bool   `json:"activeScripts"`
}

type ExternalBrowserPermission struct {
	Origin string `json:"origin"`
}

type externalBrowserPermissionFile struct {
	Version int      `json:"version"`
	Origins []string `json:"origins"`
}

type externalBrowserRun struct {
	mu            sync.RWMutex
	id            string
	projectID     string
	sessionID     string
	target        *url.URL
	origin        string
	localBase     string
	browserURL    string
	bridgeToken   string
	accessToken   string
	title         string
	state         string
	err           string
	createdAt     int64
	server        *http.Server
	jar           http.CookieJar
	client        *http.Client
	testNetwork   bool
	activeScripts bool
}

func externalBrowserPermissionPath() string {
	return filepath.Join(configDir(), permissionsFileName)
}

func normalizeExternalBrowserURL(raw string) (*url.URL, string, error) {
	if raw == "" || len(raw) > 4096 || !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 {
		return nil, "", fmt.Errorf("browser URL must be valid UTF-8 and at most 4096 bytes")
	}
	for _, value := range raw {
		if value < 0x20 || value == 0x7f {
			return nil, "", fmt.Errorf("browser URL contains control characters")
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return nil, "", fmt.Errorf("invalid absolute browser URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("only http and https browser URLs are supported")
	}
	if parsed.User != nil {
		return nil, "", fmt.Errorf("browser URLs cannot contain credentials")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" {
		return nil, "", fmt.Errorf("browser URL is missing a hostname")
	}
	if net.ParseIP(hostname) == nil {
		hostname, err = idna.Lookup.ToASCII(hostname)
		if err != nil || len(hostname) > 253 {
			return nil, "", fmt.Errorf("browser URL has an invalid hostname")
		}
		hostname = strings.ToLower(hostname)
	}
	port := parsed.Port()
	if port != "" {
		numeric, err := strconv.Atoi(port)
		if err != nil || numeric < 1 || numeric > 65535 {
			return nil, "", fmt.Errorf("browser URL has an invalid port")
		}
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" && !((parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443")) {
		host += ":" + port
	}
	parsed.Host = host
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	origin := parsed.Scheme + "://" + host
	return parsed, origin, nil
}

func (s *Studio) validateExternalBrowserURL(raw string) (*url.URL, string, error) {
	parsed, origin, err := normalizeExternalBrowserURL(raw)
	if err != nil {
		return nil, "", err
	}
	if s.testExternalBrowserValidate != nil {
		if err := s.testExternalBrowserValidate(parsed.String()); err != nil {
			return nil, "", err
		}
	} else if result := security.ValidateURLForSSRF(parsed.String()); !result.Valid {
		return nil, "", fmt.Errorf("browser navigation blocked by SSRF protection: %s", result.Reason)
	}
	return parsed, origin, nil
}

func (s *Studio) loadBrowserPermissionsLocked() error {
	if s.browserPermissionsRead {
		return nil
	}
	s.browserPermissionsRead = true
	if s.browserPermissions == nil {
		s.browserPermissions = make(map[string]bool)
	}
	data, err := readRegularFileLimited(externalBrowserPermissionPath(), externalBrowserPermissionBytes)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read browser permissions: %w", err)
	}
	var stored externalBrowserPermissionFile
	if err := json.Unmarshal(data, &stored); err != nil || stored.Version != externalBrowserPermissionVersion || len(stored.Origins) > externalBrowserPermissionMax {
		return fmt.Errorf("browser permissions file is invalid")
	}
	for _, raw := range stored.Origins {
		parsed, origin, err := normalizeExternalBrowserURL(raw)
		if err != nil || parsed.String() != origin+"/" || raw != origin {
			return fmt.Errorf("browser permissions file contains an invalid origin")
		}
		s.browserPermissions[origin] = true
	}
	return nil
}

func (s *Studio) saveBrowserPermissionsLocked() error {
	origins := make([]string, 0, len(s.browserPermissions))
	for origin := range s.browserPermissions {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	data, err := json.MarshalIndent(externalBrowserPermissionFile{Version: externalBrowserPermissionVersion, Origins: origins}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(externalBrowserPermissionPath(), append(data, '\n'), 0o600)
}

func (s *Studio) browserOriginApproved(origin string) (bool, error) {
	s.browserPermissionMu.Lock()
	defer s.browserPermissionMu.Unlock()
	if err := s.loadBrowserPermissionsLocked(); err != nil {
		return false, err
	}
	return s.browserPermissions[origin], nil
}

func (s *Studio) approveBrowserOrigin(origin string) error {
	s.browserPermissionMu.Lock()
	defer s.browserPermissionMu.Unlock()
	if err := s.loadBrowserPermissionsLocked(); err != nil {
		return err
	}
	if s.browserPermissions[origin] {
		return nil
	}
	if len(s.browserPermissions) >= externalBrowserPermissionMax {
		return fmt.Errorf("browser permission limit reached; revoke an origin in Settings")
	}
	s.browserPermissions[origin] = true
	if err := s.saveBrowserPermissionsLocked(); err != nil {
		delete(s.browserPermissions, origin)
		return fmt.Errorf("save browser permission: %w", err)
	}
	return nil
}

func (s *Studio) ReviewExternalBrowserNavigation(rawURL string) (*ExternalBrowserNavigationReview, error) {
	parsed, origin, err := s.validateExternalBrowserURL(rawURL)
	if err != nil {
		return nil, err
	}
	approved, err := s.browserOriginApproved(origin)
	if err != nil {
		return nil, err
	}
	return &ExternalBrowserNavigationReview{URL: parsed.String(), Origin: origin, Hostname: parsed.Hostname(), Approved: approved}, nil
}

func validateExternalBrowserApproval(approval string, alreadyApproved bool) error {
	switch approval {
	case "once", "always":
		return nil
	case "existing":
		if alreadyApproved {
			return nil
		}
	}
	return fmt.Errorf("explicit browser domain approval is required")
}

func (s *Studio) externalBrowserClient() (*http.Client, error) {
	if s.testExternalBrowserClient != nil {
		clone := *s.testExternalBrowserClient
		clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		return &clone, nil
	}
	return security.NewSSRFSafeHTTPClient()
}

func externalBrowserTabSnapshot(run *externalBrowserRun) ExternalBrowserTab {
	run.mu.RLock()
	defer run.mu.RUnlock()
	return ExternalBrowserTab{ID: run.id, ProjectID: run.projectID, SessionID: run.sessionID, URL: run.target.String(), Origin: run.origin, BrowserURL: run.browserURL, BridgeToken: run.bridgeToken, Title: run.title, State: run.state, Error: run.err, CreatedAt: run.createdAt, ActiveScripts: run.activeScripts}
}

func externalBrowserSessionKey(projectID, sessionID string) string {
	return projectID + "\x00" + sessionID
}

func localBrowserURL(run *externalBrowserRun, target *url.URL, includeToken bool) string {
	query := target.Query()
	if includeToken {
		query.Set("__gokin_browser_token", run.accessToken)
	}
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	result := run.localBase + path
	if encoded := query.Encode(); encoded != "" {
		result += "?" + encoded
	}
	if target.Fragment != "" {
		result += "#" + url.PathEscape(target.Fragment)
	}
	return result
}

func (s *Studio) newExternalBrowserRun(id, projectID, sessionID string, target *url.URL, origin string, jar http.CookieJar, createdAt int64) (*externalBrowserRun, error) {
	bridgeToken, err := newPreviewBridgeToken()
	if err != nil {
		return nil, err
	}
	accessToken, err := newPreviewBridgeToken()
	if err != nil {
		return nil, err
	}
	if jar == nil {
		jar, err = cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
	}
	client, err := s.externalBrowserClient()
	if err != nil {
		return nil, err
	}
	run := &externalBrowserRun{id: id, projectID: projectID, sessionID: sessionID, target: target, origin: origin, bridgeToken: bridgeToken, accessToken: accessToken, title: target.Hostname(), state: "running", createdAt: createdAt, jar: jar, client: client, testNetwork: s.testExternalBrowserClient != nil, activeScripts: externalBrowserActiveScriptsSupported()}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Loopback IP literal rather than "<token>.localhost": macOS ATS blocks the
	// latter with -1022 (it is not the exempt bare `localhost`), which made
	// every external tab fail to load in the WebView. Each tab keeps its own
	// ephemeral port, so it still gets a distinct web origin, and access stays
	// bound by accessToken.
	run.localBase = "http://127.0.0.1:" + strconv.Itoa(port)
	run.browserURL = localBrowserURL(run, target, true)
	server := &http.Server{Handler: http.HandlerFunc(run.serveHTTP), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	run.server = server
	go func() { _ = server.Serve(listener) }()
	return run, nil
}

func (s *Studio) OpenExternalBrowserTab(projectID, sessionID, rawURL, approval string) (*ExternalBrowserTab, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	target, origin, err := s.validateExternalBrowserURL(rawURL)
	if err != nil {
		return nil, err
	}
	approved, err := s.browserOriginApproved(origin)
	if err != nil {
		return nil, err
	}
	if err := validateExternalBrowserApproval(approval, approved); err != nil {
		return nil, err
	}
	if approval == "always" && !approved {
		if err := s.approveBrowserOrigin(origin); err != nil {
			return nil, err
		}
	}
	s.externalBrowserMu.Lock()
	count := 0
	for _, candidate := range s.externalBrowserTabs {
		if candidate.projectID == projectID && candidate.sessionID == sessionID {
			count++
		}
	}
	if count >= externalBrowserTabsPerSession {
		s.externalBrowserMu.Unlock()
		return nil, fmt.Errorf("at most %d external browser tabs may be open in one chat", externalBrowserTabsPerSession)
	}
	run, err := s.newExternalBrowserRun(uuid.NewString(), projectID, sessionID, target, origin, nil, time.Now().UnixMilli())
	if err == nil {
		s.externalBrowserTabs[run.id] = run
	}
	s.externalBrowserMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("start isolated browser tab: %w", err)
	}
	tab := externalBrowserTabSnapshot(run)
	return &tab, nil
}

func (s *Studio) NavigateExternalBrowserTab(projectID, sessionID, tabID, rawURL, approval string) (*ExternalBrowserTab, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	target, origin, err := s.validateExternalBrowserURL(rawURL)
	if err != nil {
		return nil, err
	}
	s.externalBrowserMu.Lock()
	run := s.externalBrowserTabs[tabID]
	if run == nil || run.projectID != projectID || run.sessionID != sessionID {
		s.externalBrowserMu.Unlock()
		return nil, fmt.Errorf("external browser tab not found")
	}
	currentOrigin := run.origin
	if origin == currentOrigin {
		run.mu.Lock()
		run.target = target
		run.browserURL = localBrowserURL(run, target, false)
		run.title = target.Hostname()
		run.err = ""
		run.mu.Unlock()
		s.externalBrowserMu.Unlock()
		tab := externalBrowserTabSnapshot(run)
		return &tab, nil
	}
	s.externalBrowserMu.Unlock()
	approved, err := s.browserOriginApproved(origin)
	if err != nil {
		return nil, err
	}
	if err := validateExternalBrowserApproval(approval, approved); err != nil {
		return nil, err
	}
	if approval == "always" && !approved {
		if err := s.approveBrowserOrigin(origin); err != nil {
			return nil, err
		}
	}
	s.externalBrowserMu.Lock()
	defer s.externalBrowserMu.Unlock()
	run = s.externalBrowserTabs[tabID]
	if run == nil || run.projectID != projectID || run.sessionID != sessionID {
		return nil, fmt.Errorf("external browser tab not found")
	}
	if run.origin != currentOrigin {
		return nil, fmt.Errorf("external browser tab changed while navigation was being reviewed")
	}
	replacement, err := s.newExternalBrowserRun(run.id, projectID, sessionID, target, origin, run.jar, run.createdAt)
	if err != nil {
		return nil, fmt.Errorf("navigate isolated browser tab: %w", err)
	}
	s.externalBrowserTabs[tabID] = replacement
	if run.server != nil {
		_ = run.server.Close()
	}
	if s.externalBrowserAgent != nil {
		s.externalBrowserAgent.cancel(projectID, sessionID, tabID, "external Browser page changed; inspect the active tab again")
	}
	tab := externalBrowserTabSnapshot(replacement)
	return &tab, nil
}

func (s *Studio) ListExternalBrowserTabs(projectID, sessionID string) ([]ExternalBrowserTab, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	s.externalBrowserMu.Lock()
	defer s.externalBrowserMu.Unlock()
	result := make([]ExternalBrowserTab, 0)
	activeID := s.externalBrowserActive[externalBrowserSessionKey(projectID, sessionID)]
	for _, run := range s.externalBrowserTabs {
		if run.projectID == projectID && run.sessionID == sessionID {
			tab := externalBrowserTabSnapshot(run)
			tab.Active = tab.ID == activeID
			result = append(result, tab)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt < result[j].CreatedAt })
	return result, nil
}

// SetActiveExternalBrowserTab records which visible external tab owns model
// browser requests. Passing an empty tab ID clears routing when Preview, a new
// blank tab, or another pane is active.
func (s *Studio) SetActiveExternalBrowserTab(projectID, sessionID, tabID string) error {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return err
	}
	s.externalBrowserMu.Lock()
	defer s.externalBrowserMu.Unlock()
	key := externalBrowserSessionKey(projectID, sessionID)
	if tabID == "" {
		delete(s.externalBrowserActive, key)
		return nil
	}
	run := s.externalBrowserTabs[tabID]
	if run == nil || run.projectID != projectID || run.sessionID != sessionID {
		return fmt.Errorf("external browser tab not found")
	}
	s.externalBrowserActive[key] = tabID
	return nil
}

// UpdateExternalBrowserTabState accepts only token-bound metadata from the
// currently rendered bridge. It keeps stale-action protection accurate for
// same-origin SPA/history navigation without trusting the page to change its
// approved origin.
func (s *Studio) UpdateExternalBrowserTabState(projectID, sessionID, tabID, bridgeToken, rawURL, title string) error {
	if len(title) > 500 || !utf8.ValidString(title) || strings.IndexByte(title, 0) >= 0 {
		return fmt.Errorf("external browser title is invalid")
	}
	target, origin, err := normalizeExternalBrowserURL(rawURL)
	if err != nil {
		return err
	}
	s.externalBrowserMu.Lock()
	run := s.externalBrowserTabs[tabID]
	if run == nil || run.projectID != projectID || run.sessionID != sessionID {
		s.externalBrowserMu.Unlock()
		return fmt.Errorf("external browser tab not found")
	}
	run.mu.Lock()
	if subtle.ConstantTimeCompare([]byte(run.bridgeToken), []byte(bridgeToken)) != 1 || run.origin != origin {
		run.mu.Unlock()
		s.externalBrowserMu.Unlock()
		return fmt.Errorf("external browser tab ownership changed")
	}
	run.target = target
	run.browserURL = localBrowserURL(run, target, false)
	if strings.TrimSpace(title) != "" {
		run.title = strings.TrimSpace(title)
	}
	run.mu.Unlock()
	s.externalBrowserMu.Unlock()
	return nil
}

func (s *Studio) CloseExternalBrowserTab(projectID, sessionID, tabID string) error {
	s.externalBrowserMu.Lock()
	run := s.externalBrowserTabs[tabID]
	if run == nil || run.projectID != projectID || run.sessionID != sessionID {
		s.externalBrowserMu.Unlock()
		return fmt.Errorf("external browser tab not found")
	}
	delete(s.externalBrowserTabs, tabID)
	key := externalBrowserSessionKey(projectID, sessionID)
	if s.externalBrowserActive[key] == tabID {
		delete(s.externalBrowserActive, key)
	}
	s.externalBrowserMu.Unlock()
	if run.server != nil {
		_ = run.server.Close()
	}
	if s.externalBrowserAgent != nil {
		s.externalBrowserAgent.cancel(projectID, sessionID, tabID, "external Browser tab closed")
	}
	return nil
}

func (s *Studio) stopExternalBrowserTabs(projectID, sessionID string) {
	s.externalBrowserMu.Lock()
	var closing []*externalBrowserRun
	for id, run := range s.externalBrowserTabs {
		if (projectID == "" || run.projectID == projectID) && (sessionID == "" || run.sessionID == sessionID) {
			delete(s.externalBrowserTabs, id)
			closing = append(closing, run)
		}
	}
	for key, tabID := range s.externalBrowserActive {
		run := s.externalBrowserTabs[tabID]
		if run == nil || ((projectID == "" || run.projectID == projectID) && (sessionID == "" || run.sessionID == sessionID)) {
			delete(s.externalBrowserActive, key)
		}
	}
	s.externalBrowserMu.Unlock()
	for _, run := range closing {
		if run.server != nil {
			_ = run.server.Close()
		}
		if s.externalBrowserAgent != nil {
			s.externalBrowserAgent.cancel(run.projectID, run.sessionID, run.id, "external Browser tab closed")
		}
	}
}

func (s *Studio) ListExternalBrowserPermissions() ([]ExternalBrowserPermission, error) {
	s.browserPermissionMu.Lock()
	defer s.browserPermissionMu.Unlock()
	if err := s.loadBrowserPermissionsLocked(); err != nil {
		return nil, err
	}
	result := make([]ExternalBrowserPermission, 0, len(s.browserPermissions))
	for origin := range s.browserPermissions {
		result = append(result, ExternalBrowserPermission{Origin: origin})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Origin < result[j].Origin })
	return result, nil
}

func (s *Studio) RevokeExternalBrowserPermission(origin string) error {
	_, normalized, err := normalizeExternalBrowserURL(origin)
	if err != nil || origin != normalized {
		return fmt.Errorf("invalid browser origin")
	}
	s.browserPermissionMu.Lock()
	defer s.browserPermissionMu.Unlock()
	if err := s.loadBrowserPermissionsLocked(); err != nil {
		return err
	}
	if !s.browserPermissions[origin] {
		return nil
	}
	delete(s.browserPermissions, origin)
	if err := s.saveBrowserPermissionsLocked(); err != nil {
		s.browserPermissions[origin] = true
		return err
	}
	return nil
}

func (run *externalBrowserRun) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("__gokin_browser_token") != "" {
		candidate := request.URL.Query().Get("__gokin_browser_token")
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(run.accessToken)) != 1 {
			http.Error(writer, "Forbidden", http.StatusForbidden)
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "gokin_browser_access", Value: run.accessToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		query := request.URL.Query()
		query.Del("__gokin_browser_token")
		destination := request.URL.EscapedPath()
		if destination == "" {
			destination = "/"
		}
		if encoded := query.Encode(); encoded != "" {
			destination += "?" + encoded
		}
		http.Redirect(writer, request, destination, http.StatusSeeOther)
		return
	}
	cookie, err := request.Cookie("gokin_browser_access")
	if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(run.accessToken)) != 1 {
		http.Error(writer, "Forbidden", http.StatusForbidden)
		return
	}
	upstream, err := run.upstreamURL(request.URL)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if request.URL.Path == "/__gokin_external_resource" && externalBrowserDocumentNavigation(request) {
		_, targetOrigin, normalizeErr := normalizeExternalBrowserURL(upstream.String())
		if normalizeErr != nil {
			http.Error(writer, "Invalid resource navigation", http.StatusBadRequest)
			return
		}
		if targetOrigin != run.origin {
			run.writeNavigationPage(writer, upstream.String())
			return
		}
		http.Redirect(writer, request, run.localPath(upstream), http.StatusTemporaryRedirect)
		return
	}
	if run.client == nil {
		http.Error(writer, "Navigation blocked by SSRF protection", http.StatusForbidden)
		return
	}
	if !run.testNetwork {
		if result := security.ValidateURLForSSRF(upstream.String()); !result.Valid {
			http.Error(writer, "Navigation blocked by SSRF protection", http.StatusForbidden)
			return
		}
	}
	body := http.MaxBytesReader(writer, request.Body, externalBrowserRequestBytes)
	defer body.Close()
	outbound, err := http.NewRequestWithContext(request.Context(), request.Method, upstream.String(), body)
	if err != nil {
		http.Error(writer, "Invalid upstream request", http.StatusBadRequest)
		return
	}
	copyExternalBrowserRequestHeaders(outbound.Header, request.Header)
	outbound.Host = upstream.Host
	outbound.Header.Set("Accept-Encoding", "identity")
	outbound.Header.Set("User-Agent", "Gokin Studio Browser/1.0")
	if request.Header.Get("Origin") != "" {
		outbound.Header.Set("Origin", upstream.Scheme+"://"+upstream.Host)
	}
	if request.Header.Get("Referer") != "" {
		outbound.Header.Set("Referer", upstream.String())
	}
	for _, cookie := range run.jar.Cookies(upstream) {
		outbound.AddCookie(cookie)
	}
	response, err := run.client.Do(outbound)
	if err != nil {
		http.Error(writer, "External page request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	run.jar.SetCookies(upstream, response.Cookies())
	localRedirect := ""
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		if location, err := upstream.Parse(response.Header.Get("Location")); err == nil && location != nil {
			locationURL, locationOrigin, normalizeErr := normalizeExternalBrowserURL(location.String())
			if normalizeErr == nil && locationOrigin == run.origin {
				localRedirect = run.localPath(locationURL)
			} else if normalizeErr == nil {
				run.writeNavigationPage(writer, locationURL.String())
				return
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, externalBrowserResponseBytes+1))
	if err != nil || len(data) > externalBrowserResponseBytes {
		http.Error(writer, "External response exceeds the 32 MiB tab limit", http.StatusBadGateway)
		return
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml") {
		data, err = run.rewriteHTML(data, upstream)
		if err != nil {
			http.Error(writer, "External HTML could not be isolated", http.StatusBadGateway)
			return
		}
		// Only an approved-origin document may re-point the tab. Without this
		// check ANY cross-origin subresource proxied through
		// /__gokin_external_resource that merely answers with an HTML
		// content-type silently repointed the whole tab: a <link>/<img> to an
		// attacker host is not a document navigation, so the origin gate above
		// is skipped, yet run.target would move to that host while run.origin
		// still named the approved site. Every later request, the approval
		// dialog, and the payload the model sees would then attribute attacker
		// content to the approved domain. Cross-origin navigation must keep
		// going through writeNavigationPage / validateExternalBrowserApproval.
		if _, upstreamOrigin, normalizeErr := normalizeExternalBrowserURL(upstream.String()); normalizeErr == nil && upstreamOrigin == run.origin {
			run.mu.Lock()
			run.target = upstream
			run.browserURL = localBrowserURL(run, upstream, false)
			run.mu.Unlock()
		}
	} else if strings.Contains(contentType, "text/css") {
		data = []byte(run.rewriteCSS(string(data), upstream))
	}
	copyExternalBrowserResponseHeaders(writer.Header(), response.Header)
	if localRedirect != "" {
		writer.Header().Set("Location", localRedirect)
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", externalBrowserCSP(run.bridgeToken, run.activeScripts))
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(data)
}

func (run *externalBrowserRun) upstreamURL(incoming *url.URL) (*url.URL, error) {
	if incoming.Path == "/__gokin_external_resource" {
		encoded := incoming.Query().Get("u")
		signature := incoming.Query().Get("s")
		data, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid external resource URL")
		}
		expected := run.externalResourceSignature(string(data))
		if signature == "" || subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) != 1 {
			return nil, fmt.Errorf("invalid external resource signature")
		}
		parsed, _, err := normalizeExternalBrowserURL(string(data))
		return parsed, err
	}
	run.mu.RLock()
	target := *run.target
	run.mu.RUnlock()
	target.Path = incoming.Path
	target.RawPath = incoming.RawPath
	target.RawQuery = incoming.RawQuery
	target.Fragment = ""
	return &target, nil
}

func externalBrowserDocumentNavigation(request *http.Request) bool {
	destination := strings.ToLower(strings.TrimSpace(request.Header.Get("Sec-Fetch-Dest")))
	mode := strings.ToLower(strings.TrimSpace(request.Header.Get("Sec-Fetch-Mode")))
	return mode == "navigate" || destination == "document" || destination == "iframe" || destination == "frame"
}

func copyExternalBrowserRequestHeaders(destination, source http.Header) {
	for key, values := range source {
		switch strings.ToLower(key) {
		case "connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade", "cookie", "host", "origin", "referer", "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest":
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func copyExternalBrowserResponseHeaders(destination, source http.Header) {
	for key, values := range source {
		switch strings.ToLower(key) {
		case "connection", "keep-alive", "transfer-encoding", "upgrade", "set-cookie", "content-length", "content-encoding", "content-security-policy", "content-security-policy-report-only", "x-frame-options", "report-to", "nel", "location", "refresh", "link":
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func externalBrowserCSP(nonce string, activeScripts bool) string {
	scriptPolicy := "script-src 'nonce-" + nonce + "'; script-src-attr 'none'"
	if activeScripts {
		scriptPolicy = "script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; script-src-attr 'unsafe-inline'"
	}
	return "default-src 'self' data: blob:; base-uri 'self'; object-src 'none'; " + scriptPolicy + "; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'none'; worker-src 'none'; form-action 'self'; navigate-to 'self'"
}

func (run *externalBrowserRun) localPath(target *url.URL) string {
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if target.RawQuery != "" {
		path += "?" + target.RawQuery
	}
	if target.Fragment != "" {
		path += "#" + url.PathEscape(target.Fragment)
	}
	return path
}

func (run *externalBrowserRun) proxyResource(target *url.URL) string {
	copyTarget := *target
	copyTarget.Fragment = ""
	raw := copyTarget.String()
	query := url.Values{}
	query.Set("u", base64.RawURLEncoding.EncodeToString([]byte(raw)))
	query.Set("s", run.externalResourceSignature(raw))
	return "/__gokin_external_resource?" + query.Encode()
}

func (run *externalBrowserRun) externalResourceSignature(raw string) string {
	mac := hmac.New(sha256.New, []byte(run.accessToken))
	_, _ = mac.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (run *externalBrowserRun) rewriteURL(raw string, base *url.URL, navigation bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(strings.ToLower(raw), "data:") || strings.HasPrefix(strings.ToLower(raw), "blob:") || strings.HasPrefix(strings.ToLower(raw), "mailto:") || strings.HasPrefix(strings.ToLower(raw), "tel:") || strings.HasPrefix(strings.ToLower(raw), "javascript:") {
		return raw
	}
	target, err := base.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		return raw
	}
	_, origin, err := normalizeExternalBrowserURL(target.String())
	if err != nil {
		return raw
	}
	if origin == run.origin {
		return run.localPath(target)
	}
	if navigation {
		return target.String()
	}
	return run.proxyResource(target)
}

var cssURLPattern = regexp.MustCompile(`(?i)url\(\s*(['"]?)([^)'"\s]+)['"]?\s*\)`)

func (run *externalBrowserRun) rewriteCSS(css string, base *url.URL) string {
	return cssURLPattern.ReplaceAllStringFunc(css, func(match string) string {
		parts := cssURLPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return "url(" + parts[1] + run.rewriteURL(parts[2], base, false) + parts[1] + ")"
	})
}

func (run *externalBrowserRun) rewriteHTML(data []byte, base *url.URL) ([]byte, error) {
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var head *html.Node
	activeScripts := run.activeScripts
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			name := strings.ToLower(node.Data)
			// Platforms without a native subframe navigation policy cannot safely
			// execute arbitrary page code: location changes could bypass the local
			// SSRF proxy. macOS confines every frame natively, so normal page scripts
			// may run there. Embedded browsing contexts remain removed everywhere.
			if name == "script" && !activeScripts {
				node.Data = "template"
				node.Attr = nil
				name = "template"
			}
			if name == "iframe" || name == "frame" || name == "object" || name == "embed" || name == "applet" {
				node.Data = "span"
				node.Attr = []html.Attribute{{Key: "hidden", Val: ""}}
				name = "span"
			}
			if name == "head" {
				head = node
			}
			filtered := node.Attr[:0]
			for _, attr := range node.Attr {
				key := strings.ToLower(attr.Key)
				if (!activeScripts && strings.HasPrefix(key, "on")) || key == "srcdoc" {
					continue
				}
				if name == "meta" && key == "http-equiv" && strings.EqualFold(attr.Val, "refresh") {
					continue
				}
				if key == "integrity" || key == "nonce" {
					continue
				}
				if key == "style" {
					attr.Val = run.rewriteCSS(attr.Val, base)
				}
				navigation := (name == "a" && (key == "href" || key == "ping")) || (name == "form" && key == "action") || (name == "area" && key == "href")
				if key == "href" || key == "src" || key == "action" || key == "poster" || key == "data" {
					if !activeScripts && strings.HasPrefix(strings.ToLower(strings.TrimSpace(attr.Val)), "javascript:") {
						attr.Val = "#"
					} else {
						attr.Val = run.rewriteURL(attr.Val, base, navigation)
					}
				}
				if key == "srcset" {
					candidates := strings.Split(attr.Val, ",")
					for index, candidate := range candidates {
						fields := strings.Fields(strings.TrimSpace(candidate))
						if len(fields) > 0 {
							fields[0] = run.rewriteURL(fields[0], base, false)
							candidates[index] = strings.Join(fields, " ")
						}
					}
					attr.Val = strings.Join(candidates, ", ")
				}
				if name == "base" && key == "href" {
					attr.Val = "/"
				}
				filtered = append(filtered, attr)
			}
			node.Attr = filtered
			if name == "style" && node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
				node.FirstChild.Data = run.rewriteCSS(node.FirstChild.Data, base)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	if head == nil {
		head = document
	}
	script := &html.Node{Type: html.ElementNode, Data: "script", Attr: []html.Attribute{{Key: "data-gokin-browser-bridge", Val: "true"}, {Key: "nonce", Val: run.bridgeToken}}}
	script.AppendChild(&html.Node{Type: html.TextNode, Data: externalBrowserBridgeScript(run.id, run.bridgeToken, run.origin)})
	if head.FirstChild != nil {
		head.InsertBefore(script, head.FirstChild)
	} else {
		head.AppendChild(script)
	}
	var output bytes.Buffer
	if err := html.Render(&output, document); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func externalBrowserBridgeScript(tabID, token, upstreamOrigin string) string {
	config, _ := json.Marshal(map[string]string{"tabID": tabID, "token": token, "origin": upstreamOrigin})
	return `(function(){
const c=` + string(config) + `;
const send=(type,p={})=>parent.postMessage({type,token:c.token,tabID:c.tabID,...p},"*");
const publicURL=()=>c.origin+location.pathname+location.search+location.hash;
const absolute=v=>{try{return new URL(v,c.origin+location.pathname)}catch{return null}};
const local=u=>u.pathname+u.search+u.hash;
const issues=[];
const issue=(kind,value)=>{let text="";try{text=typeof value==="string"?value:String(value&&value.message||value)}catch{text="Unknown page error"}issues.push({kind,text:text.slice(0,2000),time:Date.now()});if(issues.length>100)issues.shift()};
addEventListener("error",e=>issue("error",e.error||e.message));
addEventListener("unhandledrejection",e=>issue("unhandledrejection",e.reason));
const nativeConsoleError=console.error.bind(console);console.error=(...values)=>{issue("console.error",values.map(value=>{try{return typeof value==="string"?value:JSON.stringify(value)}catch{return String(value)}}).join(" "));return nativeConsoleError(...values)};
document.addEventListener("click",e=>{const a=e.target&&e.target.closest&&e.target.closest("a[href]");if(!a)return;const u=absolute(a.getAttribute("href"));if(!u||!/^https?:$/.test(u.protocol))return;e.preventDefault();if(u.origin===c.origin)location.href=local(u);else send("gokin-external-navigation",{url:u.href})},true);
document.addEventListener("submit",e=>{const f=e.target;if(!f||!f.action)return;const u=absolute(f.action);if(u&&u.origin!==c.origin){e.preventDefault();send("gokin-external-navigation",{url:u.href})}},true);
window.open=v=>{const u=absolute(String(v||""));if(u){if(u.origin===c.origin)location.href=local(u);else send("gokin-external-navigation",{url:u.href})}return null};
const nativeFetch=window.fetch;window.fetch=(input,init)=>{try{const raw=typeof input==="string"||input instanceof URL?String(input):input.url;const u=absolute(raw);if(u&&/^https?:$/.test(u.protocol)){if(u.origin!==c.origin)return Promise.reject(new TypeError("Cross-origin page fetch is blocked by the isolated Browser proxy"));const mapped=local(u);if(input instanceof Request)input=new Request(mapped,input);else input=mapped}}catch(error){return Promise.reject(error)}return nativeFetch.call(window,input,init)};
const nativeXHROpen=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(method,raw,...rest){const u=absolute(String(raw));if(u&&/^https?:$/.test(u.protocol)){if(u.origin!==c.origin)throw new DOMException("Cross-origin page request is blocked by the isolated Browser proxy","SecurityError");raw=local(u)}return nativeXHROpen.call(this,method,raw,...rest)};
const report=()=>send("gokin-external-ready",{url:publicURL(),title:document.title});
new MutationObserver(report).observe(document.querySelector("title")||document.documentElement,{childList:true,subtree:true,characterData:true});
addEventListener("popstate",report);addEventListener("hashchange",report);
const visible=el=>{if(!(el instanceof Element))return false;const style=getComputedStyle(el);const rect=el.getBoundingClientRect();return style.display!=="none"&&style.visibility!=="hidden"&&Number(style.opacity)!==0&&rect.width>0&&rect.height>0&&rect.bottom>=0&&rect.right>=0&&rect.top<=innerHeight&&rect.left<=innerWidth};
const describe=el=>{const rect=el.getBoundingClientRect();const label=el.getAttribute("aria-label")||el.getAttribute("title")||el.getAttribute("placeholder")||"";return {tag:el.tagName.toLowerCase(),role:el.getAttribute("role")||"",type:el.getAttribute("type")||"",name:el.getAttribute("name")||"",id:el.id||"",text:(label||el.innerText||el.value||"").trim().slice(0,500),disabled:Boolean(el.disabled||el.getAttribute("aria-disabled")==="true"),rect:{x:Math.round(rect.x),y:Math.round(rect.y),width:Math.round(rect.width),height:Math.round(rect.height)}}};
const screenshot=async()=>{try{const width=Math.max(1,Math.min(innerWidth,1600));const height=Math.max(1,Math.min(innerHeight,1200));const clone=document.documentElement.cloneNode(true);clone.querySelectorAll('script[data-gokin-browser-bridge]').forEach(node=>node.remove());const source=new XMLSerializer().serializeToString(clone);const svg='<svg xmlns="http://www.w3.org/2000/svg" width="'+width+'" height="'+height+'"><foreignObject width="100%" height="100%">'+source+'</foreignObject></svg>';const image=new Image();const loaded=new Promise((resolve,reject)=>{image.onload=resolve;image.onerror=reject});image.src="data:image/svg+xml;charset=utf-8,"+encodeURIComponent(svg);await loaded;const canvas=document.createElement("canvas");canvas.width=width;canvas.height=height;canvas.getContext("2d").drawImage(image,0,0);const data=canvas.toDataURL("image/png");return data.length<=5592400?data:""}catch(error){issue("screenshot",error);return ""}};
const snapshot=async withScreenshot=>{const controls=Array.from(document.querySelectorAll('a[href],button,input,textarea,select,[role="button"],[role="link"],[tabindex]')).filter(visible).slice(0,300).map(describe);const headings=Array.from(document.querySelectorAll('h1,h2,h3,h4,h5,h6,[role="heading"]')).filter(visible).slice(0,100).map(describe);const payload={url:publicURL(),title:String(document.title||"").slice(0,500),readyState:document.readyState,viewport:{width:innerWidth,height:innerHeight,devicePixelRatio:devicePixelRatio||1},text:String(document.body&&document.body.innerText||"").slice(0,50000),controls,headings,issues:issues.slice(-100),capturedAt:Date.now()};if(withScreenshot)payload.screenshotDataURL=await screenshot();return payload};
const fail=(requestId,message)=>send("gokin-external-result",{requestId,payload:{error:String(message).slice(0,2000),capturedAt:Date.now()}});
const dispatchKey=(target,key)=>{const names={ENTER:"Enter",TAB:"Tab",ESCAPE:"Escape",SPACE:" ",ARROWDOWN:"ArrowDown",ARROWUP:"ArrowUp"};const value=names[key];if(!value)throw new Error("Unsupported key");target.dispatchEvent(new KeyboardEvent("keydown",{key:value,bubbles:true,cancelable:true}));target.dispatchEvent(new KeyboardEvent("keyup",{key:value,bubbles:true,cancelable:true}))};
addEventListener("message",async event=>{const data=event.data;if(event.source!==parent||!data||data.type!=="gokin-external-command"||data.token!==c.token||typeof data.requestId!=="string")return;event.stopImmediatePropagation();const requestId=data.requestId;const args=data.args||{};const action=String(args.action||"").toLowerCase();try{if(action!=="inspect"&&String(args.expected_url||"")!==publicURL())throw new Error("The page URL changed; inspect it again before acting.");if(action==="inspect"){send("gokin-external-result",{requestId,payload:await snapshot(args.screenshot!==false)});return}if(action==="scroll"){scrollBy({top:Number(args.deltaY)||0,behavior:"instant"});await new Promise(resolve=>setTimeout(resolve,60));const payload=await snapshot(false);payload.actionResult="Scrolled the active page";send("gokin-external-result",{requestId,payload});return}if(action==="fill"){const el=document.elementFromPoint(Number(args.x),Number(args.y));if(!visible(el)||!(el.matches('input:not([type="hidden"]),textarea,[contenteditable="true"]')))throw new Error("The approved coordinates no longer point to an editable visible field.");el.focus();if(el.isContentEditable){el.textContent=String(args.text||"")}else{const setter=Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el),"value")?.set;setter?setter.call(el,String(args.text||"")):el.value=String(args.text||"")}el.dispatchEvent(new InputEvent("input",{bubbles:true,inputType:"insertText",data:String(args.text||"")}));el.dispatchEvent(new Event("change",{bubbles:true}));const payload=await snapshot(false);payload.actionResult="Filled "+describe(el).tag+" at the approved coordinates";send("gokin-external-result",{requestId,payload});return}if(action==="click"){const el=document.elementFromPoint(Number(args.x),Number(args.y));if(!visible(el))throw new Error("The approved coordinates no longer point to a visible element.");const target=describe(el);const payload=await snapshot(false);payload.actionResult="Dispatched an approved click on "+target.tag+(target.text?": "+target.text.slice(0,120):"");send("gokin-external-result",{requestId,payload});setTimeout(()=>el.click(),0);return}if(action==="key"){const target=document.activeElement||document.body;const payload=await snapshot(false);payload.actionResult="Dispatched approved key "+String(args.key||"").toUpperCase();send("gokin-external-result",{requestId,payload});setTimeout(()=>dispatchKey(target,String(args.key||"").toUpperCase()),0);return}throw new Error("Unsupported external browser action")}catch(error){fail(requestId,error&&error.message||error)}});
report();
})();`
}

func (run *externalBrowserRun) writeNavigationPage(writer http.ResponseWriter, target string) {
	payload, _ := json.Marshal(target)
	body := `<!doctype html><meta charset="utf-8"><title>Domain approval required</title><style>body{font:14px system-ui;padding:32px;color:#ddd;background:#171717}code{word-break:break-all}</style><h1>Domain approval required</h1><p>This page wants to navigate to <code></code>.</p><script nonce="` + stdhtml.EscapeString(run.bridgeToken) + `">const u=` + string(payload) + `;document.querySelector("code").textContent=u;parent.postMessage({type:"gokin-external-navigation",token:` + strconv.Quote(run.bridgeToken) + `,tabID:` + strconv.Quote(run.id) + `,url:u},"*")</script>`
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", externalBrowserCSP(run.bridgeToken, run.activeScripts))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, body)
}

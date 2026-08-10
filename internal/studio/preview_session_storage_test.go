package studio

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPreviewSessionProfilePersistsBoundedDataAndIsolatesSessions(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	profile, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	snapshot := previewStorageSnapshot{
		LocalStorage: map[string]string{"theme": "dark", "draft": "hello"},
		Cookies:      "session=secret; preference=compact",
	}
	if err := profile.saveBrowserSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	status := reloaded.status()
	if !status.Enabled || !status.HasData || status.LocalStorageEntries != 2 || status.Cookies != 2 {
		t.Fatalf("unexpected persisted status: %+v", status)
	}
	bootstrap := reloaded.bootstrap()
	if bootstrap.LocalStorage["theme"] != "dark" || bootstrap.LocalStorage["draft"] != "hello" {
		t.Fatalf("localStorage was not restored: %#v", bootstrap.LocalStorage)
	}
	info, err := os.Stat(previewSessionProfilePath("project", "chat", "web"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v, %v", info, err)
	}
	other, err := loadPreviewSessionProfile("project", "other-chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if other.status().HasData || other.status().Enabled {
		t.Fatalf("preview data leaked across sessions: %+v", other.status())
	}
	if err := reloaded.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reloaded.path); !os.IsNotExist(err) {
		t.Fatalf("disabled profile remained on disk: %v", err)
	}
}

func TestPreviewSessionProfileRejectsOversizedLocalStorage(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	profile, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	tooMany := make(map[string]string, previewSessionLocalStorageMaxItems+1)
	for index := 0; index <= previewSessionLocalStorageMaxItems; index++ {
		tooMany[string(rune(index+1))] = "value"
	}
	if err := profile.saveBrowserSnapshot(previewStorageSnapshot{LocalStorage: tooMany}); err == nil {
		t.Fatal("accepted too many localStorage entries")
	}
	if err := profile.saveBrowserSnapshot(previewStorageSnapshot{LocalStorage: map[string]string{"huge": strings.Repeat("x", 129<<10)}}); err == nil {
		t.Fatal("accepted oversized localStorage value")
	}
}

func TestPreviewSessionCookiesRespectPathsAndDeletion(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	profile, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.mergeCookies([]*http.Cookie{{Name: "root", Value: "one", Path: "/"}, {Name: "admin", Value: "two", Path: "/admin"}}); err != nil {
		t.Fatal(err)
	}
	if got := profile.requestCookies("/home"); len(got) != 1 || got[0].Name != "root" {
		t.Fatalf("root request cookies = %#v", got)
	}
	if got := profile.requestCookies("/admin/users"); len(got) != 2 {
		t.Fatalf("admin request cookies = %#v", got)
	}
	if got := profile.requestCookies("/administrator"); len(got) != 1 || got[0].Name != "root" {
		t.Fatalf("cookie path crossed a segment boundary: %#v", got)
	}
	if err := profile.mergeCookies([]*http.Cookie{{Name: "admin", Value: "", Path: "/admin", MaxAge: -1}}); err != nil {
		t.Fatal(err)
	}
	if got := profile.requestCookies("/admin/users"); len(got) != 1 || got[0].Name != "root" {
		t.Fatalf("deleted cookie remained: %#v", got)
	}
	if _, err := os.Stat(profile.path); !os.IsNotExist(err) {
		t.Fatalf("ephemeral cookie state was persisted without opt-in: %v", err)
	}
}

func TestSavePreviewBrowserStorageRequiresActiveBridgeToken(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := newStudioForTest(t)
	profile, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	run := &previewServerRun{
		projectID: "project", sessionID: "chat", config: PreviewServerConfiguration{Name: "web"},
		state: "running", bridgeToken: "active-token", profile: profile,
	}
	s.previewServers[previewServerKey("project", "chat", "web")] = run
	payload := `{"localStorage":{"draft":"safe"},"cookies":"session=one"}`

	if err := s.SavePreviewBrowserStorage("project", "chat", "web", "stale-token", payload); err == nil {
		t.Fatal("accepted a snapshot from a stale bridge token")
	}
	if status := profile.status(); status.HasData {
		t.Fatalf("rejected bridge mutated the profile: %+v", status)
	}
	if err := s.SavePreviewBrowserStorage("project", "chat", "web", "active-token", payload); err != nil {
		t.Fatalf("active bridge snapshot was rejected: %v", err)
	}
	if status := profile.status(); !status.HasData || status.LocalStorageEntries != 1 || status.Cookies != 1 {
		t.Fatalf("active bridge snapshot was not persisted: %+v", status)
	}
	run.mu.Lock()
	run.state = "stopped"
	run.mu.Unlock()
	if err := s.SavePreviewBrowserStorage("project", "chat", "web", "active-token", payload); err == nil {
		t.Fatal("accepted a snapshot after the preview stopped")
	}
}

func TestPreviewBridgeProxyRestoresCookiesStorageAndUsesUniqueBrowserOrigin(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	var upstreamCookie string
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCookie = request.Header.Get("Cookie")
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.SetCookie(response, &http.Cookie{Name: "fresh", Value: "yes", Path: "/", HttpOnly: true})
		_, _ = io.WriteString(response, "<html><head></head><body>fixture</body></html>")
	}))
	defer target.Close()
	profile, err := loadPreviewSessionProfile("project", "chat", "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := profile.saveBrowserSnapshot(previewStorageSnapshot{LocalStorage: map[string]string{"token": "local"}, Cookies: "session=restored"}); err != nil {
		t.Fatal(err)
	}
	run := &previewServerRun{targetURL: target.URL, bridgeToken: "AbCdEfGhIjKlMnOpQrStUvWx", profile: profile}
	if err := startPreviewBridgeProxy(run); err != nil {
		t.Fatal(err)
	}
	defer run.proxy.Close()
	response, err := http.Get(run.proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(upstreamCookie, "session=restored") {
		t.Fatalf("persisted cookie did not reach target: %q", upstreamCookie)
	}
	if !strings.Contains(string(body), `"token":"local"`) || !strings.Contains(string(body), "gokin-preview-storage") {
		t.Fatalf("storage bootstrap missing from bridge: %s", body)
	}
	// The browser origin must be a loopback IP literal. A "<token>.localhost"
	// host looks more isolated but macOS App Transport Security rejects it
	// (-1022) because only bare `localhost` and loopback literals are exempt,
	// so the pane never loaded in the shipped WebView. Isolation now comes from
	// the per-run ephemeral port, which is what defines a web origin for
	// localStorage/sessionStorage/IndexedDB. (Cookies ignore the port, so they
	// are shared across loopback ports — the run's own server-side jar, not the
	// browser jar, is what keeps upstream cookies separated.)
	if !strings.HasPrefix(run.browserURL, "http://127.0.0.1:") {
		t.Fatalf("browser origin must be an ATS-exempt loopback literal: %q", run.browserURL)
	}
	if strings.Contains(run.browserURL, ".localhost") {
		t.Fatalf("browser origin regressed to an ATS-blocked host: %q", run.browserURL)
	}
	if status := profile.status(); status.Cookies != 2 {
		t.Fatalf("upstream cookie was not persisted: %+v", status)
	}
}

func TestRemovePreviewSessionProfilesUsesExactProjectSessionPrefix(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	for _, ids := range [][3]string{{"project", "chat", "web"}, {"project", "other", "web"}, {"project-two", "chat", "web"}} {
		profile, err := loadPreviewSessionProfile(ids[0], ids[1], ids[2])
		if err != nil {
			t.Fatal(err)
		}
		if err := profile.setEnabled(true); err != nil {
			t.Fatal(err)
		}
	}
	removePreviewSessionProfiles("project", "chat")
	if _, err := os.Stat(previewSessionProfilePath("project", "chat", "web")); !os.IsNotExist(err) {
		t.Fatalf("target session profile remained: %v", err)
	}
	for _, ids := range [][3]string{{"project", "other", "web"}, {"project-two", "chat", "web"}} {
		if _, err := os.Stat(previewSessionProfilePath(ids[0], ids[1], ids[2])); err != nil {
			t.Fatalf("unrelated profile was removed: %v", err)
		}
	}
	removePreviewSessionProfiles("project", "")
	if _, err := os.Stat(previewSessionProfilePath("project", "other", "web")); !os.IsNotExist(err) {
		t.Fatalf("project profile remained: %v", err)
	}
	if _, err := os.Stat(previewSessionProfilePath("project-two", "chat", "web")); err != nil {
		t.Fatalf("similarly prefixed project was removed: %v", err)
	}
}

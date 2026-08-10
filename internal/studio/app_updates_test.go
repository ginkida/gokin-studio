package studio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseReleaseVersion(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"v1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.3", true},
		{"  v12.0.41  ", "12.0.41", true},
		{"v1.2", "", false},
		{"v1.2.3.4", "", false},
		{"v1.02.3", "", false},
		{"v1.2.3-rc.1", "", false},
		{"v1.2.x", "", false},
		{"https://example.com/v1.2.3", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, _, err := parseReleaseVersion(tt.raw)
			if (err == nil) != tt.ok {
				t.Fatalf("parseReleaseVersion(%q) error = %v, ok=%v", tt.raw, err, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("parseReleaseVersion(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"2.0.0", "1.99.99", 1},
		{"1.3.0", "1.2.9", 1},
		{"1.2.1", "1.2.0", 1},
		{"1.2.0", "1.2.0", 0},
		{"1.1.99", "1.2.0", -1},
	} {
		_, a, _ := parseReleaseVersion(tt.a)
		_, b, _ := parseReleaseVersion(tt.b)
		if got := compareReleaseVersions(a, b); got != tt.want {
			t.Fatalf("compareReleaseVersions(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestUpdateHTTPClientRedirectPolicy(t *testing.T) {
	client := updateHTTPClient(NewStudio())
	if client.CheckRedirect == nil {
		t.Fatal("production update client has no redirect policy")
	}
	trusted, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/ginkida/gokin-studio/releases/latest", nil)
	if err := client.CheckRedirect(trusted, nil); err != nil {
		t.Fatalf("trusted redirect rejected: %v", err)
	}
	for _, rawURL := range []string{
		"http://api.github.com/repos/ginkida/gokin-studio/releases/latest",
		"https://api.github.com.evil.example/releases/latest",
		"https://api.github.com:444/releases/latest",
		"https://github.com/ginkida/gokin-studio/releases/latest",
	} {
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		if err := client.CheckRedirect(req, nil); err == nil {
			t.Fatalf("untrusted redirect accepted: %s", rawURL)
		}
	}
}

// newerThanCurrent derives a release tag that is always ahead of the shipped
// Version, so a version bump can never silently turn this fixture into a
// "no update available" case (which is exactly what a hardcoded v1.3.0 did
// when Version moved to 2.0.0).
func newerThanCurrent(t *testing.T) string {
	t.Helper()
	major, _, ok := strings.Cut(Version, ".")
	if !ok {
		t.Fatalf("unexpected Version format %q", Version)
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		t.Fatalf("unexpected Version major in %q: %v", Version, err)
	}
	return fmt.Sprintf("%d.0.0", n+1)
}

func TestCheckForUpdatesNewReleasePersistsAndEmits(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	published := "2026-08-01T10:30:00Z"
	latest := newerThanCurrent(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "Gokin-Studio/"+Version {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = fmt.Fprintf(w, `{"tag_name":"v%s","html_url":"https://evil.example/update","draft":false,"prerelease":false,"published_at":%q}`, latest, published)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	s := newUpdateTestStudio(server, now)
	var emitted UpdateStatus
	s.testUpdateEmitter = func(status UpdateStatus) { emitted = status }

	status, err := s.CheckForUpdates()
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if !status.Available || status.CurrentVersion != Version || status.LatestVersion != latest {
		t.Fatalf("status = %+v", status)
	}
	if status.ReleaseURL != "https://github.com/ginkida/gokin-studio/releases/tag/v"+latest {
		t.Fatalf("trusted release URL = %q", status.ReleaseURL)
	}
	if emitted.LatestVersion != latest || !emitted.Available {
		t.Fatalf("emitted = %+v", emitted)
	}
	if status.PublishedAt != time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("publishedAt = %d", status.PublishedAt)
	}

	cached := s.GetUpdateStatus()
	if *cached != *status {
		t.Fatalf("cached = %+v, want %+v", cached, status)
	}
	info, err := os.Stat(updateStatePath())
	if err != nil {
		t.Fatalf("stat update state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("update state mode = %o", info.Mode().Perm())
	}
}

func TestUpdateCheckDailyCacheAndManualForce(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `{"tag_name":"v1.2.0","draft":false,"prerelease":false,"published_at":"2026-08-01T00:00:00Z"}`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	s := newUpdateTestStudio(server, now)
	first, err := s.CheckForUpdatesIfDue()
	if err != nil || first.Available {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	if _, err := s.CheckForUpdatesIfDue(); err != nil {
		t.Fatalf("cached check: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("daily requests = %d, want 1", got)
	}
	if _, err := s.CheckForUpdates(); err != nil {
		t.Fatalf("manual check: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("manual requests = %d, want 2", got)
	}

	s.testUpdateNow = func() time.Time { return now.Add(25 * time.Hour) }
	if _, err := s.CheckForUpdatesIfDue(); err != nil {
		t.Fatalf("next-day check: %v", err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("next-day requests = %d, want 3", got)
	}
}

func TestAutomaticUpdateCheckCanBeDisabled(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	s := newUpdateTestStudio(server, time.Now())
	s.config.Settings.AutoUpdateCheckDisabled = true
	status, err := s.CheckForUpdatesIfDue()
	if err != nil || status.CurrentVersion != Version {
		t.Fatalf("disabled status = %+v, err=%v", status, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("disabled automatic check made %d requests", requests.Load())
	}
	if _, err := s.CheckForUpdates(); err != nil {
		t.Fatalf("manual check while automatic disabled: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("manual check made %d requests", requests.Load())
	}
}

func TestUpdateCheckNoPublishedRelease(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	s := newUpdateTestStudio(server, time.Now())
	status, err := s.CheckForUpdates()
	if err != nil {
		t.Fatalf("CheckForUpdates: %v", err)
	}
	if status.Available || status.LatestVersion != "" || status.CheckedAt == 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestUpdateCheckRejectsUntrustedResponses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error", http.StatusInternalServerError, `{}`, "HTTP 500"},
		{"prerelease", http.StatusOK, `{"tag_name":"v1.3.0","prerelease":true}`, "non-stable"},
		{"draft", http.StatusOK, `{"tag_name":"v1.3.0","draft":true}`, "non-stable"},
		{"bad tag", http.StatusOK, `{"tag_name":"v1.3.0-rc.1"}`, "invalid release tag"},
		{"bad json", http.StatusOK, `{`, "decode update response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			s := newUpdateTestStudio(server, time.Now())
			_, err := s.CheckForUpdates()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if _, statErr := os.Stat(updateStatePath()); !os.IsNotExist(statErr) {
				t.Fatalf("untrusted response wrote cache: %v", statErr)
			}
		})
	}
}

func TestUpdateCheckRejectsOversizedResponse(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", updateResponseMaxBytes+1))
	}))
	defer server.Close()
	s := newUpdateTestStudio(server, time.Now())
	_, err := s.CheckForUpdates()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestInvalidUpdateCacheIsReplacedBySuccessfulCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOKIN_CONFIG_DIR", dir)
	if err := os.WriteFile(updateStatePath(), []byte(`{"schema":1,"latestVersion":"../../evil"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	s := newUpdateTestStudio(server, time.Now())
	status, err := s.CheckForUpdatesIfDue()
	if err != nil {
		t.Fatalf("replace invalid cache: %v", err)
	}
	if status.Available || status.ReleaseURL != "" {
		t.Fatalf("status = %+v", status)
	}
}

func newUpdateTestStudio(server *httptest.Server, now time.Time) *Studio {
	s := NewStudio()
	s.ctx = context.Background()
	s.config = defaultConfig()
	s.testUpdateEndpoint = server.URL
	s.testUpdateHTTPClient = server.Client()
	s.testUpdateNow = func() time.Time { return now }
	return s
}

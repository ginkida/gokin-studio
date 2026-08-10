package studio

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGetBuildInfo(t *testing.T) {
	s := NewStudio()
	bi := s.GetBuildInfo()
	if bi.Version != Version {
		t.Fatalf("Version=%q, want %q", bi.Version, Version)
	}
	if bi.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion=%q, want %q", bi.GoVersion, runtime.Version())
	}
	if bi.OS != runtime.GOOS {
		t.Fatalf("OS=%q, want %q", bi.OS, runtime.GOOS)
	}
	if bi.Arch != runtime.GOARCH {
		t.Fatalf("Arch=%q, want %q", bi.Arch, runtime.GOARCH)
	}
	if bi.NumCPU < 1 {
		t.Fatalf("NumCPU=%d, want >=1", bi.NumCPU)
	}
}

func TestGetDiagnostics_BasicShape(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	d := s.GetDiagnostics()
	if d == nil {
		t.Fatal("GetDiagnostics returned nil")
	}
	if d.Build.Version == "" {
		t.Error("Build.Version is empty")
	}
	if d.ConfigDir == "" {
		t.Error("ConfigDir is empty")
	}
	if d.GeneratedAtMs == 0 {
		t.Error("GeneratedAtMs not populated")
	}
	if len(d.Checks) == 0 {
		t.Error("Checks slice is empty — expected at least config-dir check")
	}
}

func TestGetDiagnostics_TotalsReflectProjects(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	// Add 2 projects, each with default session + 1 extra.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	p1, err := s.AddProject("P1", dir1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.AddProject("P2", dir2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChatSession(p1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChatSession(p2.ID); err != nil {
		t.Fatal(err)
	}

	d := s.GetDiagnostics()
	if d.TotalProjects != 2 {
		t.Errorf("TotalProjects=%d, want 2", d.TotalProjects)
	}
	if d.TotalSessions != 4 { // 2 defaults + 2 extras
		t.Errorf("TotalSessions=%d, want 4", d.TotalSessions)
	}
}

func TestHealthCheck_ConfigDirWritable(t *testing.T) {
	dir := withTempHistoryDir(t) // parent (configDir) is the temp dir
	parentDir := filepath.Dir(dir)
	check := checkConfigDirWritable(parentDir)
	if check.Status != "ok" {
		t.Errorf("status=%q, want ok (msg=%s)", check.Status, check.Message)
	}
	if check.Category != "config" {
		t.Errorf("category=%q, want config", check.Category)
	}
}

func TestHealthCheck_ConfigDirMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope-does-not-exist")
	check := checkConfigDirWritable(missing)
	if check.Status != "warn" {
		t.Errorf("status=%q, want warn", check.Status)
	}
	if !strings.Contains(check.Message, "does not exist") {
		t.Errorf("message=%q, expected mentions 'does not exist'", check.Message)
	}
}

func TestHealthCheck_ConfigDirIsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := checkConfigDirWritable(path)
	if check.Status != "error" {
		t.Errorf("status=%q, want error", check.Status)
	}
	if !strings.Contains(check.Message, "not a directory") {
		t.Errorf("message=%q, expected mentions 'not a directory'", check.Message)
	}
}

func TestHealthCheck_APIKeys_MissingKeyFlagged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.GLMKey = "" // no key
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = p

	d := s.GetDiagnostics()
	foundErr := false
	for _, c := range d.Checks {
		if c.Category == "providers" && strings.Contains(c.Name, "GLM") && c.Status == "error" {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Errorf("expected error check for missing GLM API key, got checks: %+v", d.Checks)
	}
}

func TestHealthCheck_APIKeys_KeyPresentIsOK(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.GLMKey = "sk-test-key"
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = p

	d := s.GetDiagnostics()
	foundOK := false
	for _, c := range d.Checks {
		if c.Category == "providers" && strings.Contains(c.Name, "GLM") && c.Status == "ok" {
			foundOK = true
			break
		}
	}
	if !foundOK {
		t.Errorf("expected OK check for GLM API key, got: %+v", d.Checks)
	}
}

func TestHealthCheck_ProjectDir_Missing(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()

	// Add project, then delete its directory.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "child")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := s.AddProject("P", subdir)
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}

	d := s.GetDiagnostics()
	foundErr := false
	for _, c := range d.Checks {
		if c.Category == "projects" && c.Status == "error" {
			foundErr = true
			if !strings.Contains(c.Detail, "P") {
				t.Errorf("Detail should mention project name; got %q", c.Detail)
			}
			break
		}
	}
	if !foundErr {
		t.Errorf("expected error check for missing project dir, got: %+v", d.Checks)
	}
}

func TestHealthCheck_StaleReplays_Detected(t *testing.T) {
	histDir := withTempHistoryDir(t)
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a fresh replay (should not count) and a stale one (8 days old).
	fresh := filepath.Join(histDir, "fresh.replay.jsonl")
	stale := filepath.Join(histDir, "stale.replay.jsonl")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	count := countStaleReplays(histDir, 7*24*time.Hour)
	if count != 1 {
		t.Errorf("staleReplays=%d, want 1", count)
	}
}

func TestHealthCheck_StaleReplays_NoneWhenAllFresh(t *testing.T) {
	histDir := withTempHistoryDir(t)
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(histDir, "fresh.replay.jsonl")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if count := countStaleReplays(histDir, 7*24*time.Hour); count != 0 {
		t.Errorf("staleReplays=%d, want 0", count)
	}
}

func TestDiagnosticsReport_ContainsKeyFields(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.GLMKey = "sk-test"
	p, err := s.AddProject("ReportTest", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = p

	report := s.DiagnosticsReport()
	for _, want := range []string{
		"Gokin Studio Diagnostics",
		"Version: " + Version,
		"Go:      " + runtime.Version(),
		"Storage",
		"Health checks",
		"[OK]",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q\n--- full report ---\n%s", want, report)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1500, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.00 GB"},
	}
	for _, c := range cases {
		got := humanBytes(c.n)
		if got != c.want {
			t.Errorf("humanBytes(%d)=%q, want %q", c.n, got, c.want)
		}
	}
}

func TestInspectConfigDir_MissingReturnsFalse(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	ok, size := inspectConfigDir(missing)
	if ok {
		t.Error("ok=true for missing dir, want false")
	}
	if size != 0 {
		t.Errorf("size=%d for missing dir, want 0", size)
	}
}

func TestInspectConfigDir_RealDirOK(t *testing.T) {
	dir := t.TempDir()
	// Put a small file in it.
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, size := inspectConfigDir(dir)
	if !ok {
		t.Error("ok=false for writable dir, want true")
	}
	if size < 5 {
		t.Errorf("size=%d, want >=5 (data.txt is 5 bytes)", size)
	}
}

func TestInspectHistoryDir_ReturnsStats(t *testing.T) {
	dir := t.TempDir()
	// Write a fresh history file + a stale replay.
	if err := os.WriteFile(filepath.Join(dir, "p_default.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "p_default.replay.jsonl")
	if err := os.WriteFile(stale, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	total, staleCount := inspectHistoryDir(dir)
	if total < 7 {
		t.Errorf("total=%d, want >=7", total)
	}
	if staleCount != 1 {
		t.Errorf("staleCount=%d, want 1", staleCount)
	}
}

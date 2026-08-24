package studio

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// crossByte60Name builds a name whose total length exceeds the 60-byte cap with
// a multibyte rune straddling byte index 60. 58 ASCII 'a' (bytes 0-57) + CJK
// runes (3 bytes each starting at byte 58) means byte 60 lands in the MIDDLE of
// the first CJK rune — so a raw name[:60] cut would split it and yield invalid
// UTF-8. truncateUTF8 must back up to the rune boundary instead.
func crossByte60Name() string {
	return strings.Repeat("a", 58) + "中文字汉字" // 58 + 5*3 = 73 bytes
}

// TestProjectName_RuneSafeTruncationPersistsValidUTF8 is the regression for the
// audit finding: project-name caps used byte slicing, so a name with a rune
// across byte 60 was stored as invalid UTF-8 — and config.yaml (yaml.v3) then
// serialized it as a !!binary blob, corrupting the display name. The fix routes
// every name cap through truncateUTF8.
func TestProjectName_RuneSafeTruncationPersistsValidUTF8(t *testing.T) {
	s := newStudioForTest(t)

	info, err := s.AddProject(crossByte60Name(), t.TempDir())
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if !utf8.ValidString(info.Name) {
		t.Errorf("AddProject persisted invalid UTF-8 name: %q", info.Name)
	}
	// The cap moved from bytes to characters so it matches the maxLength={60}
	// the name inputs enforce; the rune-safety property this test exists for is
	// unchanged, and a rune cap cannot split a rune by construction.
	if n := utf8.RuneCountInString(info.Name); n > DisplayNameMaxRunes {
		t.Errorf("name not capped to %d characters: got %d", DisplayNameMaxRunes, n)
	}

	// The raw config.yaml must NOT contain a !!binary node (that's the symptom
	// of an invalid-UTF-8 string reaching yaml.v3).
	raw, err := os.ReadFile(configPath())
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if strings.Contains(string(raw), "!!binary") {
		t.Errorf("config.yaml contains a !!binary node — name was stored as invalid UTF-8:\n%s", raw)
	}

	// Round-trip via LoadConfig: the reloaded name stays valid UTF-8.
	cfg := LoadConfig()
	found := false
	for _, p := range cfg.Projects {
		if p.ID == info.ID {
			found = true
			if !utf8.ValidString(p.Name) {
				t.Errorf("config round-trip produced invalid UTF-8 name: %q", p.Name)
			}
		}
	}
	if !found {
		t.Error("project not found in reloaded config")
	}
}

// TestRenameProject_RuneSafeTruncation covers the rename path (also persists to
// config.yaml).
func TestRenameProject_RuneSafeTruncation(t *testing.T) {
	s := newStudioForTest(t)
	info, err := s.AddProject("p", t.TempDir())
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := s.RenameProject(info.ID, crossByte60Name()); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !utf8.ValidString(got.Name) {
		t.Errorf("RenameProject persisted invalid UTF-8 name: %q", got.Name)
	}
	raw, _ := os.ReadFile(configPath())
	if strings.Contains(string(raw), "!!binary") {
		t.Errorf("config.yaml contains !!binary after rename:\n%s", raw)
	}
}

// TestImportProjectJSON_RuneSafeTruncation covers the attacker-supplied-name
// import surface (project_export.go / session_export.go).
func TestImportSession_RuneSafeName(t *testing.T) {
	if utf8.ValidString(crossByte60Name()[:60]) {
		t.Skip("test premise invalid: byte slice unexpectedly valid at this boundary")
	}
	// The import paths cap display names by character now, so assert the helper
	// they actually call.
	got := truncateRunes(crossByte60Name(), DisplayNameMaxRunes)
	if !utf8.ValidString(got) {
		t.Errorf("truncateRunes produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > DisplayNameMaxRunes {
		t.Errorf("truncateRunes returned %d characters, want at most %d", n, DisplayNameMaxRunes)
	}
	// truncateUTF8 still guards byte-bounded payloads; keep its rune safety pinned.
	if !utf8.ValidString(truncateUTF8(crossByte60Name(), 60)) {
		t.Error("truncateUTF8 produced invalid UTF-8")
	}
}

package studio

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The composer inputs for project and session names all carry maxLength={60},
// which the browser counts in characters. The backend normalized the same
// fields with truncateUTF8(name, 60), which counts BYTES. For Latin text the
// two agree; for anything else they do not. A Cyrillic name is 2 bytes per
// character, so the UI accepted 60 characters and the backend silently kept 30
// — cutting the name mid-word with nothing to indicate it happened. CJK is 3
// bytes per character, so it kept 20.
const sixtyRuneCyrillicName = "Рефакторинг платежного бэкенда и миграция схемы данных Проек"

func TestFixtureIsSixtyRunesAndOverSixtyBytes(t *testing.T) {
	if got := utf8.RuneCountInString(sixtyRuneCyrillicName); got != 60 {
		t.Fatalf("fixture is %d runes, want exactly the UI limit of 60", got)
	}
	if len(sixtyRuneCyrillicName) <= 60 {
		t.Fatalf("fixture is %d bytes; it must exceed 60 to exercise the mismatch", len(sixtyRuneCyrillicName))
	}
}

func TestAddProjectKeepsSixtyCharacterName(t *testing.T) {
	s := newStudioForTest(t)
	info, err := s.AddProject(sixtyRuneCyrillicName, t.TempDir())
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if info.Name != sixtyRuneCyrillicName {
		t.Errorf("name = %q (%d runes), want the full %d-rune name the UI allowed",
			info.Name, utf8.RuneCountInString(info.Name), utf8.RuneCountInString(sixtyRuneCyrillicName))
	}
}

// A name that survives creation must also survive the next app start: config
// normalization re-truncates every project name on load, so a byte limit there
// would eat the name on restart even if creation kept it.
func TestProjectNameSurvivesConfigReload(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.AddProject(sixtyRuneCyrillicName, t.TempDir()); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	s.saveConfig()
	cfg := LoadConfig()
	if len(cfg.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(cfg.Projects))
	}
	if cfg.Projects[0].Name != sixtyRuneCyrillicName {
		t.Errorf("reloaded name = %q (%d runes), want the full name",
			cfg.Projects[0].Name, utf8.RuneCountInString(cfg.Projects[0].Name))
	}
}

func TestRenameChatSessionKeepsSixtyCharacterName(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "rename-runes")
	sessions, err := s.ListChatSessions(info.ID)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("ListChatSessions: %v (%d sessions)", err, len(sessions))
	}
	if err := s.RenameChatSession(info.ID, sessions[0].ID, sixtyRuneCyrillicName); err != nil {
		t.Fatalf("RenameChatSession: %v", err)
	}
	after, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if after[0].Name != sixtyRuneCyrillicName {
		t.Errorf("session name = %q (%d runes), want the full name",
			after[0].Name, utf8.RuneCountInString(after[0].Name))
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under the limit is untouched", "abc", 10, "abc"},
		{"exactly at the limit is untouched", "abcde", 5, "abcde"},
		{"ascii cuts by character", "abcdef", 5, "abcde"},
		{"cyrillic cuts by character, not byte", "привет мир", 6, "привет"},
		{"cjk cuts by character", "你好世界你好", 4, "你好世界"},
		// A skin-tone emoji is base + modifier, i.e. two runes, so a rune limit
		// can land between them; the result must still be valid UTF-8.
		{"astral emoji counts per rune", "👍👍👍", 2, "👍👍"},
		{"skin-tone emoji splits at the rune boundary", "👍🏽👍🏽", 2, "👍🏽"},
		{"zero max yields empty", "привет", 0, ""},
		{"negative max yields empty", "привет", -1, ""},
		{"empty input stays empty", "", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
		})
	}
}

// Whatever the limit, a truncated name must never end mid-rune — the failure
// truncateUTF8 was originally written to prevent.
func TestTruncateRunesNeverSplitsARune(t *testing.T) {
	long := strings.Repeat("я", 200) + strings.Repeat("世", 200) + strings.Repeat("👍", 50)
	for max := 0; max <= 460; max++ {
		got := truncateRunes(long, max)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateRunes(..., %d) produced invalid UTF-8", max)
		}
		if n := utf8.RuneCountInString(got); n > max {
			t.Fatalf("truncateRunes(..., %d) returned %d runes", max, n)
		}
	}
}

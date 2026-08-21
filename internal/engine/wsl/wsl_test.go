package wsl

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseWindowsPathAcceptsBothUNCSpellings(t *testing.T) {
	cases := []struct {
		in     string
		distro string
		linux  string
		host   string
	}{
		{`\\wsl.localhost\Ubuntu\home\me\proj`, "Ubuntu", "/home/me/proj", "wsl.localhost"},
		{`\\wsl$\Ubuntu\home\me\proj`, "Ubuntu", "/home/me/proj", "wsl$"},
		// Go, the shell and users all disagree about slash direction.
		{`//wsl.localhost/Ubuntu/home/me/proj`, "Ubuntu", "/home/me/proj", "wsl.localhost"},
		{`\\WSL.LOCALHOST\Ubuntu-22.04\srv`, "Ubuntu-22.04", "/srv", "wsl.localhost"},
		// The distro root is a legitimate target.
		{`\\wsl.localhost\Debian`, "Debian", "/", "wsl.localhost"},
		{`\\wsl.localhost\Debian\`, "Debian", "/", "wsl.localhost"},
		// Lexical cleanup, without escaping the root.
		{`\\wsl.localhost\Ubuntu\home\..\..\etc`, "Ubuntu", "/etc", "wsl.localhost"},
		{`\\wsl.localhost\Ubuntu\home\.\me\\proj`, "Ubuntu", "/home/me/proj", "wsl.localhost"},
	}
	for _, tc := range cases {
		got, ok := ParseWindowsPath(tc.in)
		if !ok {
			t.Fatalf("ParseWindowsPath(%q) did not recognise a WSL path", tc.in)
		}
		if got.Distro != tc.distro || got.LinuxPath != tc.linux || got.Host != tc.host {
			t.Fatalf("ParseWindowsPath(%q) = %+v, want distro=%q linux=%q host=%q",
				tc.in, got, tc.distro, tc.linux, tc.host)
		}
	}
}

func TestParseWindowsPathRejectsNonWSLPaths(t *testing.T) {
	for _, in := range []string{
		`C:\Users\me\proj`,
		`/home/me/proj`,
		`\\server\share\proj`,   // an ordinary UNC share
		`\\wsl.localhost`,       // the share list, not a filesystem
		`\\wsl.localhost\`,      // still no distro
		`\\wsl.localhostevil\x`, // must not match on prefix alone
		``,
		`wsl.localhost\Ubuntu\home`, // missing the UNC prefix
	} {
		if loc, ok := ParseWindowsPath(in); ok {
			t.Fatalf("ParseWindowsPath(%q) wrongly matched: %+v", in, loc)
		}
	}
}

// A round-trip must preserve the spelling the user chose; silently rewriting
// \\wsl$\ to \\wsl.localhost\ would change paths stored in their config.
func TestWindowsPathRoundTripPreservesHostSpelling(t *testing.T) {
	for _, in := range []string{
		`\\wsl.localhost\Ubuntu\home\me\proj`,
		`\\wsl$\Ubuntu\home\me\proj`,
		`\\wsl.localhost\Debian`,
	} {
		loc, ok := ParseWindowsPath(in)
		if !ok {
			t.Fatalf("not parsed: %q", in)
		}
		if got := loc.WindowsPath(); !strings.EqualFold(got, in) {
			t.Fatalf("round trip of %q = %q", in, got)
		}
	}
}

func TestWindowsDriveToLinuxMapsAutomount(t *testing.T) {
	cases := map[string]string{
		`C:\Users\me`:     "/mnt/c/Users/me",
		`c:\Users\me`:     "/mnt/c/Users/me",
		`D:\`:             "/mnt/d",
		`C:/Users/me/x`:   "/mnt/c/Users/me/x",
		`C:\Users\..\tmp`: "/mnt/c/tmp",
	}
	for in, want := range cases {
		got, ok := WindowsDriveToLinux(in)
		if !ok || got != want {
			t.Fatalf("WindowsDriveToLinux(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{`\\wsl.localhost\Ubuntu\home`, `/home/me`, ``, `1:\x`} {
		if got, ok := WindowsDriveToLinux(in); ok {
			t.Fatalf("WindowsDriveToLinux(%q) wrongly matched: %q", in, got)
		}
	}
}

// The command must reach the distro's own toolchain, and repo content must not
// be able to smuggle a flag into wsl.exe itself.
// Without --exec, wsl.exe hands everything to the user's DEFAULT login shell,
// which re-parses the script before bash -lc sees it. The bash tool's payload
// contains "$workspace_root", "$(pwd -P)", $#, $@ and $? — all of which would
// be expanded by that intermediate shell against the wrong environment.
func TestLoginShellArgsUsesExecSoNoIntermediateShellReparses(t *testing.T) {
	args := LoginShellArgs("Ubuntu", "/home/me/proj", "npm run build")
	want := []string{"-d", "Ubuntu", "--cd", "/home/me/proj", "--exec", "bash", "-lc", "npm run build"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("LoginShellArgs = %#v, want %#v", args, want)
	}
	execAt := -1
	for i, arg := range args {
		if arg == "--exec" {
			execAt = i
			break
		}
	}
	if execAt < 0 || args[execAt+1] != "bash" {
		t.Fatalf("--exec must immediately precede bash: %#v", args)
	}
	// nvm/pyenv/asdf live in login profiles, so the shell must still be a login
	// shell even though no intermediate shell re-parses the script.
	if args[len(args)-2] != "-lc" {
		t.Fatal("a login shell is required for the distro's toolchain to be on PATH")
	}

	// A script that begins like a flag must reach bash verbatim. `wsl
	// --shutdown` would otherwise kill every distro on the machine.
	hostile := LoginShellArgs("Ubuntu", "/x", "--shutdown")
	if got := hostile[len(hostile)-4:]; !reflect.DeepEqual(got, []string{"--exec", "bash", "-lc", "--shutdown"}) {
		t.Fatalf("flag-like script was not isolated: %#v", hostile)
	}

	// A script carrying shell metacharacters must survive as ONE argv entry.
	script := `cd "$workspace_root" && echo "$(pwd -P)" >&2`
	metas := LoginShellArgs("Ubuntu", "/x", script)
	if metas[len(metas)-1] != script {
		t.Fatalf("script was altered: %q", metas[len(metas)-1])
	}
}

func TestLoginShellArgsOmitsEmptyDistroAndDirectory(t *testing.T) {
	args := LoginShellArgs("", "", "echo hi")
	want := []string{"--exec", "bash", "-lc", "echo hi"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("LoginShellArgs = %#v, want %#v", args, want)
	}
}

func TestExecArgsSkipsTheShell(t *testing.T) {
	args := ExecArgs("Ubuntu", "/repo", "git", "status", "--porcelain")
	want := []string{"-d", "Ubuntu", "--cd", "/repo", "--exec", "git", "status", "--porcelain"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ExecArgs = %#v, want %#v", args, want)
	}
}

func TestShellQuoteSurvivesEmbeddedQuotes(t *testing.T) {
	cases := map[string]string{
		`/home/me/proj`:      `'/home/me/proj'`,
		`/home/me/it's here`: `'/home/me/it'\''s here'`,
		`; rm -rf /`:         `'; rm -rf /'`,
		`$(whoami)`:          `'$(whoami)'`,
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Fatalf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// The header row of `wsl -l -v` is localised, so parsing must be positional.
func TestParseDistroListIsLocaleIndependent(t *testing.T) {
	english := "  NAME            STATE           VERSION\n" +
		"* Ubuntu          Running         2\n" +
		"  Debian          Stopped         2\n" +
		"  Legacy          Stopped         1\n"
	got := ParseDistroList(english)
	want := []DistroState{
		{Name: "Ubuntu", Running: true, Version: 2, Default: true},
		{Name: "Debian", Running: false, Version: 2},
		{Name: "Legacy", Running: false, Version: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDistroList(english) = %+v, want %+v", got, want)
	}

	// A non-English header must not be mistaken for a distro.
	russian := "  ИМЯ             СОСТОЯНИЕ       ВЕРСИЯ\n" +
		"* Ubuntu          Running         2\n"
	got = ParseDistroList(russian)
	if len(got) != 1 || got[0].Name != "Ubuntu" || !got[0].Default {
		t.Fatalf("ParseDistroList(russian) = %+v", got)
	}
}

func TestParseDistroListHandlesEmptyAndHeaderOnly(t *testing.T) {
	if got := ParseDistroList(""); len(got) != 0 {
		t.Fatalf("empty output = %+v", got)
	}
	if got := ParseDistroList("  NAME  STATE  VERSION\n"); len(got) != 0 {
		t.Fatalf("header-only output = %+v", got)
	}
}

// wsl.exe writes UTF-16LE; treating it as UTF-8 yields NUL-riddled garbage.
func TestDecodeConsoleOutputHandlesUTF16AndUTF8(t *testing.T) {
	text := "  NAME   STATE   VERSION\n* Ubuntu Running 2\n"
	units := utf16.Encode([]rune(text))
	raw := make([]byte, 0, len(units)*2+2)
	raw = append(raw, 0xFF, 0xFE) // BOM
	for _, unit := range units {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	if got := DecodeConsoleOutput(raw); got != text {
		t.Fatalf("UTF-16 with BOM decoded to %q", got)
	}

	// Without a BOM, the every-other-byte-is-zero shape still gives it away.
	if got := DecodeConsoleOutput(raw[2:]); got != text {
		t.Fatalf("UTF-16 without BOM decoded to %q", got)
	}

	// Plain UTF-8 must pass through untouched, including non-ASCII.
	plain := "Ubuntu Running 2\nДебиан\n"
	if got := DecodeConsoleOutput([]byte(plain)); got != plain {
		t.Fatalf("UTF-8 was mangled: %q", got)
	}
	if got := DecodeConsoleOutput(nil); got != "" {
		t.Fatalf("nil input = %q", got)
	}
}

func TestDecodeThenParseRoundTrip(t *testing.T) {
	text := "  NAME  STATE  VERSION\n* Ubuntu-22.04  Running  2\n"
	units := utf16.Encode([]rune(text))
	raw := []byte{0xFF, 0xFE}
	for _, unit := range units {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	got := ParseDistroList(DecodeConsoleOutput(raw))
	if len(got) != 1 || got[0].Name != "Ubuntu-22.04" || got[0].Version != 2 || !got[0].Default {
		t.Fatalf("round trip = %+v", got)
	}
}

// A distro name reaches an argv entry, never a shell — but a name starting with
// "-" would still be read by wsl.exe as one of its own flags.
func TestValidateDistroNameRejectsFlagsAndSeparators(t *testing.T) {
	for _, bad := range []string{"", "   ", "-d", "--shutdown", `Ubuntu\evil`, "Ubuntu/evil", "a\x00b", "x\nyz", strings.Repeat("u", 65)} {
		if err := ValidateDistroName(bad); err == nil {
			t.Fatalf("ValidateDistroName(%q) accepted it", bad)
		}
	}
	for _, ok := range []string{"Ubuntu", "Ubuntu-22.04", "kali-linux", "openSUSE-Leap-15.5", "Debian"} {
		if err := ValidateDistroName(ok); err != nil {
			t.Fatalf("ValidateDistroName(%q) = %v", ok, err)
		}
	}
}

func TestIsWSLPathMatchesParse(t *testing.T) {
	for _, in := range []string{`\\wsl.localhost\Ubuntu\home`, `C:\x`, `/home/me`, `\\wsl$\D\x`} {
		_, ok := ParseWindowsPath(in)
		if IsWSLPath(in) != ok {
			t.Fatalf("IsWSLPath(%q) disagrees with ParseWindowsPath", in)
		}
	}
}

// `wsl --import "My Distro"` is legal, and `-l -v` pads columns instead of
// delimiting them, so positional parsing of the verbose output splits the name.
// The quiet listing is the authoritative source.
func TestParseDistroNamesHandlesSpacesAndBlankLines(t *testing.T) {
	got := ParseDistroNames("My Distro\r\nUbuntu-24.04\n\n\x00\n")
	want := []string{"My Distro", "Ubuntu-24.04"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDistroNames = %#v, want %#v", got, want)
	}
	if len(ParseDistroNames("")) != 0 {
		t.Fatal("empty output produced names")
	}
}

func TestMergeDistroStatesPrefersQuietNames(t *testing.T) {
	// The verbose row mis-splits "My Distro"; the merge must not adopt that.
	verbose := []DistroState{{Name: "My", Running: true, Version: 2, Default: true}}
	got := MergeDistroStates([]string{"My Distro", "Ubuntu"}, []string{"Ubuntu"}, verbose)
	if len(got) != 2 {
		t.Fatalf("merged = %+v", got)
	}
	if got[0].Name != "My Distro" {
		t.Fatalf("name was corrupted by the verbose row: %q", got[0].Name)
	}
	if got[0].Running {
		t.Fatal("running state must come from the quiet running listing")
	}
	if !got[1].Running || got[1].Name != "Ubuntu" {
		t.Fatalf("Ubuntu = %+v, want running", got[1])
	}
}

func TestMergeDistroStatesAttachesVersionCaseInsensitively(t *testing.T) {
	got := MergeDistroStates(
		[]string{"Ubuntu"}, nil,
		[]DistroState{{Name: "ubuntu", Version: 2, Default: true}},
	)
	if len(got) != 1 || got[0].Version != 2 || !got[0].Default {
		t.Fatalf("merged = %+v", got)
	}
}

// Windows matches UNC shares case-insensitively, but `wsl -d ubuntu` fails for
// a distro registered as "Ubuntu".
func TestReconcileDistroNameIsCaseInsensitive(t *testing.T) {
	if got := ReconcileDistroName("ubuntu", []string{"Ubuntu", "Debian"}); got != "Ubuntu" {
		t.Fatalf("ReconcileDistroName = %q, want Ubuntu", got)
	}
	if got := ReconcileDistroName("Fedora", []string{"Ubuntu"}); got != "Fedora" {
		t.Fatalf("unknown name should pass through, got %q", got)
	}
}

// Identity must collapse every spelling; display must not.
func TestCanonicalFormsCollapseSpellingButDisplayDoesNot(t *testing.T) {
	spellings := []string{
		`\\wsl$\Ubuntu\home\me\p`,
		`\\wsl.localhost\Ubuntu\home\me\p`,
		`\\WSL.LOCALHOST\ubuntu\home\me\p`,
		`//wsl.localhost/Ubuntu/home/me/p`,
	}
	keys := make(map[string]bool)
	for _, in := range spellings {
		keys[CanonicalKey(in)] = true
		canonical, ok := CanonicalWindowsPath(in)
		if !ok {
			t.Fatalf("CanonicalWindowsPath(%q) failed", in)
		}
		if !strings.HasPrefix(canonical, `\\wsl.localhost\`) {
			t.Fatalf("CanonicalWindowsPath(%q) = %q, want the modern spelling", in, canonical)
		}
	}
	if len(keys) != 1 {
		t.Fatalf("one location produced %d identity keys: %v", len(keys), keys)
	}
	// The user's own spelling survives for display and storage.
	location, _ := ParseWindowsPath(`\\wsl$\Ubuntu\home\me\p`)
	if location.WindowsPath() != `\\wsl$\Ubuntu\home\me\p` {
		t.Fatalf("display path was rewritten: %q", location.WindowsPath())
	}
	if _, ok := CanonicalWindowsPath(`C:\Users\me`); ok {
		t.Fatal("a drive path is not a WSL path")
	}
}

func TestJoinArgvQuotesHostileArguments(t *testing.T) {
	got := JoinArgv([]string{"git", "commit", "-m", "it's a \"fix\"; rm -rf /", "$(whoami)", "--"})
	// Every entry stays single-quoted, so nothing is re-interpreted by a shell.
	if strings.Contains(got, "rm -rf /'") && !strings.Contains(got, `'\''`) {
		t.Fatalf("embedded quote was not escaped: %s", got)
	}
	for _, fragment := range []string{`'git'`, `'$(whoami)'`, `'--'`} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("JoinArgv lost %s: %s", fragment, got)
		}
	}
}

func TestParseSupportsCDAgainstHelpFixture(t *testing.T) {
	modern := "Usage: wsl.exe [Argument]\n    --cd <Directory>\n        Sets the specified directory as the current working directory.\n"
	legacy := "Usage: wsl.exe [Argument]\n    --exec, -e <CommandLine>\n"
	if !ParseSupportsCD(modern) {
		t.Fatal("modern wsl.exe help was not recognised as supporting --cd")
	}
	if ParseSupportsCD(legacy) {
		t.Fatal("legacy wsl.exe help was treated as supporting --cd")
	}
}

// wsl.exe returns 1 for its OWN failures, which is indistinguishable from grep
// finding nothing or `git check-ignore` reporting "not ignored".
func TestClassifyWSLFailureSeparatesWSLFaultsFromCommandExitCodes(t *testing.T) {
	cases := []struct {
		code   int
		stderr string
		kind   string
		fault  bool
	}{
		{1, "There is no distribution with the supplied name.", FailureDistroMissing, true},
		{1, "The Windows Subsystem for Linux instance has terminated.", FailureDistroTerminated, true},
		{1, "", "", false}, // grep found nothing
		{1, "fatal: not a git repository", "", false},
		{0, "There is no distribution with the supplied name.", "", false}, // success wins
	}
	for _, tc := range cases {
		kind, fault := ClassifyWSLFailure(tc.code, tc.stderr)
		if kind != tc.kind || fault != tc.fault {
			t.Fatalf("ClassifyWSLFailure(%d, %q) = %q/%v, want %q/%v",
				tc.code, tc.stderr, kind, fault, tc.kind, tc.fault)
		}
	}
	// The same message arriving as UTF-16LE must classify identically.
	utf16Message := DecodeConsoleOutput(encodeUTF16LEForTest("There is no distribution with the supplied name."))
	if kind, _ := ClassifyWSLFailure(1, utf16Message); kind != FailureDistroMissing {
		t.Fatalf("UTF-16 stderr classified as %q", kind)
	}
}

func encodeUTF16LEForTest(text string) []byte {
	raw := []byte{0xFF, 0xFE}
	for _, unit := range utf16.Encode([]rune(text)) {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	return raw
}

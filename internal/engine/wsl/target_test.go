package wsl

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

const fakeExe = `C:\Windows\System32\wsl.exe`

func modernTarget(dir string) Target {
	return Detect("windows", fakeExe, dir, Caps{SupportsCD: true}, []string{"Ubuntu"})
}

// The identity guarantee, stated as a test: off Windows nothing is ever
// retargeted, so macOS and Linux keep byte-identical behaviour.
func TestDetectReturnsHostTargetOffWindows(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		got := Detect(goos, fakeExe, `\\wsl.localhost\Ubuntu\home\me\p`, Caps{SupportsCD: true}, nil)
		if got.IsWSL() {
			t.Fatalf("Detect(%q) routed into WSL: %+v", goos, got)
		}
	}
}

func TestDetectRequiresWSLExecutable(t *testing.T) {
	got := Detect("windows", "", `\\wsl.localhost\Ubuntu\home\me\p`, Caps{}, nil)
	if got.IsWSL() {
		t.Fatalf("Detect with no wsl.exe routed anyway: %+v", got)
	}
}

func TestDetectIgnoresOrdinaryWindowsDirectories(t *testing.T) {
	for _, dir := range []string{`C:\Users\me\proj`, `\\server\share\proj`, ""} {
		if got := modernTarget(dir); got.IsWSL() {
			t.Fatalf("Detect(%q) routed into WSL: %+v", dir, got)
		}
	}
}

func TestDetectAcceptsBothSpellingsAndReconcilesCase(t *testing.T) {
	for _, dir := range []string{
		`\\wsl.localhost\Ubuntu\home\me\p`,
		`\\wsl$\Ubuntu\home\me\p`,
		`//wsl.localhost/ubuntu/home/me/p`,
	} {
		got := modernTarget(dir)
		if !got.IsWSL() {
			t.Fatalf("Detect(%q) did not route into WSL", dir)
		}
		// `wsl -d ubuntu` fails for a distro registered as "Ubuntu".
		if got.Distro != "Ubuntu" {
			t.Fatalf("Detect(%q).Distro = %q, want the registered spelling", dir, got.Distro)
		}
		if got.LinuxDir != "/home/me/p" {
			t.Fatalf("Detect(%q).LinuxDir = %q", dir, got.LinuxDir)
		}
		if got.HostDir != dir {
			t.Fatalf("Detect(%q) lost the original spelling: %q", dir, got.HostDir)
		}
	}
}

func TestDetectRejectsHostileDistroName(t *testing.T) {
	if got := modernTarget(`\\wsl.localhost\-d\home`); got.IsWSL() {
		t.Fatalf("a flag-shaped distro name was accepted: %+v", got)
	}
}

// A host target must leave every caller on the path it already had.
func TestRetargetHostTargetReturnsNotOK(t *testing.T) {
	host := Target{}
	if _, ok := RetargetExec(host, []string{"git", "status"}, nil); ok {
		t.Fatal("RetargetExec rewrote a host command")
	}
	if _, ok := RetargetShell(host, "echo hi", nil); ok {
		t.Fatal("RetargetShell rewrote a host command")
	}
	// Empty payloads are refused rather than producing a bare wsl.exe call.
	wsl := modernTarget(`\\wsl.localhost\Ubuntu\p`)
	if _, ok := RetargetExec(wsl, nil, nil); ok {
		t.Fatal("an empty argv was accepted")
	}
	if _, ok := RetargetShell(wsl, "", nil); ok {
		t.Fatal("an empty script was accepted")
	}
}

func TestRetargetExecGoldenArgvWithCD(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	plan, ok := RetargetExec(target, []string{"git", "status", "--porcelain"}, nil)
	if !ok {
		t.Fatal("not retargeted")
	}
	want := []string{"-d", "Ubuntu", "--cd", "/home/me/repo", "--exec", "git", "status", "--porcelain"}
	if plan.Name != fakeExe || !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("plan = %+v, want args %#v", plan, want)
	}
	// A UNC path is not a legal CreateProcess working directory.
	if plan.Dir != "" {
		t.Fatalf("plan.Dir = %q, must be empty", plan.Dir)
	}
	// cmd.Env would be wsl.exe's Windows environment and never reaches the
	// distro, so setting it would be a lie.
	if plan.Env != nil {
		t.Fatalf("plan.Env = %#v, must be nil", plan.Env)
	}
}

// Legacy inbox WSL has no --cd. Skipping this fallback would silently run every
// command in $HOME, with no error, because cmd.Dir was cleared.
func TestRetargetFallsBackToShellCDOnLegacyWSL(t *testing.T) {
	target := Detect("windows", fakeExe, `\\wsl.localhost\Ubuntu\home\me\repo`, Caps{SupportsCD: false}, []string{"Ubuntu"})
	plan, ok := RetargetExec(target, []string{"git", "status"}, nil)
	if !ok {
		t.Fatal("not retargeted")
	}
	for _, arg := range plan.Args {
		if arg == "--cd" {
			t.Fatalf("--cd used on a wsl.exe that does not support it: %#v", plan.Args)
		}
	}
	last := plan.Args[len(plan.Args)-1]
	if !strings.HasPrefix(last, "cd -- '/home/me/repo' || exit 1\n") {
		t.Fatalf("no directory fallback: %q", last)
	}
	if !strings.Contains(last, `'git' 'status'`) {
		t.Fatalf("argv was not quoted into the fallback script: %q", last)
	}

	shellPlan, _ := RetargetShell(target, "npm test", nil)
	shellLast := shellPlan.Args[len(shellPlan.Args)-1]
	if shellLast != "cd -- '/home/me/repo' || exit 1\nnpm test" {
		t.Fatalf("shell fallback = %q", shellLast)
	}
}

func TestRetargetShellUsesExecBashLoginC(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\p`)
	plan, ok := RetargetShell(target, `echo "$(pwd -P)"`, nil)
	if !ok {
		t.Fatal("not retargeted")
	}
	want := []string{"-d", "Ubuntu", "--cd", "/p", "--exec", "bash", "-lc", `echo "$(pwd -P)"`}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("plan.Args = %#v, want %#v", plan.Args, want)
	}
}

// Injected variables reach the distro through the environment overlay and
// WSLENV, never through argv. Sorted keys keep WSLENV deterministic.
func TestRetargetEnvOverlayIsSortedAndKeptOffArgv(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\p`)
	plan, _ := RetargetShell(target, "printenv", map[string]string{
		"ZED":   "last",
		"ALPHA": "first value",
		"MID":   "has 'quote'",
	})
	// argv carries only the command.
	want := []string{"-d", "Ubuntu", "--cd", "/p", "--exec", "bash", "-lc", "printenv"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("plan.Args = %#v, want %#v", plan.Args, want)
	}
	joined := strings.Join(plan.EnvOverlay, "\x00")
	for _, entry := range []string{"ALPHA=first value", "MID=has 'quote'", "ZED=last", "WSLENV=ALPHA/u:MID/u:ZED/u"} {
		if !strings.Contains(joined, entry) {
			t.Fatalf("overlay missing %q: %#v", entry, plan.EnvOverlay)
		}
	}
}

func TestRetargetRejectsHostileDistroName(t *testing.T) {
	for _, distro := range []string{"-d", `Ub\untu`, "a\x00b", ""} {
		target := Target{Distro: distro, LinuxDir: "/p", Exe: fakeExe, Caps: Caps{SupportsCD: true}}
		if _, ok := RetargetExec(target, []string{"git"}, nil); ok {
			t.Fatalf("hostile distro %q was accepted", distro)
		}
	}
}

func TestIsRemotePathIsStructuralAndWindowsOnly(t *testing.T) {
	if !IsRemotePath("windows", `\\wsl.localhost\Ubuntu\p`) {
		t.Fatal("a WSL path on Windows is remote")
	}
	if IsRemotePath("darwin", `\\wsl.localhost\Ubuntu\p`) {
		t.Fatal("nothing is remote off Windows")
	}
	if IsRemotePath("windows", `C:\p`) {
		t.Fatal("a drive path is not remote")
	}
}

func TestLinuxPathForTranslatesWithinTargetOnly(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	if got, ok := LinuxPathFor(target, `\\wsl.localhost\Ubuntu\home\me\repo\src`); !ok || got != "/home/me/repo/src" {
		t.Fatalf("same-distro path = %q, %v", got, ok)
	}
	// A Windows drive is reachable through the automount.
	if got, ok := LinuxPathFor(target, `C:\Users\me\notes.md`); !ok || got != "/mnt/c/Users/me/notes.md" {
		t.Fatalf("drive path = %q, %v", got, ok)
	}
	// Another distro's filesystem is NOT reachable from this one.
	if got, ok := LinuxPathFor(target, `\\wsl.localhost\Debian\home\me`); ok {
		t.Fatalf("a foreign distro path was translated: %q", got)
	}
	if _, ok := LinuxPathFor(Target{}, `C:\x`); ok {
		t.Fatal("a host target translated a path")
	}
}

// Preserving the UNC spelling matters: filepath.Rel and VolumeName comparisons
// would otherwise straddle two different volume names for one location.
func TestHostPathForPreservesTargetSpelling(t *testing.T) {
	legacy := Detect("windows", fakeExe, `\\wsl$\Ubuntu\home\me\repo`, Caps{SupportsCD: true}, []string{"Ubuntu"})
	got, ok := HostPathFor(legacy, "/home/me/repo/src")
	if !ok || got != `\\wsl$\Ubuntu\home\me\repo\src` {
		t.Fatalf("HostPathFor = %q, %v", got, ok)
	}
	modern := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	got, _ = HostPathFor(modern, "/home/me/repo/src")
	if got != `\\wsl.localhost\Ubuntu\home\me\repo\src` {
		t.Fatalf("HostPathFor = %q", got)
	}
	if _, ok := HostPathFor(modern, "relative/path"); ok {
		t.Fatal("a relative path was accepted")
	}
}

func TestApplyExecRewritesCommandFields(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	cmd := exec.Command("git", "status")
	cmd.Dir = `\\wsl.localhost\Ubuntu\home\me\repo`
	if !ApplyExec(cmd, target, []string{"git", "status"}, nil) {
		t.Fatal("ApplyExec did not retarget")
	}
	if cmd.Path != fakeExe {
		t.Fatalf("cmd.Path = %q", cmd.Path)
	}
	if cmd.Args[0] != fakeExe || cmd.Args[1] != "-d" || cmd.Args[2] != "Ubuntu" {
		t.Fatalf("cmd.Args = %#v", cmd.Args)
	}
	// Dir must be cleared: a UNC path is not a legal CreateProcess working
	// directory, and --cd carries the real one.
	if cmd.Dir != "" {
		t.Fatalf("cmd.Dir = %q, must be cleared", cmd.Dir)
	}
	// Env must NOT be cleared: wsl.exe needs a real Windows environment
	// (SystemRoot and friends) to start. The overlay pins WSLENV instead.
	if len(cmd.Env) == 0 {
		t.Fatal("cmd.Env was cleared; wsl.exe may not start without a Windows environment")
	}
}

// exec.Command records a lookup failure in cmd.Err, and Start() returns it even
// after Path is repointed — so on Windows, where "bash" usually is not found,
// every retargeted command would fail while pointing at wsl.exe.
func TestApplyClearsLookPathError(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\p`)
	cmd := exec.Command("definitely-not-a-real-binary-xyz")
	if cmd.Err == nil {
		t.Skip("this platform resolved the fake binary; the guard is Windows-specific")
	}
	if !ApplyShell(cmd, target, "echo hi", nil) {
		t.Fatal("ApplyShell did not retarget")
	}
	if cmd.Err != nil {
		t.Fatalf("cmd.Err survived retargeting: %v", cmd.Err)
	}
}

// The machine-checked form of "macOS is unaffected".
func TestApplyHostTargetLeavesCommandIdentical(t *testing.T) {
	cmd := exec.Command("git", "status")
	cmd.Dir = "/home/me/repo"
	cmd.Env = []string{"A=1"}
	beforePath, beforeArgs := cmd.Path, append([]string(nil), cmd.Args...)
	beforeDir, beforeEnv := cmd.Dir, append([]string(nil), cmd.Env...)

	if ApplyExec(cmd, Target{}, []string{"git", "status"}, nil) {
		t.Fatal("ApplyExec claimed to retarget a host command")
	}
	if ApplyShell(cmd, Target{}, "git status", nil) {
		t.Fatal("ApplyShell claimed to retarget a host command")
	}
	if cmd.Path != beforePath || !reflect.DeepEqual(cmd.Args, beforeArgs) ||
		cmd.Dir != beforeDir || !reflect.DeepEqual(cmd.Env, beforeEnv) {
		t.Fatalf("a host command was mutated: %+v", cmd)
	}
}

// git is invoked in four different shapes across the codebase — `-C dir` in the
// argv, cmd.Dir, with and without `-c` safety flags. One helper must handle all
// of them identically, so a new call site cannot pick a subtly different form.
func TestRetargetGitRewritesDashCAndWorkingDirectory(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)

	// Shape 1: -C in the argv, no cmd.Dir.
	plan, ok := RetargetGit(target, "", []string{
		"git", "-c", "core.hooksPath=/dev/null", "-C", `\\wsl.localhost\Ubuntu\home\me\repo`, "status",
	})
	if !ok {
		t.Fatal("shape 1 not retargeted")
	}
	joined := strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "-C /home/me/repo") {
		t.Fatalf("-C was not translated: %s", joined)
	}
	if strings.Contains(joined, `wsl.localhost`) {
		t.Fatalf("a Windows path survived into the distro argv: %s", joined)
	}
	if !strings.Contains(joined, "core.hooksPath=/dev/null") {
		t.Fatalf("the safety flags were dropped: %s", joined)
	}

	// Shape 2: cmd.Dir, no -C. The directory becomes --cd.
	plan, ok = RetargetGit(target, `\\wsl.localhost\Ubuntu\home\me\repo`, []string{"git", "status"})
	if !ok {
		t.Fatal("shape 2 not retargeted")
	}
	if !reflect.DeepEqual(plan.Args, []string{
		"-d", "Ubuntu", "--cd", "/home/me/repo", "--exec", "git", "status",
	}) {
		t.Fatalf("shape 2 args = %#v", plan.Args)
	}
}

// Running git against the wrong directory silently is worse than not running
// it, so an unreachable path refuses instead of falling back.
func TestRetargetGitRefusesPathsOutsideTheDistro(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	if _, ok := RetargetGit(target, "", []string{
		"git", "-C", `\\wsl.localhost\Debian\home\me`, "status",
	}); ok {
		t.Fatal("a foreign distro path was accepted")
	}
	if _, ok := RetargetGit(Target{}, "/repo", []string{"git", "status"}); ok {
		t.Fatal("a host target was retargeted")
	}
	if _, ok := RetargetGit(target, "", nil); ok {
		t.Fatal("an empty argv was accepted")
	}
}

// A Windows drive path reaches the distro through the automount, so a git call
// against C:\... from a WSL project still resolves.
func TestRetargetGitTranslatesWindowsDriveThroughAutomount(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	plan, ok := RetargetGit(target, `C:\Users\me\other`, []string{"git", "status"})
	if !ok {
		t.Fatal("not retargeted")
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "--cd /mnt/c/Users/me/other") {
		t.Fatalf("drive path was not automounted: %#v", plan.Args)
	}
}

// `exec` takes ONE command and replaces the shell, so a multi-line payload lost
// everything after its first line — and every bash payload here IS multi-line,
// because wrapCommandWithPWD appends the exit-code and pwd probe. `exec cd src`
// also fails outright, cd being a builtin.
func TestLegacyFallbackRunsTheWholeScriptNotJustItsFirstCommand(t *testing.T) {
	legacy := Detect("windows", fakeExe, `\\wsl.localhost\Ubuntu\home\me\repo`,
		Caps{SupportsCD: false}, []string{"Ubuntu"})
	script := "ls -la\n__gokin_rc=$?; echo marker$(pwd); exit $__gokin_rc"
	plan, ok := RetargetShell(legacy, script, nil)
	if !ok {
		t.Fatal("not retargeted")
	}
	wrapped := plan.Args[len(plan.Args)-1]
	if strings.Contains(wrapped, "exec ") {
		t.Fatalf("exec would discard everything after the first command:\n%s", wrapped)
	}
	if !strings.HasPrefix(wrapped, "cd -- '/home/me/repo' || exit 1\n") {
		t.Fatalf("directory guard is wrong:\n%s", wrapped)
	}
	// The whole script must survive, including the probe that tracks pwd.
	if !strings.HasSuffix(wrapped, script) {
		t.Fatalf("the payload was truncated:\n%s", wrapped)
	}
	// A failed cd must not fall through into the rest of the script.
	if strings.Contains(wrapped, "&&") {
		t.Fatalf("&& binds only to the first line, so a failed cd would still run the rest:\n%s", wrapped)
	}
}

// git's path-taking -C is valid only BEFORE the subcommand. Treating a
// subcommand's -C as a path made LinuxPathFor fail, which aborted retargeting
// and silently ran Windows git against the UNC share.
func TestRetargetGitOnlyTranslatesLeadingDashC(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)

	// `git grep -C 3 pattern`: the -C is grep's context count, not a path.
	plan, ok := RetargetGit(target, `\\wsl.localhost\Ubuntu\home\me\repo`,
		[]string{"git", "grep", "-C", "3", "needle"})
	if !ok {
		t.Fatal("a subcommand -C aborted retargeting; the command would have run Windows git")
	}
	joined := strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "grep -C 3 needle") {
		t.Fatalf("the subcommand's own -C was rewritten: %s", joined)
	}

	// A leading -C is still translated, including after other global options.
	plan, ok = RetargetGit(target, "", []string{
		"git", "-c", "core.hooksPath=/dev/null", "-C", `\\wsl.localhost\Ubuntu\home\me\repo`, "log", "-C",
	})
	if !ok {
		t.Fatal("a leading -C was not retargeted")
	}
	joined = strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "-C /home/me/repo") {
		t.Fatalf("the leading -C was not translated: %s", joined)
	}
	// `git log -C` (detect copies) must survive untouched.
	if !strings.HasSuffix(joined, "log -C") {
		t.Fatalf("log's own -C was disturbed: %s", joined)
	}
}

// "C:" is the per-drive current directory and "C:foo" is relative to it —
// neither is C:\ or C:\foo, so fabricating a rooted path would name a different
// directory inside the distro.
func TestWindowsDriveToLinuxRejectsDriveRelativePaths(t *testing.T) {
	for _, in := range []string{"C:", "C:foo", "c:bar\\baz"} {
		if got, ok := WindowsDriveToLinux(in); ok {
			t.Fatalf("WindowsDriveToLinux(%q) = %q; a drive-relative path must fail closed", in, got)
		}
	}
	// Rooted forms still work.
	for in, want := range map[string]string{`C:\Users\me`: "/mnt/c/Users/me", "C:/x": "/mnt/c/x", `D:\`: "/mnt/d"} {
		if got, ok := WindowsDriveToLinux(in); !ok || got != want {
			t.Fatalf("WindowsDriveToLinux(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

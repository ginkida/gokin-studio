package wsl

import (
	"sort"
	"strings"
)

// The retargeting layer: given a project directory, decide whether work belongs
// inside a distro, and rewrite a command so it lands there.
//
// The single most important decision here is that Detect takes `goos` and the
// wsl.exe path as PARAMETERS rather than reading runtime.GOOS and probing the
// machine. That makes the Windows branch exercisable from a macOS test, which
// matters because this code cannot otherwise be run by whoever writes it.

// Caps records what this machine's wsl.exe can do.
type Caps struct {
	// SupportsCD is false on the legacy inbox WSL component, which predates
	// the --cd flag. Without it the working directory has to be set by the
	// shell instead, and skipping that fallback would silently run every
	// command in $HOME.
	SupportsCD bool
}

// Target says where a command should run.
type Target struct {
	Distro   string // "" means the host: run exactly as before
	LinuxDir string // absolute path of the working directory inside the distro
	HostDir  string // the original Windows spelling, kept for display and identity
	Exe      string // absolute path to wsl.exe
	Caps     Caps
}

// IsWSL reports whether commands for this target must be routed into a distro.
func (t Target) IsWSL() bool { return t.Distro != "" && t.Exe != "" }

// Detect decides the target for a project directory.
//
// It returns the host target — meaning "change nothing" — unless the platform
// is Windows, wsl.exe exists, and the directory really is inside a distro.
func Detect(goos, exe, dir string, caps Caps, known []string) Target {
	if goos != "windows" || exe == "" {
		return Target{}
	}
	location, ok := ParseWindowsPath(dir)
	if !ok {
		return Target{}
	}
	distro := ReconcileDistroName(location.Distro, known)
	if ValidateDistroName(distro) != nil {
		return Target{}
	}
	return Target{
		Distro:   distro,
		LinuxDir: location.LinuxPath,
		HostDir:  dir,
		Exe:      exe,
		Caps:     caps,
	}
}

// IsRemotePath is the structural question — "does this path live inside a
// distro?" — independent of whether wsl.exe is installed. Used for decisions
// that must hold even when routing is impossible, such as refusing to create a
// Git worktree for a Linux repo on the Windows drive.
func IsRemotePath(goos, dir string) bool {
	return goos == "windows" && IsWSLPath(dir)
}

// Plan mirrors the four fields of an *exec.Cmd that retargeting rewrites.
// Args excludes argv[0], matching exec.Command(name, args...).
type Plan struct {
	Name string
	Args []string
	Dir  string
	Env  []string
	// EnvOverlay are entries to layer over the inherited Windows environment.
	// apply() merges them, because composing them needs os.Environ().
	EnvOverlay []string
}

// RetargetExec routes an exact argument vector into the distro with no shell.
// Used for git, where the argv is already precise and a login shell would only
// add startup cost.
func RetargetExec(t Target, argv []string, inject map[string]string) (Plan, bool) {
	if len(argv) == 0 {
		return Plan{}, false
	}
	return retarget(t, inject, argv, JoinArgv(argv))
}

// RetargetShell routes a POSIX script into the distro's login shell, so the
// user's nvm/pyenv/asdf toolchain resolves exactly as it does in their terminal.
func RetargetShell(t Target, script string, inject map[string]string) (Plan, bool) {
	if script == "" {
		return Plan{}, false
	}
	return retarget(t, inject, []string{"bash", "-lc", script}, script)
}

// retarget builds the wsl.exe invocation. payloadArgv is the exact vector to
// exec; payloadScript is the same work expressed as a shell string, needed only
// for the legacy no---cd fallback.
func retarget(t Target, inject map[string]string, payloadArgv []string, payloadScript string) (Plan, bool) {
	// The identity guarantee: a host target changes nothing, so macOS and Linux
	// keep byte-identical behaviour.
	if !t.IsWSL() {
		return Plan{}, false
	}
	if ValidateDistroName(t.Distro) != nil {
		return Plan{}, false
	}

	args := []string{"-d", t.Distro}
	useShellCD := t.LinuxDir != "" && !t.Caps.SupportsCD
	if t.LinuxDir != "" && t.Caps.SupportsCD {
		args = append(args, "--cd", t.LinuxDir)
	}
	args = append(args, "--exec")

	if useShellCD {
		// Legacy wsl.exe has no --cd, and cmd.Dir cannot help because a UNC
		// path is not a legal CreateProcess working directory. Without this the
		// command would run in $HOME with no error at all.
		script := payloadScript
		if len(payloadArgv) > 0 && payloadScript == "" {
			script = JoinArgv(payloadArgv)
		}
		// NOT `cd … && exec <script>`. `exec` takes ONE command and replaces
		// the shell, so every line after the first would never run — and every
		// bash payload here is multi-line, because wrapCommandWithPWD appends
		// the exit-code and pwd probe. `exec cd src` would also fail outright,
		// since cd is a builtin. `&&` is wrong for the same reason: it binds
		// only to the first line, so a failed cd would still run the rest in
		// $HOME. A separate guarded statement plus a newline is the only form
		// that is correct for both a single command and a script.
		args = append(args, "bash", "-lc",
			"cd -- "+ShellQuote(t.LinuxDir)+" || exit 1\n"+script)
	} else {
		args = append(args, payloadArgv...)
	}

	return Plan{
		Name: t.Exe,
		Args: args,
		// A UNC path is not a legal working directory for CreateProcess, and
		// the real directory is already carried by --cd or the cd fallback.
		Dir: "",
		// nil means "inherit", which wsl.exe needs in order to start at all
		// (SystemRoot and friends). apply() layers EnvOverlay on top.
		Env:        nil,
		EnvOverlay: EnvOverlay(inject),
	}, true
}

// leakedHostSecrets are variables the app itself may hold that must never be
// handed to wsl.exe. The host command path builds its environment from an
// ALLOWLIST (security.safeEnvironment), so these never reach an agent command
// there; inheriting the parent environment for wsl.exe would quietly undo that.
var leakedHostSecrets = []string{
	"GLM_API_KEY", "KIMI_API_KEY",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
}

// EnvOverlay builds the entries layered over wsl.exe's inherited Windows
// environment.
//
// Variables reach the distro through WSLENV — the mechanism Microsoft provides
// for exactly this — rather than through an `env -- K=V` argv prefix. On Windows
// a process's command line is readable by other processes, and these values are
// the user's Keychain/DPAPI secrets, so keeping them out of argv matters.
//
// WSLENV is also set unconditionally when injecting, which closes the reverse
// hole: whatever the user had in WSLENV is replaced, so no OTHER variable of the
// app's environment can cross the boundary by that route.
func EnvOverlay(inject map[string]string) []string {
	overlay := make([]string, 0, len(inject)+len(leakedHostSecrets)+1)
	// Blank the app's own credentials for wsl.exe itself, matching what the
	// host path's allowlist achieves by omission.
	for _, name := range leakedHostSecrets {
		overlay = append(overlay, name+"=")
	}
	if len(inject) == 0 {
		// Still pin WSLENV so a user-set value cannot ferry anything across.
		return append(overlay, "WSLENV=")
	}
	keys := make([]string, 0, len(inject))
	for key := range inject {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	shared := make([]string, 0, len(keys))
	for _, key := range keys {
		overlay = append(overlay, key+"="+inject[key])
		// "/u" shares the value only Windows-to-WSL, never back.
		shared = append(shared, key+"/u")
	}
	return append(overlay, "WSLENV="+strings.Join(shared, ":"))
}

// MergeEnv layers overlay entries over base, last-wins, comparing names
// case-insensitively because Windows environment names are case-insensitive.
func MergeEnv(base, overlay []string) []string {
	if len(overlay) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	out := make([]string, 0, len(base)+len(overlay))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		index[strings.ToUpper(name)] = len(out)
		out = append(out, entry)
	}
	for _, entry := range overlay {
		name, _, _ := strings.Cut(entry, "=")
		key := strings.ToUpper(name)
		if at, ok := index[key]; ok {
			out[at] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	return out
}

// LinuxPathFor translates a Windows path into the distro's view of it, for a
// path that travels as a command argument. Paths inside the same distro map
// directly; paths on a Windows drive map through /mnt.
func LinuxPathFor(t Target, hostPath string) (string, bool) {
	if !t.IsWSL() || hostPath == "" {
		return "", false
	}
	if location, ok := ParseWindowsPath(hostPath); ok {
		// A path in a DIFFERENT distro is not reachable from this one.
		if !equalFoldASCII(location.Distro, t.Distro) {
			return "", false
		}
		return location.LinuxPath, true
	}
	return WindowsDriveToLinux(hostPath)
}

// HostPathFor translates a Linux path back to Windows, preserving the UNC
// spelling this target was created with so comparisons never straddle two
// different VolumeNames.
func HostPathFor(t Target, linuxPath string) (string, bool) {
	if !t.IsWSL() || linuxPath == "" || linuxPath[0] != '/' {
		return "", false
	}
	host := hostModern
	if location, ok := ParseWindowsPath(t.HostDir); ok {
		host = location.Host
	}
	return Location{Distro: t.Distro, LinuxPath: cleanLinuxPath(linuxPath), Host: host}.WindowsPath(), true
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// RetargetGit routes a git invocation into the distro.
//
// git is called in four different shapes across this codebase: some sites put
// `-C <dir>` in the argv, others set cmd.Dir, and some add `-c` safety flags
// first. One helper handles all of them so a new call site cannot pick a
// subtly different form.
//
// argv must include "git" as its first element. Any `-C <path>` inside it is
// rewritten to the distro's view of that path, because inside the distro a
// Windows spelling means nothing.
func RetargetGit(t Target, dir string, argv []string) (Plan, bool) {
	if !t.IsWSL() || len(argv) == 0 {
		return Plan{}, false
	}
	translated := make([]string, 0, len(argv))
	// Only git's LEADING global options may carry a path. After the subcommand,
	// -C belongs to the subcommand (git grep -C 3, git log -C) or is an ordinary
	// value, and translating it would corrupt the command — or, when the value
	// is untranslatable, abort retargeting and silently run Windows git against
	// the UNC share, which is exactly what this layer exists to prevent.
	inGlobalOptions := true
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if i == 0 {
			translated = append(translated, arg)
			continue
		}
		if inGlobalOptions && arg == "-C" && i+1 < len(argv) {
			linux, ok := LinuxPathFor(t, argv[i+1])
			if !ok {
				// A path this distro cannot reach.
				return Plan{}, false
			}
			translated = append(translated, "-C", linux)
			i++
			continue
		}
		if inGlobalOptions {
			if valued, ok := gitGlobalOptionWithValue(arg); ok {
				translated = append(translated, arg)
				if valued && i+1 < len(argv) {
					i++
					translated = append(translated, argv[i])
				}
				continue
			}
			// The first non-option token is the subcommand.
			inGlobalOptions = false
		}
		translated = append(translated, arg)
	}
	// The command's own working directory becomes --cd, so a site that relied
	// on cmd.Dir keeps working.
	routed := t
	if dir != "" {
		linux, ok := LinuxPathFor(t, dir)
		if !ok {
			return Plan{}, false
		}
		routed.LinuxDir = linux
	}
	return RetargetExec(routed, translated, nil)
}

// gitGlobalOptionWithValue reports whether arg is one of git's leading global
// options and, if so, whether it consumes the following argument. Anything else
// ends the global-option region — it is the subcommand.
func gitGlobalOptionWithValue(arg string) (bool, bool) {
	switch arg {
	case "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix":
		return true, true
	case "--bare", "--no-pager", "--paginate", "-p", "--no-replace-objects",
		"--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs":
		return false, true
	}
	// The long forms that embed their value, e.g. --git-dir=/x, take no separate
	// argument but are still global options.
	for _, prefix := range []string{"--git-dir=", "--work-tree=", "--namespace=", "--exec-path="} {
		if strings.HasPrefix(arg, prefix) {
			return false, true
		}
	}
	return false, false
}

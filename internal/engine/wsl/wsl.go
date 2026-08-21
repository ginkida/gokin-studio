// Package wsl translates between Windows and WSL views of a path and builds the
// command lines that run work inside a distro.
//
// Everything here is deliberately PURE: no exec, no filesystem, no build tags.
// The interesting logic — UNC parsing, path translation, argument construction,
// distro-list parsing — is where the mistakes live, and it must be testable on
// any operating system rather than only on the one machine that has WSL.
//
// Two shapes of Windows path matter:
//
//	\\wsl.localhost\Ubuntu\home\me\proj   (Windows 11 and recent Windows 10)
//	\\wsl$\Ubuntu\home\me\proj            (older, still widely present)
//
// Both are UNC shares served over 9P. Reading and writing through them works, so
// ordinary file tools need no changes; what does NOT work is running the distro's
// toolchain, because that lives inside the distro.
package wsl

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrUnavailable is returned when WSL cannot be used on this machine — either
// the platform is not Windows, or wsl.exe is not installed.
var ErrUnavailable = errors.New("WSL is not available on this system")

// UNC hosts that expose a distro's root filesystem.
const (
	hostModern = "wsl.localhost"
	hostLegacy = "wsl$"
)

// Location identifies a directory inside a WSL distro.
type Location struct {
	// Distro is the name exactly as it appeared in the path, which is what
	// `wsl.exe -d` expects. Distro names are matched case-insensitively by
	// Windows but wsl.exe wants the real spelling, so it is preserved.
	Distro string
	// LinuxPath is absolute inside the distro, always slash-separated and
	// always starting with "/". The distro root is "/".
	LinuxPath string
	// Host records which UNC spelling the caller used, so a round-trip does
	// not silently rewrite the user's path to the other form.
	Host string
}

// String renders the Windows UNC form of this location.
func (l Location) String() string { return l.WindowsPath() }

// WindowsPath rebuilds the UNC path for this location.
func (l Location) WindowsPath() string {
	host := l.Host
	if host == "" {
		host = hostModern
	}
	base := `\\` + host + `\` + l.Distro
	trimmed := strings.Trim(l.LinuxPath, "/")
	if trimmed == "" {
		return base
	}
	return base + `\` + strings.ReplaceAll(trimmed, "/", `\`)
}

// ParseWindowsPath reports whether p addresses a WSL distro and, if so, what it
// points at. It accepts either UNC spelling and either slash direction, because
// Go, the Windows shell and users all disagree about which to use.
func ParseWindowsPath(p string) (Location, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), "/", `\`)
	if !strings.HasPrefix(normalized, `\\`) {
		return Location{}, false
	}
	rest := normalized[2:]
	slash := strings.IndexByte(rest, '\\')
	if slash <= 0 {
		return Location{}, false
	}
	host := rest[:slash]
	if !strings.EqualFold(host, hostModern) && !strings.EqualFold(host, hostLegacy) {
		return Location{}, false
	}
	rest = rest[slash+1:]
	// The distro segment is required: \\wsl.localhost alone is the share list,
	// not a filesystem.
	distro := rest
	remainder := ""
	if next := strings.IndexByte(rest, '\\'); next >= 0 {
		distro, remainder = rest[:next], rest[next+1:]
	}
	if distro == "" {
		return Location{}, false
	}
	linux := "/" + strings.ReplaceAll(strings.Trim(remainder, `\`), `\`, "/")
	return Location{
		Distro:    distro,
		LinuxPath: cleanLinuxPath(linux),
		Host:      strings.ToLower(host),
	}, true
}

// IsWSLPath is the cheap predicate for callers that only need a yes/no.
func IsWSLPath(p string) bool {
	_, ok := ParseWindowsPath(p)
	return ok
}

// cleanLinuxPath collapses redundant separators and resolves "." and ".."
// lexically. It never escapes the root: "/.." is "/", which matches how the
// kernel treats it.
func cleanLinuxPath(p string) string {
	if p == "" {
		return "/"
	}
	segments := strings.Split(p, "/")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, segment)
		}
	}
	if len(out) == 0 {
		return "/"
	}
	return "/" + strings.Join(out, "/")
}

// WindowsDriveToLinux maps a Windows drive path onto the distro's automount
// view of it: C:\Users\me -> /mnt/c/Users/me.
//
// This is how a distro sees the Windows filesystem, not the other way round. It
// is the right translation when a WSL command must reach something that lives on
// the Windows side.
func WindowsDriveToLinux(p string) (string, bool) {
	trimmed := strings.TrimSpace(p)
	// A separator is required. "C:" means the per-drive current directory and
	// "C:foo" is relative to it — neither is C:\ or C:\foo, so translating them
	// would silently name a different directory inside the distro.
	if len(trimmed) < 3 || trimmed[1] != ':' || (trimmed[2] != '\\' && trimmed[2] != '/') {
		return "", false
	}
	drive := trimmed[0]
	switch {
	case drive >= 'a' && drive <= 'z':
		drive -= 'a' - 'A'
	case drive >= 'A' && drive <= 'Z':
	default:
		return "", false
	}
	rest := strings.ReplaceAll(trimmed[2:], `\`, "/")
	return cleanLinuxPath("/mnt/" + strings.ToLower(string(drive)) + "/" + rest), true
}

// LoginShellArgs builds the arguments for running a shell command inside a
// distro with a working directory.
//
// `--cd` is used rather than `cd &&` because it sets the directory before the
// shell starts, so a failure to enter it is reported by wsl.exe instead of being
// swallowed into the command's own output.
//
// A LOGIN shell (`-lc`) is deliberate: the point of a WSL project is the
// distro's toolchain, and nvm, pyenv, rbenv, asdf and cargo all install
// themselves into login profiles. `--exec`, which skips the shell entirely,
// would find none of them.
func LoginShellArgs(distro, linuxDir, command string) []string {
	args := make([]string, 0, 8)
	if distro != "" {
		args = append(args, "-d", distro)
	}
	if linuxDir != "" {
		args = append(args, "--cd", linuxDir)
	}
	// --exec is REQUIRED, not decoration. Without it wsl.exe hands everything
	// after the flags to the user's DEFAULT LOGIN SHELL, which word-splits and
	// expands $, backticks and quotes before `bash -lc` ever sees the script.
	// The bash tool's payload contains "$workspace_root", "$(pwd -P)", $#, $@
	// and $? — every one of them would be mangled. --exec marshals argv through
	// execvp with no intermediate shell, and it also stops flag parsing.
	//
	// `bash -lc` still sources the login profile, so nvm/pyenv/asdf/cargo
	// resolve exactly as they do in the user's own terminal.
	args = append(args, "--exec", "bash", "-lc", command)
	return args
}

// ExecArgs builds arguments for running a binary directly, with no shell. Use it
// when the argument vector is already exact and no profile is wanted — notably
// git, where a login shell would only add startup cost and surprises.
func ExecArgs(distro, linuxDir string, argv ...string) []string {
	args := make([]string, 0, 6+len(argv))
	if distro != "" {
		args = append(args, "-d", distro)
	}
	if linuxDir != "" {
		args = append(args, "--cd", linuxDir)
	}
	args = append(args, "--exec")
	args = append(args, argv...)
	return args
}

// ShellQuote wraps a value for safe use inside a single-quoted POSIX shell
// context. Needed whenever a path or value has to be interpolated into a
// command string rather than passed as its own argv entry.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// DistroState mirrors the columns of `wsl.exe --list --verbose`.
type DistroState struct {
	Name    string
	Running bool
	Version int // 1 or 2; 0 when the column is missing or unparseable
	Default bool
}

// ParseDistroList reads the output of `wsl.exe --list --verbose`.
//
// wsl.exe writes UTF-16LE, so callers should pass the bytes through
// DecodeConsoleOutput first. The header row is localised — it is "NAME STATE
// VERSION" in English but not in other UI languages — so this deliberately does
// NOT match on header text. It skips the first non-empty line and parses the
// rest positionally, which is stable across locales.
func ParseDistroList(output string) []DistroState {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	var distros []DistroState
	seenHeader := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isDefault := strings.HasPrefix(trimmed, "*")
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		if !seenHeader {
			// The first non-empty row is the localised header.
			seenHeader = true
			continue
		}
		state := DistroState{Name: fields[0], Default: isDefault}
		if len(fields) >= 2 {
			// "Running"/"Stopped" are also localised, so compare against the
			// English word but fall back to "not stopped means running" only
			// when the word is recognisable.
			state.Running = strings.EqualFold(fields[1], "Running")
		}
		if len(fields) >= 3 {
			switch fields[2] {
			case "1":
				state.Version = 1
			case "2":
				state.Version = 2
			}
		}
		distros = append(distros, state)
	}
	return distros
}

// DecodeConsoleOutput converts wsl.exe's UTF-16LE output to UTF-8. Output that
// is already valid UTF-8 is returned unchanged, so this is safe to apply
// unconditionally.
func DecodeConsoleOutput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	// A BOM is decisive.
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return decodeUTF16LE(raw[2:])
	}
	if utf8.Valid(raw) && !looksUTF16LE(raw) {
		return string(raw)
	}
	if len(raw)%2 == 0 && looksUTF16LE(raw) {
		return decodeUTF16LE(raw)
	}
	return string(raw)
}

// looksUTF16LE detects the giveaway of ASCII text encoded as UTF-16LE: every
// second byte is zero.
func looksUTF16LE(raw []byte) bool {
	if len(raw) < 4 || len(raw)%2 != 0 {
		return false
	}
	zeros := 0
	pairs := 0
	for i := 1; i < len(raw); i += 2 {
		pairs++
		if raw[i] == 0 {
			zeros++
		}
	}
	return pairs > 0 && zeros*2 >= pairs
}

func decodeUTF16LE(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00")
}

// ValidateDistroName rejects anything that could not be a real distro or that
// would change the meaning of a wsl.exe command line. Distro names reach an
// argv entry, never a shell, but a name starting with "-" would still be read
// as a flag.
func ValidateDistroName(name string) error {
	trimmed := strings.TrimSpace(name)
	switch {
	case trimmed == "":
		return fmt.Errorf("distro name is empty")
	case len(trimmed) > 64:
		return fmt.Errorf("distro name is too long")
	case strings.HasPrefix(trimmed, "-"):
		return fmt.Errorf("distro name %q cannot start with '-'", trimmed)
	case strings.ContainsAny(trimmed, "\\/\x00\r\n"):
		return fmt.Errorf("distro name %q contains a path or control character", trimmed)
	case !utf8.ValidString(trimmed):
		return fmt.Errorf("distro name is not valid UTF-8")
	}
	return nil
}

// ParseDistroNames reads `wsl.exe --list --quiet`, which prints one registered
// name per line and no header.
//
// This is the AUTHORITATIVE source of distro names. `--list --verbose` pads its
// columns with spaces instead of delimiting them, so a distro registered as
// "My Distro" (wsl --import accepts it) splits into "My" there.
func ParseDistroNames(output string) []string {
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		name := strings.Trim(strings.TrimSpace(line), "\x00")
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// MergeDistroStates combines the three wsl.exe listings into one view: names
// come from the quiet listing, running state from the quiet running listing,
// and version/default from the verbose one (matched case-insensitively, so a
// mis-split verbose row simply contributes nothing).
func MergeDistroStates(all, running []string, verbose []DistroState) []DistroState {
	isRunning := make(map[string]bool, len(running))
	for _, name := range running {
		isRunning[strings.ToLower(name)] = true
	}
	byName := make(map[string]DistroState, len(verbose))
	for _, state := range verbose {
		byName[strings.ToLower(state.Name)] = state
	}
	out := make([]DistroState, 0, len(all))
	for _, name := range all {
		state := DistroState{Name: name, Running: isRunning[strings.ToLower(name)]}
		if extra, ok := byName[strings.ToLower(name)]; ok {
			state.Version = extra.Version
			state.Default = extra.Default
		}
		out = append(out, state)
	}
	return out
}

// ReconcileDistroName maps a name as spelled in a path onto the registered
// spelling. Windows matches UNC shares case-insensitively, so a user can
// legitimately type \\wsl.localhost\ubuntu\... for a distro registered as
// "Ubuntu" — but `wsl.exe -d ubuntu` would then fail to find it.
func ReconcileDistroName(parsed string, known []string) string {
	for _, name := range known {
		if strings.EqualFold(name, parsed) {
			return name
		}
	}
	return parsed
}

// CanonicalWindowsPath rewrites a WSL path to the modern UNC spelling. Use it
// for IDENTITY, never for display: WindowsPath deliberately preserves whichever
// spelling the user chose, so their stored config is not silently rewritten.
func CanonicalWindowsPath(p string) (string, bool) {
	location, ok := ParseWindowsPath(p)
	if !ok {
		return "", false
	}
	location.Host = hostModern
	return location.WindowsPath(), true
}

// CanonicalKey collapses every spelling of one location to a single comparable
// string, so \\wsl$\Ubuntu\p and \\wsl.localhost\ubuntu\p are recognised as
// the same project rather than two.
func CanonicalKey(p string) string {
	location, ok := ParseWindowsPath(p)
	if !ok {
		return strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "/")
	}
	return "wsl://" + strings.ToLower(location.Distro) + location.LinuxPath
}

// JoinArgv renders an exact argument vector as a single shell-safe string, for
// the fallback path where a command has to be handed to a shell rather than
// exec'd directly.
func JoinArgv(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, ShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// ParseSupportsCD reports whether this wsl.exe understands --cd, which arrived
// in the Store build (0.51.2) and is absent from the legacy inbox component.
// The flag NAME is not localised even when the surrounding help text is.
func ParseSupportsCD(helpOutput string) bool {
	return strings.Contains(helpOutput, "--cd")
}

// Failure kinds returned by ClassifyWSLFailure.
const (
	FailureDistroMissing    = "distro-missing"
	FailureDistroTerminated = "distro-terminated"
	FailureUnavailable      = "wsl-unavailable"
)

// ClassifyWSLFailure separates wsl.exe's OWN failures from the exit status of
// the command it ran. This matters because wsl.exe returns 1 for its own
// errors, which is indistinguishable from `grep` finding no match or
// `git check-ignore` reporting "not ignored" — both of which are normal.
//
// stderr must already be decoded with DecodeConsoleOutput.
func ClassifyWSLFailure(exitCode int, decodedStderr string) (string, bool) {
	if exitCode == 0 {
		return "", false
	}
	lowered := strings.ToLower(decodedStderr)
	switch {
	case strings.Contains(lowered, "no distribution with the supplied name"),
		strings.Contains(lowered, "there is no distribution"):
		return FailureDistroMissing, true
	case strings.Contains(lowered, "has terminated"):
		return FailureDistroTerminated, true
	case strings.Contains(lowered, "windows subsystem for linux has no installed distributions"),
		strings.Contains(lowered, "is not installed"),
		strings.Contains(lowered, "wsl is not"):
		return FailureUnavailable, true
	}
	return "", false
}

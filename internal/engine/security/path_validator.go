package security

import (
	"fmt"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PathValidator validates file paths to prevent directory traversal attacks.
type PathValidator struct {
	allowedDirs   []string
	allowSymlinks bool
	// fromPrefix/toPrefix translate a Linux path the model read out of its own
	// tool output back to the Windows spelling the file tools need. Empty
	// fromPrefix disables translation entirely, which is the default and the
	// only state reachable off Windows.
	fromPrefix string
	toPrefix   string
}

// NewPathValidator creates a new path validator.
func NewPathValidator(allowedDirs []string, allowSymlinks bool) *PathValidator {
	// Normalize allowed directories
	normalized := make([]string, len(allowedDirs))
	for i, dir := range allowedDirs {
		normalized[i] = filepath.Clean(dir)
	}
	validator := &PathValidator{
		allowedDirs:   normalized,
		allowSymlinks: allowSymlinks,
	}
	// Once commands run inside a distro, every compiler error, stack trace and
	// `git status` line the model reads names /home/me/... while every file
	// tool still takes the UNC spelling. Translating here covers all 16 tools
	// that share this validator instead of wiring each one.
	//
	// wsl.Available() is a constant false off Windows, so this costs one
	// boolean there and the validator is byte-identical to before.
	if wsl.Available() {
		if from, to, ok := WSLPathTranslation(allowedDirs); ok {
			validator.fromPrefix, validator.toPrefix = from, to
		}
	}
	return validator
}

// SetPathTranslation configures the Linux-to-Windows rewrite explicitly. Tests
// use it to exercise the Windows behaviour on any platform.
func (v *PathValidator) SetPathTranslation(fromPrefix, toPrefix string) {
	v.fromPrefix, v.toPrefix = fromPrefix, toPrefix
}

// WSLPathTranslation derives the rewrite from the allowed directories, which
// already carry the target. The first directory that parses as a WSL UNC path
// wins; its Linux form is what the model will see in tool output.
func WSLPathTranslation(allowedDirs []string) (string, string, bool) {
	for _, dir := range allowedDirs {
		if location, ok := wsl.ParseWindowsPath(dir); ok {
			return location.LinuxPath, dir, true
		}
	}
	return "", "", false
}

// TranslatePrefixPath rewrites p from one prefix to another, matching only on a
// path-segment boundary so /home/me/api2 is never treated as living under
// /home/me/api.
func TranslatePrefixPath(p, fromPrefix, toPrefix string) (string, bool) {
	if fromPrefix == "" || p == "" || !strings.HasPrefix(p, "/") {
		return p, false
	}
	if p == fromPrefix {
		return toPrefix, true
	}
	if !strings.HasPrefix(p, fromPrefix) {
		return p, false
	}
	rest := p[len(fromPrefix):]
	// A prefix of "/" — a project registered at the distro root — already ends
	// in the separator, so the boundary test below would reject every path.
	if fromPrefix == "/" {
		return filepath.Join(toPrefix, filepath.FromSlash(rest)), true
	}
	// A boundary is required: without it "/home/me/api" would also match
	// "/home/me/apiary".
	if !strings.HasPrefix(rest, "/") {
		return p, false
	}
	return filepath.Join(toPrefix, filepath.FromSlash(strings.TrimPrefix(rest, "/"))), true
}

// Validate validates that a path is safe and within allowed directories.
// Uses filepath.EvalSymlinks for atomic symlink resolution to prevent TOCTOU races.
func (v *PathValidator) Validate(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	// Additional security checks - do these first before any file operations
	if strings.Contains(path, "\x00") {
		return "", fmt.Errorf("null byte in path")
	}

	// A Linux path the model copied out of its own tool output becomes the
	// Windows spelling the filesystem needs. Inert when translation is off,
	// which is always the case off Windows.
	path, _ = TranslatePrefixPath(path, v.fromPrefix, v.toPrefix)

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Convert to absolute path for validation
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	// Check for symlink if not allowed (check ORIGINAL path before resolution,
	// because EvalSymlinks resolves all symlinks and checkSymlink would find
	// nothing on the resolved path)
	if !v.allowSymlinks {
		if err := v.checkSymlink(absPath); err != nil {
			return "", err
		}
	}

	// Use EvalSymlinks for atomic symlink resolution (prevents TOCTOU race)
	// This resolves all symlinks in the path atomically
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// Path doesn't exist yet - that's OK for new files, but we need to
		// check the parent directory to prevent symlink attacks
		if os.IsNotExist(err) {
			// Check parent directory instead
			parentDir := filepath.Dir(absPath)
			resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
			if parentErr != nil && !os.IsNotExist(parentErr) {
				return "", fmt.Errorf("failed to resolve parent path: %w", parentErr)
			}
			// Use resolved parent + filename for validation
			if resolvedParent != "" {
				resolvedPath = filepath.Join(resolvedParent, filepath.Base(absPath))
			} else {
				resolvedPath = absPath
			}
		} else if tolerateUnresolvedPath(absPath) {
			// The unresolved path is used as-is, so the containment check below
			// is purely lexical for it. That is only safe because the scan
			// above proved no component is a link of any kind — see
			// tolerateUnresolvedPath.
			resolvedPath = absPath
		} else {
			return "", fmt.Errorf("failed to resolve symlinks: %w", err)
		}
	}

	// Check if resolved path is within allowed directories
	if !v.isAllowed(resolvedPath) {
		return "", fmt.Errorf("path '%s' is outside allowed directories", filepath.Base(path))
	}

	return resolvedPath, nil
}

// ValidateFile validates a file path for read/write operations.
func (v *PathValidator) ValidateFile(path string) (string, error) {
	absPath, err := v.Validate(path)
	if err != nil {
		return "", err
	}

	// Check if parent directory exists
	dir := filepath.Dir(absPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("parent directory does not exist: %s", dir)
	}

	return absPath, nil
}

// ValidateDir validates a directory path.
func (v *PathValidator) ValidateDir(path string) (string, error) {
	absPath, err := v.Validate(path)
	if err != nil {
		return "", err
	}

	// Check if it's actually a directory
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}

	return absPath, nil
}

// isAllowed checks if the path is within allowed directories.
func (v *PathValidator) isAllowed(absPath string) bool {
	// If no restrictions, allow all (use with caution)
	if len(v.allowedDirs) == 0 {
		return true
	}

	for _, allowedDir := range v.allowedDirs {
		if v.isPathWithin(absPath, allowedDir) {
			return true
		}
	}
	return false
}

// isPathWithin checks if target is within base directory.
// Handles cross-drive paths on Windows and other edge cases.
func (v *PathValidator) isPathWithin(target, base string) bool {
	// Handle Windows cross-drive paths
	// filepath.Rel returns an error when target and base are on different drives
	rel, err := filepath.Rel(base, target)
	if err != nil {
		// Different drives or other path resolution issues
		// Check if they share the same root
		return filepath.VolumeName(target) == filepath.VolumeName(base)
	}

	// If relative path starts with "..", target is outside base
	if strings.HasPrefix(rel, "..") {
		return false
	}

	// Double check: joined path must match exactly or be a subpath
	joined := filepath.Join(base, rel)
	// On Windows, paths are case-insensitive, so we need to compare accordingly
	if runtime.GOOS == "windows" {
		lowerJoined := strings.ToLower(joined)
		lowerBase := strings.ToLower(base)
		return lowerJoined == lowerBase || strings.HasPrefix(lowerJoined, lowerBase+string(filepath.Separator))
	}
	return joined == base || strings.HasPrefix(joined, base+string(filepath.Separator))
}

// checkSymlink checks if any component of the path is a symlink.
// tolerateUnresolvedPath reports whether a non-ENOENT EvalSymlinks failure is
// expected rather than suspicious, in which case the caller proceeds with the
// UNRESOLVED path and containment becomes a lexical check.
//
// Being WSL is not enough to make that safe. Go maps only
// IO_REPARSE_TAG_SYMLINK to ModeSymlink; a Linux symlink over the 9P share
// arrives as IO_REPARSE_TAG_LX_SYMLINK, lands in the default arm of
// os.fileStat.mode and becomes ModeIrregular, and since it is a name surrogate
// Go also withholds ModeDir. That is EXACTLY the shape that makes EvalSymlinks
// return ENOTDIR. So the failure this hatch exists for and a symlink escape are
// the same error value, and tolerating on the error alone would let
// `proj\escape -> /` resolve to \\wsl.localhost\Ubuntu\proj\escape\etc\shadow and
// pass a lexical containment check.
//
// The two are distinguishable one level down: an ordinary directory has no
// reparse point on any component, a link has one. Scan for that and fail closed
// when anything is found, or when the scan itself cannot answer.
func tolerateUnresolvedPath(absPath string) bool {
	if !wsl.IsWSLPath(absPath) {
		return false
	}
	scan, err := scanPathLinks(absPath)
	return unresolvedPathIsTolerable(scan, err)
}

// unresolvedPathIsTolerable is the decision itself, split out because
// wsl.IsWSLPath is false for every path that exists on the machine this code is
// developed on, so the branch above cannot otherwise be exercised. Fail closed
// on a scan error: not knowing is not the same as knowing there is no link.
func unresolvedPathIsTolerable(scan linkScan, scanErr error) bool {
	return scanErr == nil && !scan.found()
}

// linkScan is what an upward walk saw. reparse covers the non-symlink reparse
// points Go cannot classify — WSL's LX symlinks, mount points, junctions.
type linkScan struct {
	symlink string
	reparse string
}

func (s linkScan) found() bool { return s.symlink != "" || s.reparse != "" }

// scanPathLinks walks a path upward with filepath.Dir instead of splitting on
// the separator and rejoining. Splitting needs the volume ("C:", "\\\\host\\share")
// peeled off first, and the previous implementation did not do that: it
// prepended the volume and then joined the volume's own segments back on, so on
// Windows every Lstat hit a path like \\host\share\host\share\..., got ENOENT
// and was skipped — allowSymlinks=false was silently unenforced there. Dir stops
// at the volume root on its own, on every platform, with no such arithmetic.
//
// A component that does not exist yet is not an error: files are created
// through this validator too.
func scanPathLinks(path string) (linkScan, error) {
	var scan linkScan
	for current := filepath.Clean(path); ; {
		info, err := os.Lstat(current)
		switch {
		case err != nil && !os.IsNotExist(err):
			return scan, err
		case err != nil:
			// missing component; keep walking toward the root
		case info.Mode()&os.ModeSymlink != 0:
			if scan.symlink == "" {
				scan.symlink = current
			}
		case info.Mode()&os.ModeIrregular != 0:
			if scan.reparse == "" {
				scan.reparse = current
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the root ("/", "C:\\", "\\\\host\\share\\"); Dir is a fixed
			// point from here.
			return scan, nil
		}
		current = parent
	}
}

func (v *PathValidator) checkSymlink(path string) error {
	scan, err := scanPathLinks(path)
	if err != nil {
		return err
	}
	if scan.symlink != "" {
		return fmt.Errorf("symlinks not allowed: %s", scan.symlink)
	}
	// A reparse point Go could not classify is only treated as a link on a WSL
	// path, where it is how a Linux symlink actually presents. Applying it
	// everywhere would start rejecting Windows junctions and OneDrive
	// placeholders that this validator has always accepted, and every non-WSL
	// project must stay byte-identical.
	if scan.reparse != "" && wsl.IsWSLPath(path) {
		return fmt.Errorf("symlinks not allowed: %s", scan.reparse)
	}
	return nil
}

// IsBlockedWritePath returns an error if the resolved path points to a location
// that must never be written to by tools. The .git/ directory contains hooks,
// config, and attributes that can execute arbitrary code on git operations.
func IsBlockedWritePath(path string) error {
	cleanPath := filepath.Clean(path)
	sep := string(filepath.Separator)
	gitComponent := sep + ".git" + sep
	gitSuffix := sep + ".git"
	// Block paths inside .git/ (hooks, config, attributes — all can execute code)
	// and the .git entry itself (in worktrees it's a file with gitdir: redirect
	// that can point to a directory with malicious hooks).
	if strings.Contains(cleanPath, gitComponent) || strings.HasSuffix(cleanPath, gitSuffix) {
		return fmt.Errorf("writing to .git/ directory is blocked for security reasons")
	}
	return nil
}

// SanitizeFilename sanitizes a filename by removing dangerous characters.
func SanitizeFilename(name string) string {
	// Remove null bytes and other dangerous characters
	dangerous := []string{"\x00", "..", "/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	sanitized := name
	for _, dangerousChar := range dangerous {
		sanitized = strings.ReplaceAll(sanitized, dangerousChar, "_")
	}
	return sanitized
}

// JoinPathSafe joins path components safely.
func JoinPathSafe(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("base path cannot be empty")
	}

	cleanBase := filepath.Clean(base)
	cleanRel := filepath.Clean(rel)

	// Don't allow absolute paths in relative part
	if filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("relative path cannot be absolute")
	}

	joined := filepath.Join(cleanBase, cleanRel)

	// Verify the result is still within base (use separator-aware check to prevent prefix matching bugs)
	if joined != cleanBase && !strings.HasPrefix(joined, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return joined, nil
}

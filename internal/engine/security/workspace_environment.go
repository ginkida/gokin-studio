package security

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	MaxWorkspaceEnvironmentVariables  = 64
	MaxWorkspaceEnvironmentNameBytes  = 128
	MaxWorkspaceEnvironmentValueBytes = 32 << 10
	MaxWorkspaceEnvironmentTotalBytes = 48 << 10
)

var (
	workspaceEnvironmentMu sync.RWMutex
	workspaceEnvironment   = map[string]string{}
)

var reservedWorkspaceEnvironmentNames = map[string]struct{}{
	"PATH": {}, "HOME": {}, "PWD": {}, "OLDPWD": {}, "SHELL": {},
	"TMPDIR": {}, "TMP": {}, "TEMP": {},
	"XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {}, "XDG_CACHE_HOME": {},
	"GOPATH": {}, "GOCACHE": {}, "GOROOT": {}, "NPM_CONFIG_CACHE": {}, "PIP_CACHE_DIR": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "DYLD_INSERT_LIBRARIES": {}, "DYLD_LIBRARY_PATH": {},
	"BASH_ENV": {}, "ENV": {}, "PROMPT_COMMAND": {}, "IFS": {}, "CDPATH": {},
	"SHELLOPTS": {}, "BASHOPTS": {}, "PS4": {},
}

// reservedWorkspaceEnvironmentPrefix rejects whole families of loader and
// shell-injection variables rather than individual spellings. Naming only
// DYLD_INSERT_LIBRARIES and DYLD_LIBRARY_PATH left DYLD_FRAMEWORK_PATH,
// DYLD_FALLBACK_LIBRARY_PATH, DYLD_VERSIONED_LIBRARY_PATH, LD_AUDIT and
// friends accepted — each of which loads attacker-chosen code into every
// sandboxed bash/test/preview process the agent starts.
func reservedWorkspaceEnvironmentPrefix(upper string) bool {
	for _, prefix := range []string{"DYLD_", "LD_", "BASH_FUNC_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// ValidateWorkspaceEnvironment keeps user-defined variables from weakening the
// workspace boundary or creating an unbounded secure-storage payload.
func ValidateWorkspaceEnvironment(values map[string]string) error {
	if len(values) > MaxWorkspaceEnvironmentVariables {
		return fmt.Errorf("local environment supports at most %d variables", MaxWorkspaceEnvironmentVariables)
	}
	total := 0
	seen := make(map[string]string, len(values))
	for name, value := range values {
		if len(name) == 0 || len(name) > MaxWorkspaceEnvironmentNameBytes || !validWorkspaceEnvironmentName(name) {
			return fmt.Errorf("invalid environment variable name %q", name)
		}
		upper := strings.ToUpper(name)
		if previous, exists := seen[upper]; exists {
			return fmt.Errorf("environment variable names %q and %q conflict", previous, name)
		}
		seen[upper] = name
		if _, reserved := reservedWorkspaceEnvironmentNames[upper]; reserved || reservedWorkspaceEnvironmentPrefix(upper) {
			return fmt.Errorf("environment variable %q is reserved by workspace isolation", name)
		}
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || len(value) > MaxWorkspaceEnvironmentValueBytes {
			return fmt.Errorf("environment variable %q has an invalid or oversized value", name)
		}
		total += len(name) + len(value) + 2
		if total > MaxWorkspaceEnvironmentTotalBytes {
			return fmt.Errorf("local environment exceeds the %d KiB limit", MaxWorkspaceEnvironmentTotalBytes>>10)
		}
	}
	return nil
}

func validWorkspaceEnvironmentName(name string) bool {
	for index, char := range name {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

// SetWorkspaceEnvironment atomically replaces the variables inherited by new
// local workspace processes. Existing processes intentionally keep their
// original environment until restarted.
func SetWorkspaceEnvironment(values map[string]string) error {
	if values == nil {
		values = map[string]string{}
	}
	if err := ValidateWorkspaceEnvironment(values); err != nil {
		return err
	}
	clone := make(map[string]string, len(values))
	for name, value := range values {
		clone[name] = value
	}
	workspaceEnvironmentMu.Lock()
	workspaceEnvironment = clone
	workspaceEnvironmentMu.Unlock()
	return nil
}

func WorkspaceEnvironmentSnapshot() map[string]string {
	workspaceEnvironmentMu.RLock()
	defer workspaceEnvironmentMu.RUnlock()
	clone := make(map[string]string, len(workspaceEnvironment))
	for name, value := range workspaceEnvironment {
		clone[name] = value
	}
	return clone
}

// MergeWorkspaceEnvironment overlays the configured variables on an existing
// environment without mutating either input. It is used by the integrated
// terminal, whose interactive shell still needs the ordinary host environment.
func MergeWorkspaceEnvironment(base []string) []string {
	merged := make(map[string]string, len(base)+MaxWorkspaceEnvironmentVariables)
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if ok && name != "" {
			merged[name] = value
		}
	}
	for name, value := range WorkspaceEnvironmentSnapshot() {
		merged[name] = value
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+merged[name])
	}
	return result
}

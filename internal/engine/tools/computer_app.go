package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ComputerAppIDMaxBytes   = 1024
	ComputerAppNameMaxBytes = 256
)

// ComputerApplication is an OS-observed foreground application identity.
// ID is a bundle identifier on macOS and a normalized executable path on
// Windows; it never comes from model-provided tool arguments.
type ComputerApplication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	PID  int    `json:"pid,omitempty"`
}

func ForegroundApplication(ctx context.Context) (ComputerApplication, error) {
	app, err := foregroundApplication(ctx)
	if err != nil {
		return ComputerApplication{}, err
	}
	app.ID = NormalizeComputerAppID(app.ID)
	app.Name = strings.TrimSpace(app.Name)
	if app.ID == "" || len(app.ID) > ComputerAppIDMaxBytes || !utf8.ValidString(app.ID) {
		return ComputerApplication{}, fmt.Errorf("foreground application returned an invalid identity")
	}
	if app.Name == "" {
		app.Name = filepath.Base(app.ID)
	}
	if len(app.Name) > ComputerAppNameMaxBytes || !utf8.ValidString(app.Name) || hasComputerControlRune(app.Name) {
		return ComputerApplication{}, fmt.Errorf("foreground application returned an invalid name")
	}
	return app, nil
}

func NormalizeComputerAppID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "/")
	return strings.ToLower(value)
}

func hasComputerControlRune(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\t'
	}) >= 0
}

// IsSensitiveComputerApplication protects common credential and hardware
// wallet applications regardless of a project's stored allowlist.
func IsSensitiveComputerApplication(app ComputerApplication) bool {
	haystack := strings.ToLower(app.ID + "\n" + app.Name)
	for _, marker := range []string{
		"1password", "keychain access", "keychainaccess",
		"bitwarden", "keepass", "lastpass",
		"ledger live", "ledger-live", "trezor suite", "trezor-suite",
	} {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

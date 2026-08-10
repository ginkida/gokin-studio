//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func runComputerAppleScript(ctx context.Context, script string, args ...string) error {
	binary, err := exec.LookPath("osascript")
	if err != nil {
		return fmt.Errorf("macOS osascript utility not found")
	}
	commandArgs := append([]string{"-e", script}, args...)
	if output, err := exec.CommandContext(ctx, binary, commandArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func performComputerClick(ctx context.Context, x, y int, button string) error {
	count := "1"
	control := "0"
	if button == "double" {
		count = "2"
	}
	if button == "right" {
		control = "1"
	}
	script := `on run argv
set px to item 1 of argv as integer
set py to item 2 of argv as integer
set n to item 3 of argv as integer
set useControl to item 4 of argv as integer
tell application "System Events"
if useControl is 1 then key down control
repeat n times
click at {px, py}
delay 0.08
end repeat
if useControl is 1 then key up control
end tell
end run`
	return runComputerAppleScript(ctx, script, strconv.Itoa(x), strconv.Itoa(y), count, control)
}

func performComputerType(ctx context.Context, text string) error {
	script := `on run argv
tell application "System Events" to keystroke (item 1 of argv)
end run`
	return runComputerAppleScript(ctx, script, text)
}

func performComputerKey(ctx context.Context, chord string) error {
	parts := strings.Split(chord, "+")
	key := parts[len(parts)-1]
	modifiers := parts[:len(parts)-1]
	modifierNames := make([]string, 0, len(modifiers))
	for _, modifier := range modifiers {
		switch modifier {
		case "CTRL":
			modifierNames = append(modifierNames, "control down")
		case "ALT":
			modifierNames = append(modifierNames, "option down")
		case "SHIFT":
			modifierNames = append(modifierNames, "shift down")
		case "META":
			modifierNames = append(modifierNames, "command down")
		}
	}
	using := ""
	if len(modifierNames) > 0 {
		using = " using {" + strings.Join(modifierNames, ", ") + "}"
	}
	keyCodes := map[string]int{
		"ENTER": 36, "ESC": 53, "ESCAPE": 53, "TAB": 48, "SPACE": 49,
		"BACKSPACE": 51, "DELETE": 117, "UP": 126, "DOWN": 125,
		"LEFT": 123, "RIGHT": 124, "HOME": 115, "END": 119,
		"PAGEUP": 116, "PAGEDOWN": 121,
	}
	if code, ok := keyCodes[key]; ok {
		return runComputerAppleScript(ctx, fmt.Sprintf(`tell application "System Events" to key code %d%s`, code, using))
	}
	return runComputerAppleScript(ctx, fmt.Sprintf(`tell application "System Events" to keystroke "%s"%s`, strings.ToLower(key), using))
}

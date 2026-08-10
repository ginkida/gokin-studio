//go:build windows

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func runComputerPowerShell(ctx context.Context, script string, args ...string) error {
	binary, err := exec.LookPath("powershell.exe")
	if err != nil {
		return fmt.Errorf("PowerShell not found")
	}
	commandArgs := append([]string{"-NoProfile", "-NonInteractive", "-Command", script}, args...)
	if output, err := exec.CommandContext(ctx, binary, commandArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func performComputerClick(ctx context.Context, x, y int, button string) error {
	flags := "0x0002,0x0004"
	count := "1"
	if button == "right" {
		flags = "0x0008,0x0010"
	}
	if button == "double" {
		count = "2"
	}
	script := `Add-Type @'
using System; using System.Runtime.InteropServices; public class Mouse { [DllImport("user32.dll")] public static extern bool SetCursorPos(int x,int y); [DllImport("user32.dll")] public static extern void mouse_event(uint f,uint dx,uint dy,uint d,UIntPtr e); }
'@; [Mouse]::SetCursorPos([int]$args[0],[int]$args[1])|Out-Null; $f=$args[2].Split(','); 1..([int]$args[3])|%{[Mouse]::mouse_event([uint32]$f[0],0,0,0,[UIntPtr]::Zero); [Mouse]::mouse_event([uint32]$f[1],0,0,0,[UIntPtr]::Zero); Start-Sleep -Milliseconds 80}`
	return runComputerPowerShell(ctx, script, strconv.Itoa(x), strconv.Itoa(y), flags, count)
}

func performComputerType(ctx context.Context, text string) error {
	script := `Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait($args[0])`
	return runComputerPowerShell(ctx, script, escapeWindowsSendKeys(text))
}

func performComputerKey(ctx context.Context, chord string) error {
	parts := strings.Split(chord, "+")
	key := parts[len(parts)-1]
	prefix := ""
	for _, modifier := range parts[:len(parts)-1] {
		switch modifier {
		case "CTRL":
			prefix += "^"
		case "ALT":
			prefix += "%"
		case "SHIFT":
			prefix += "+"
		case "META":
			return fmt.Errorf("Windows-key chords are not supported")
		}
	}
	names := map[string]string{
		"ENTER": "{ENTER}", "ESC": "{ESC}", "ESCAPE": "{ESC}", "TAB": "{TAB}",
		"SPACE": " ", "BACKSPACE": "{BACKSPACE}", "DELETE": "{DELETE}",
		"UP": "{UP}", "DOWN": "{DOWN}", "LEFT": "{LEFT}", "RIGHT": "{RIGHT}",
		"HOME": "{HOME}", "END": "{END}", "PAGEUP": "{PGUP}", "PAGEDOWN": "{PGDN}",
	}
	if named, ok := names[key]; ok {
		key = named
	}
	return runComputerPowerShell(ctx, `Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait($args[0])`, prefix+key)
}

func escapeWindowsSendKeys(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '+', '^', '%', '~', '(', ')', '[', ']', '{', '}':
			fmt.Fprintf(&b, "{%c}", r)
		case '\n':
			b.WriteString("{ENTER}")
		case '\r':
			continue
		case '\t':
			b.WriteString("{TAB}")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

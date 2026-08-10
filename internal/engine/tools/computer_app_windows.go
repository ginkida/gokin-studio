//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

func foregroundApplication(ctx context.Context) (ComputerApplication, error) {
	binary, err := exec.LookPath("powershell.exe")
	if err != nil {
		return ComputerApplication{}, fmt.Errorf("PowerShell not found")
	}
	script := `Add-Type @'` + "\n" +
		`using System; using System.Runtime.InteropServices; public class FG { [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow(); [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint p); }` + "\n" +
		`'@; [uint32]$p=0; [FG]::GetWindowThreadProcessId([FG]::GetForegroundWindow(),[ref]$p)|Out-Null; $x=Get-Process -Id $p; @{id=$x.MainModule.FileName;name=$x.ProcessName;pid=[int]$p}|ConvertTo-Json -Compress`
	output, err := exec.CommandContext(ctx, binary, "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return ComputerApplication{}, fmt.Errorf("identify foreground application: %w", err)
	}
	var app ComputerApplication
	if err := json.Unmarshal(output, &app); err != nil {
		return ComputerApplication{}, fmt.Errorf("parse foreground application: %w", err)
	}
	return app, nil
}

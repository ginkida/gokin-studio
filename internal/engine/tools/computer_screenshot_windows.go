//go:build windows

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func captureDesktopPNG(ctx context.Context) ([]byte, error) {
	return runWindowsScreenCapture(ctx, false)
}

func captureInteractiveDesktopPNG(ctx context.Context) ([]byte, error) {
	return runWindowsScreenCapture(ctx, true)
}

func runWindowsScreenCapture(ctx context.Context, interactive bool) ([]byte, error) {
	binary, err := exec.LookPath("powershell.exe")
	if err != nil {
		return nil, fmt.Errorf("PowerShell not found")
	}
	f, err := os.CreateTemp("", "gokin-screen-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	script := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $b=[System.Windows.Forms.SystemInformation]::VirtualScreen; $i=New-Object System.Drawing.Bitmap $b.Width,$b.Height; $g=[System.Drawing.Graphics]::FromImage($i); $g.CopyFromScreen($b.Left,$b.Top,0,0,$i.Size); $i.Save($args[0],[System.Drawing.Imaging.ImageFormat]::Png); $g.Dispose(); $i.Dispose()`
	flags := []string{"-NoProfile", "-NonInteractive"}
	if interactive {
		// Snipping Tool publishes the selected region/window to the clipboard.
		// GetClipboardSequenceNumber prevents a stale pre-existing image from
		// being accepted. The helper process runs STA because WinForms
		// clipboard APIs require a single-threaded apartment.
		script = `$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class GokinClipboardSequence {
  [DllImport("user32.dll")] public static extern uint GetClipboardSequenceNumber();
}
'@; $before=[GokinClipboardSequence]::GetClipboardSequenceNumber(); $tool=Join-Path $env:SystemRoot 'System32\SnippingTool.exe'; if (!(Test-Path $tool)) { throw 'Windows Snipping Tool was not found' }; Start-Process -FilePath $tool -ArgumentList '/clip'; $deadline=[DateTime]::UtcNow.AddSeconds(115); while ([DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 120; if ([GokinClipboardSequence]::GetClipboardSequenceNumber() -eq $before) { continue }; try { if ([System.Windows.Forms.Clipboard]::ContainsImage()) { $i=[System.Windows.Forms.Clipboard]::GetImage(); if ($null -ne $i) { $i.Save($args[0],[System.Drawing.Imaging.ImageFormat]::Png); $i.Dispose(); exit 0 } } } catch {} }; exit 2`
		flags = append(flags, "-Sta")
	}
	flags = append(flags, "-Command", script, path)
	if output, err := exec.CommandContext(ctx, binary, flags...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if interactive {
			if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 2 {
				return nil, ErrDesktopCaptureCancelled
			}
		}
		return nil, fmt.Errorf("%w: %s", err, string(output))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if interactive && len(data) == 0 {
		return nil, ErrDesktopCaptureCancelled
	}
	return data, nil
}

//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

func foregroundApplication(ctx context.Context) (ComputerApplication, error) {
	binary, err := exec.LookPath("osascript")
	if err != nil {
		return ComputerApplication{}, fmt.Errorf("macOS osascript utility not found")
	}
	script := `ObjC.import('AppKit'); const a=$.NSWorkspace.sharedWorkspace.frontmostApplication; JSON.stringify({id:ObjC.unwrap(a.bundleIdentifier)||'',name:ObjC.unwrap(a.localizedName)||'',pid:Number(a.processIdentifier)})`
	output, err := exec.CommandContext(ctx, binary, "-l", "JavaScript", "-e", script).Output()
	if err != nil {
		return ComputerApplication{}, fmt.Errorf("identify foreground application: %w", err)
	}
	var app ComputerApplication
	if err := json.Unmarshal(output, &app); err != nil {
		return ComputerApplication{}, fmt.Errorf("parse foreground application: %w", err)
	}
	return app, nil
}

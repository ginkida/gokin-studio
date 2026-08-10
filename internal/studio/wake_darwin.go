//go:build darwin

package studio

import (
	"fmt"
	"os/exec"
)

func wakePlatformSupported() bool {
	_, err := exec.LookPath("caffeinate")
	return err == nil
}

func acquirePlatformWakeLease(reason string) (wakeLease, error) {
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil, fmt.Errorf("find caffeinate: %w", err)
	}
	cmd := exec.Command(path, "-i")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start caffeinate: %w", err)
	}
	return wakeLeaseCloseProcess(cmd.Process, cmd.Wait), nil
}

//go:build !darwin && !windows

package studio

import (
	"fmt"
	"os/exec"
)

func wakePlatformSupported() bool {
	_, err := exec.LookPath("systemd-inhibit")
	return err == nil
}

func acquirePlatformWakeLease(reason string) (wakeLease, error) {
	path, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil, fmt.Errorf("find systemd-inhibit: %w", err)
	}
	cmd := exec.Command(path, "--what=sleep", "--mode=block", "--why="+reason, "sleep", "infinity")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start systemd-inhibit: %w", err)
	}
	return wakeLeaseCloseProcess(cmd.Process, cmd.Wait), nil
}

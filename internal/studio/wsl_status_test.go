package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

// Off Windows the status must be an honest "not applicable", not an empty list
// that reads like "no distributions installed".
func TestGetWSLStatusIsHonestWhenUnavailable(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	status := s.GetWSLStatus()
	if status.Available {
		t.Fatal("WSL reported available on a non-Windows build")
	}
	if status.Detail == "" {
		t.Fatal("an unavailable status must say why")
	}
	if status.Distros == nil {
		t.Fatal("Distros must be an empty slice, not null, so the UI can render it")
	}
	if len(status.Distros) != 0 {
		t.Fatalf("distros = %+v", status.Distros)
	}
}

// A stopped distro must not be reported as a missing folder — that would send
// the user hunting for a directory that is exactly where they left it.
func TestWSLDistroNoticeDistinguishesStoppedFromMissing(t *testing.T) {
	dir := `\\wsl.localhost\Ubuntu\home\me\api`

	stopped := wslDistroNotice(dir, true, []wsl.DistroState{{Name: "Ubuntu", Running: false}})
	if !strings.Contains(stopped, "not running") {
		t.Fatalf("stopped distro notice = %q", stopped)
	}

	running := wslDistroNotice(dir, true, []wsl.DistroState{{Name: "ubuntu", Running: true}})
	if running != "" {
		t.Fatalf("a running distro produced a notice: %q", running)
	}

	unregistered := wslDistroNotice(dir, true, []wsl.DistroState{{Name: "Debian", Running: true}})
	if !strings.Contains(unregistered, "not registered") {
		t.Fatalf("unregistered distro notice = %q", unregistered)
	}

	noWSL := wslDistroNotice(dir, false, nil)
	if !strings.Contains(noWSL, "wsl.exe was not found") {
		t.Fatalf("missing wsl.exe notice = %q", noWSL)
	}

	// An ordinary directory has nothing to say.
	if got := wslDistroNotice(`C:\Users\me\api`, true, nil); got != "" {
		t.Fatalf("a local path produced a WSL notice: %q", got)
	}
}

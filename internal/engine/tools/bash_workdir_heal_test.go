package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func newBoundedBashTool(t *testing.T, root string) *BashTool {
	t.Helper()
	tool := NewBashTool(root)
	tool.SetWorkspaceBoundary(root)
	return tool
}

// A directory the Windows share cannot stat is still adopted, because the host
// is not the authority: the next command reaches it as `wsl.exe --cd <linux
// path>`, translated purely from strings. Names that are legal in Linux and
// impossible on Windows — a timestamp with colons is the everyday case — would
// otherwise strand the session in the wrong directory while the model believes
// it moved, permanently and with no error.
func TestUnstattableDirectoryIsAdoptedAndKept(t *testing.T) {
	root := t.TempDir()
	tool := newBoundedBashTool(t, root)

	// Never created on disk; on Windows this is the `logs/2026-08-11T09:00:00`
	// case, which no amount of waiting will make stat-able.
	unnameable := filepath.Join(root, "logs-with-a-name-windows-cannot-hold")
	tool.session.adoptUnverifiedWorkDir(unnameable)

	if got := tool.sessionWorkDir(); got != unnameable {
		t.Fatalf("sessionWorkDir() = %q, want the adopted directory %q", got, unnameable)
	}
	// Repeated reads must not erode it either — the old implementation re-ran a
	// stat on every command and relocated the session on the second call.
	if got := tool.sessionWorkDir(); got != unnameable {
		t.Fatalf("second call returned %q; the directory was silently dropped", got)
	}
}

// The one case adoption cannot cover: the directory really is gone, so
// `--cd` fails before the payload runs and no pwd marker comes back. Without
// recovery the session is wedged there for its whole lifetime.
func TestMissingPWDOnAFailedCommandUnwindsAnUnverifiedDirectory(t *testing.T) {
	root := t.TempDir()
	tool := newBoundedBashTool(t, root)
	tool.session.adoptUnverifiedWorkDir(filepath.Join(root, "deleted-under-us"))

	tool.recoverUnverifiedWorkDir()

	if got := tool.sessionWorkDir(); got != filepath.Clean(root) {
		t.Fatalf("sessionWorkDir() = %q, want the workspace root %q; the session is wedged", got, root)
	}
	if _, unverified := tool.session.workDirState(); unverified {
		t.Fatal("the unverified flag survived the recovery")
	}
}

// Recovery must not touch a directory the shell has since confirmed, or an
// unrelated later failure would yank the session out of a working directory.
func TestRecoveryLeavesAConfirmedDirectoryAlone(t *testing.T) {
	root := t.TempDir()
	tool := newBoundedBashTool(t, root)

	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tool.session.adoptUnverifiedWorkDir(sub)
	tool.updateSessionFromPWD(sub) // the shell reports it; it stats fine now

	if _, unverified := tool.session.workDirState(); unverified {
		t.Fatal("a directory confirmed by the shell is still flagged unverified")
	}
	tool.recoverUnverifiedWorkDir()
	if got := tool.sessionWorkDir(); got != sub {
		t.Fatalf("sessionWorkDir() = %q, want %q; recovery fired on a healthy session", got, sub)
	}
}

// Without a workspace boundary there is nowhere safe to fall back to.
func TestRecoveryIsANoOpWithoutAWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewBashTool(root)

	missing := filepath.Join(root, "gone")
	tool.session.adoptUnverifiedWorkDir(missing)
	tool.recoverUnverifiedWorkDir()

	if got := tool.sessionWorkDir(); got != missing {
		t.Fatalf("sessionWorkDir() = %q, want %q unchanged", got, missing)
	}
}

// The non-WSL path must be untouched: nothing sets the flag off Windows, so
// recovery must be inert and an ordinary SetWorkDir must never leave it set.
func TestOrdinaryWorkDirChangesAreNeverFlagged(t *testing.T) {
	root := t.TempDir()
	tool := newBoundedBashTool(t, root)

	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tool.session.SetWorkDir(sub)

	if _, unverified := tool.session.workDirState(); unverified {
		t.Fatal("SetWorkDir must clear the flag, or a stale flag would outlive the directory it described")
	}
	tool.recoverUnverifiedWorkDir()
	if got := tool.sessionWorkDir(); got != sub {
		t.Fatalf("recovery relocated a session it should not have touched: %q", got)
	}
}

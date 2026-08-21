//go:build !windows

package studio

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWriteConfigArchive_SkipsFIFOWithoutBlocking(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("projects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(cfgDir, "agent-events.pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}

	type outcome struct {
		count int
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		var archive bytes.Buffer
		count, err := writeConfigArchive(&archive, cfgDir)
		done <- outcome{count: count, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("writeConfigArchive: %v", got.err)
		}
		if got.count != 1 {
			t.Fatalf("filesCount=%d, want only config.yaml", got.count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writeConfigArchive blocked opening a FIFO")
	}
}

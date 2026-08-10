//go:build !windows

package studio

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadFileContentRejectsFIFOWithoutBlocking(t *testing.T) {
	s := newStudioForTest(t)
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	info, err := s.AddProject("FIFO", root)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.ReadFileContent(info.ID, "pipe")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as a regular text file")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO preview blocked waiting for a writer")
	}
	entries, err := s.ListDirectory(info.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "pipe" {
			t.Fatal("FIFO leaked into the text file browser")
		}
	}
}

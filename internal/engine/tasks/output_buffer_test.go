package tasks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeBufferPersistsPrivateOutput(t *testing.T) {
	var buffer safeBuffer
	preferred := filepath.Join(t.TempDir(), "task-output", "task-1.log")
	if err := buffer.SetOutputFile(preferred); err != nil {
		t.Fatal(err)
	}
	if n, err := buffer.Write([]byte("task output")); err != nil || n != len("task output") {
		t.Fatalf("Write=%d, %v", n, err)
	}
	path := buffer.FilePath()
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "task output" {
		t.Fatalf("persisted output=%q err=%v", got, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%v err=%v, want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode=%v err=%v, want 0700", info, err)
	}
}

func TestSafeBufferReportsOutputFileFailure(t *testing.T) {
	var buffer safeBuffer
	if err := buffer.SetOutputFile(filepath.Join(t.TempDir(), "task.log")); err != nil {
		t.Fatal(err)
	}
	if err := buffer.file.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := buffer.Write([]byte("buffered")); err == nil || n != len("buffered") {
		t.Fatalf("Write=%d, %v; want full in-memory count and file error", n, err)
	}
	if got := buffer.String(); got != "buffered" {
		t.Fatalf("in-memory output=%q", got)
	}
	_ = buffer.Close()
}

func TestTaskStartRejectsUnsafeID(t *testing.T) {
	task := NewTask("../../escape", "echo unsafe", t.TempDir())
	if err := task.Start(context.Background()); err == nil {
		t.Fatal("Start accepted an unsafe task ID")
	}
	if task.GetStatus() != StatusPending {
		t.Fatalf("status=%s, want pending after validation failure", task.GetStatus())
	}
}

func TestTaskInfoExposesOutputStreamSnapshot(t *testing.T) {
	task := NewTask("task-1", "echo", t.TempDir())
	path := filepath.Join(task.WorkDir, ".gokin", "task-output", "task-1.log")
	if err := task.Output.SetOutputFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Output.Write([]byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	info := task.GetInfo()
	if info.Output != "snapshot" || info.OutputFile != task.Output.FilePath() || info.TotalBytes != 8 {
		t.Fatalf("info=%+v", info)
	}
	if err := task.Output.Close(); err != nil {
		t.Fatal(err)
	}
}

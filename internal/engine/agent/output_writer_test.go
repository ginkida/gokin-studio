package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentOutputWriterUsesPrivateFileAndBoundedReads(t *testing.T) {
	workDir := t.TempDir()
	writer := NewAgentOutputWriter(workDir, "agent-1")
	if writer.FilePath() == "" {
		t.Fatal("writer did not create an output file")
	}
	payload := bytes.Repeat([]byte("x"), maxAgentOutputReadBytes+137)
	if n, err := writer.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write=%d, %v; want %d, nil", n, err, len(payload))
	}

	first, next, err := writer.ReadFrom(0)
	if err != nil || len(first) != maxAgentOutputReadBytes || next != maxAgentOutputReadBytes {
		t.Fatalf("first read len=%d next=%d err=%v", len(first), next, err)
	}
	second, finalOffset, err := writer.ReadFrom(next)
	if err != nil || len(second) != 137 || finalOffset != int64(len(payload)) {
		t.Fatalf("second read len=%d next=%d err=%v", len(second), finalOffset, err)
	}
	if _, _, err := writer.ReadFrom(-1); err == nil {
		t.Fatal("negative offset was accepted")
	}

	path := writer.FilePath()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%v err=%v, want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode=%v err=%v, want 0700", info, err)
	}
}

func TestAgentOutputWriterRejectsUnsafeID(t *testing.T) {
	workDir := t.TempDir()
	writer := NewAgentOutputWriter(workDir, "../../escape")
	if writer.FilePath() != "" {
		t.Fatalf("unsafe ID created %s", writer.FilePath())
	}
	if err := writer.WriteString("memory fallback"); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != "memory fallback" {
		t.Fatalf("fallback output=%q", got)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "escape.log")); !os.IsNotExist(err) {
		t.Fatalf("unsafe ID escaped output directory: %v", err)
	}
}

func TestAgentOutputWriterReportsFileWriteFailure(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "agent-1")
	if writer.file == nil {
		t.Fatal("writer did not create an output file")
	}
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write([]byte("still buffered")); err == nil || n != len("still buffered") {
		t.Fatalf("Write=%d, %v; want full in-memory count and file error", n, err)
	}
	if got := writer.String(); got != "still buffered" {
		t.Fatalf("in-memory fallback=%q", got)
	}
	_ = writer.Close()
}

func TestAgentOutputBufferPublishesBeforeClose(t *testing.T) {
	writer := NewAgentOutputWriter(t.TempDir(), "agent-1")
	buffer := newAgentOutputBuffer(writer)
	buffer.WriteString("first page")
	if err := buffer.Err(); err != nil {
		t.Fatal(err)
	}
	data, next, err := writer.ReadFrom(0)
	if err != nil || data != "first page" || next != int64(len("first page")) {
		t.Fatalf("live read=(%q, %d, %v)", data, next, err)
	}
	if buffer.String() != "first page" || buffer.Len() != len("first page") {
		t.Fatalf("bounded buffer=%q len=%d", buffer.String(), buffer.Len())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

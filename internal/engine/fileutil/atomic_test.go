package fileutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAtomicWriteReplacesCompleteFileWithPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("new-state-"), 4096)
	if err := AtomicWrite(path, want, 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("published bytes len=%d err=%v, want %d exact bytes", len(got), err, len(want))
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("published mode=%#o, want 0600", gotMode)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".gokin-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("atomic write leaked temps=%v err=%v", temps, err)
	}
}

func TestAtomicWritePublishFailurePreservesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing-directory")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(target, []byte("replacement"), 0o600); err == nil {
		t.Fatal("AtomicWrite unexpectedly replaced a directory")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("existing target changed: %q err=%v", got, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".gokin-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("failed atomic write leaked temps=%v err=%v", temps, err)
	}
}

func TestSafeFilenameComponent(t *testing.T) {
	for _, valid := range []string{"agent-1", "plan_2", "checkpoint.3"} {
		if !SafeFilenameComponent(valid) {
			t.Errorf("SafeFilenameComponent(%q)=false", valid)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../escape", `..\\escape`, "a/b", "a:b", "with space", "trailing.", "con", "COM1.log"} {
		if SafeFilenameComponent(invalid) {
			t.Errorf("SafeFilenameComponent(%q)=true", invalid)
		}
	}
}

func TestReadRegularFileLimitedRejectsOversizeAndNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("exact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRegularFileLimited(path, 5); err != nil || string(got) != "exact" {
		t.Fatalf("exact limited read=%q err=%v", got, err)
	}
	if _, err := ReadRegularFileLimited(path, 4); err == nil {
		t.Fatal("oversized file was accepted")
	}
	if _, err := ReadRegularFileLimited(dir, 1024); err == nil {
		t.Fatal("directory was accepted as a regular file")
	}
	if _, err := ReadRegularFileLimited(path, -1); err == nil {
		t.Fatal("negative size limit was accepted")
	}
}

func TestReadRegularFileLimitedRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	link := filepath.Join(dir, "state.json")
	if err := os.WriteFile(target, []byte("do not disclose"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if data, err := ReadRegularFileLimited(link, 1024); err == nil {
		t.Fatalf("symlink was read: %q", data)
	}
	if RegularFileExists(link) {
		t.Fatal("symlink reported as a regular stored file")
	}
}

func TestReadRegularFileRangeIsBoundedAndRejectsInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, next, total, err := ReadRegularFileRange(path, 2, 4)
	if err != nil || string(data) != "2345" || next != 6 || total != 10 {
		t.Fatalf("range read=(%q, %d, %d, %v), want (2345, 6, 10, nil)", data, next, total, err)
	}
	data, next, total, err = ReadRegularFileRange(path, 10, 4)
	if err != nil || len(data) != 0 || next != 10 || total != 10 {
		t.Fatalf("EOF range read=(%q, %d, %d, %v)", data, next, total, err)
	}
	if _, _, _, err := ReadRegularFileRange(path, -1, 4); err == nil {
		t.Fatal("negative offset was accepted")
	}
	if _, _, _, err := ReadRegularFileRange(path, 0, 0); err == nil {
		t.Fatal("zero read limit was accepted")
	}

	link := filepath.Join(dir, "linked.log")
	if err := os.Symlink(path, link); err == nil {
		if _, _, _, err := ReadRegularFileRange(link, 0, 4); err == nil {
			t.Fatal("symlink was accepted for ranged read")
		}
	}
}

func TestCreatePrivateOutputFileDoesNotOverwriteCollision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "output")
	preferred := filepath.Join(dir, "agent-1.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preferred, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, actual, err := CreatePrivateOutputFile(preferred)
	if err != nil {
		t.Fatal(err)
	}
	if actual == preferred {
		t.Fatal("existing preferred path was reused")
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(preferred); err != nil || string(got) != "existing" {
		t.Fatalf("preferred file=%q err=%v, want existing", got, err)
	}
	if got, err := os.ReadFile(actual); err != nil || string(got) != "new" {
		t.Fatalf("new output=%q err=%v, want new", got, err)
	}
	if info, err := os.Stat(actual); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new output mode=%v err=%v, want 0600", info, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode=%v err=%v, want 0700", info, err)
	}
}

func TestLatestFileWriterCoalescesOlderQueuedGeneration(t *testing.T) {
	var writer LatestFileWriter
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var mu sync.Mutex
	var published []string
	writer.writeFile = func(_ string, data []byte, _ os.FileMode) error {
		call := calls.Add(1)
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		mu.Lock()
		published = append(published, string(data))
		mu.Unlock()
		return nil
	}
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	done3 := make(chan error, 1)
	writer.Schedule("state", []byte("one"), 0o600, func(err error) { done1 <- err })
	<-firstStarted
	writer.Schedule("state", []byte("two"), 0o600, func(err error) { done2 <- err })
	writer.Schedule("state", []byte("three"), 0o600, func(err error) { done3 <- err })
	close(releaseFirst)
	for i, done := range []<-chan error{done1, done2, done3} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("completion %d: %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("completion %d timed out", i)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 2 || published[0] != "one" || published[1] != "three" {
		t.Fatalf("published=%v, want [one three] with queued generation two coalesced", published)
	}
}

func TestLatestFileWriterSynchronousWriteSupersedesScheduledFailure(t *testing.T) {
	var writer LatestFileWriter
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var mu sync.Mutex
	final := ""
	writer.writeFile = func(_ string, data []byte, _ os.FileMode) error {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return errors.New("obsolete write failed")
		}
		mu.Lock()
		final = string(data)
		mu.Unlock()
		return nil
	}
	oldDone := make(chan error, 1)
	writer.Schedule("state", []byte("old"), 0o600, func(err error) { oldDone <- err })
	<-firstStarted
	newDone := make(chan error, 1)
	go func() { newDone <- writer.Write("state", []byte("new"), 0o600) }()
	deadline := time.Now().Add(time.Second)
	for writer.latest.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if writer.latest.Load() < 2 {
		t.Fatal("new synchronous write did not reserve its generation")
	}
	close(releaseFirst)
	if err := <-oldDone; err != nil {
		t.Fatalf("superseded scheduled failure leaked to callback: %v", err)
	}
	if err := <-newDone; err != nil {
		t.Fatalf("new synchronous write: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if final != "new" {
		t.Fatalf("final=%q, want new", final)
	}
}

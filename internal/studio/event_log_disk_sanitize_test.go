package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFromDisk_SanitizesLegacyEntries is the iter 900+ security
// regression guard. Simulates an events.log file written BEFORE iter 870+
// redaction was added — i.e., contains unredacted secrets. After
// LoadFromDisk, those secrets MUST be sanitized in the ring buffer so
// they don't leak via Snapshot, CSV export, or new auto-backups.
func TestLoadFromDisk_SanitizesLegacyEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	// Hand-craft a legacy events.log with unredacted secrets — what a
	// pre-iter-870+ install might have on disk after a careless log call.
	const secret1 = "sk-LEGACY-2345678901234567"
	const secret2 = "Bearer abcdefghijklmnop"
	content := `{"timestampMs":1,"level":"info","source":"test","message":"login key=` + secret1 + `","count":1}` + "\n" +
		`{"timestampMs":2,"level":"error","source":"agent","message":"Authorization: ` + secret2 + ` failed","count":1}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	l := NewEventLog()
	if err := l.LoadFromDisk(path); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("got %d entries, want 2", len(snap))
	}

	// SECURITY: neither secret string may appear in any entry.
	for i, e := range snap {
		if strings.Contains(e.Message, secret1) {
			t.Errorf("entry %d leaked sk-key secret: %q", i, e.Message)
		}
		if strings.Contains(e.Message, secret2) {
			t.Errorf("entry %d leaked Bearer secret: %q", i, e.Message)
		}
	}
	// And redaction markers should be present in the corresponding entries.
	if !strings.Contains(snap[0].Message, "<redacted:sk-key>") {
		t.Errorf("entry 0 missing sk-key redaction marker; got %q", snap[0].Message)
	}
	if !strings.Contains(snap[1].Message, "<redacted:bearer>") {
		t.Errorf("entry 1 missing bearer redaction marker; got %q", snap[1].Message)
	}
}

func TestLoadFromDisk_AlreadyRedactedPassesThrough(t *testing.T) {
	// Idempotency check: if disk content was already redacted (current
	// behavior post-iter-870+ writes), LoadFromDisk should not double-redact.
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	content := `{"timestampMs":1,"level":"info","source":"test","message":"login key=<redacted:sk-key> ok","count":1}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	l := NewEventLog()
	if err := l.LoadFromDisk(path); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("got %d entries, want 1", len(snap))
	}
	if snap[0].Message != "login key=<redacted:sk-key> ok" {
		t.Errorf("idempotency broken; got %q", snap[0].Message)
	}
}

// TestExportLogsCSV_SanitizesLegacyDiskEntries — the regression closes
// across the whole export path: legacy unredacted disk → LoadFromDisk →
// in-memory ring → ExportLogsCSV must not leak the secret.
func TestExportLogsCSV_SanitizesLegacyDiskEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	const secret = "sk-LEGACY-9876543210987654321"
	content := `{"timestampMs":1,"level":"info","source":"frontend","message":"unhandled rejection: ` + secret + `"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStudio()
	s.ensureEventLog()
	if err := s.eventLog.LoadFromDisk(path); err != nil {
		t.Fatal(err)
	}
	csv := s.ExportLogsCSV()
	if strings.Contains(csv, secret) {
		t.Errorf("CSV LEAKED legacy secret: %q", csv)
	}
	if !strings.Contains(csv, "<redacted:sk-key>") {
		t.Errorf("CSV missing redaction marker; got %q", csv)
	}
}

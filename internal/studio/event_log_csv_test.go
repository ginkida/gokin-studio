package studio

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

// parseCSV decodes the CSV string into [][]string for assertion ease.
func parseCSV(t *testing.T, s string) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(s))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parseCSV failed: %v\ninput=%q", err, s)
	}
	return rows
}

func TestExportLogsCSV_EmptyLog(t *testing.T) {
	s := NewStudio()
	csvStr := s.ExportLogsCSV()
	rows := parseCSV(t, csvStr)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (header only)", len(rows))
	}
	header := rows[0]
	want := []string{"timestamp", "level", "source", "count", "message"}
	if len(header) != len(want) {
		t.Fatalf("header has %d cols, want %d", len(header), len(want))
	}
	for i, h := range header {
		if h != want[i] {
			t.Errorf("col %d=%q, want %q", i, h, want[i])
		}
	}
}

func TestExportLogsCSV_ContainsEntries(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "test", "first event")
	s.LogEvent("warn", "config", "second event")
	s.LogEvent("error", "agent", "third event")

	csvStr := s.ExportLogsCSV()
	rows := parseCSV(t, csvStr)
	if len(rows) != 4 { // 1 header + 3 entries
		t.Fatalf("got %d rows, want 4; csv=%q", len(rows), csvStr)
	}
	// Header verified separately. Row 1 should be the first entry.
	if rows[1][1] != "info" || rows[1][2] != "test" || rows[1][4] != "first event" {
		t.Errorf("row 1 wrong: %+v", rows[1])
	}
	if rows[2][1] != "warn" || rows[2][4] != "second event" {
		t.Errorf("row 2 wrong: %+v", rows[2])
	}
	if rows[3][1] != "error" || rows[3][4] != "third event" {
		t.Errorf("row 3 wrong: %+v", rows[3])
	}
	// Count column is "1" for each since none deduped.
	for i := 1; i < len(rows); i++ {
		if rows[i][3] != "1" {
			t.Errorf("row %d count=%q, want '1'", i, rows[i][3])
		}
	}
}

func TestExportLogsCSV_PreservesCountFromDedup(t *testing.T) {
	s := NewStudio()
	// Same message three times → dedup collapses to one entry with count=3.
	s.LogEvent("error", "x", "same message")
	s.LogEvent("error", "x", "same message")
	s.LogEvent("error", "x", "same message")
	csvStr := s.ExportLogsCSV()
	rows := parseCSV(t, csvStr)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (header + 1 deduped); csv=%q", len(rows), csvStr)
	}
	if rows[1][3] != "3" {
		t.Errorf("count=%q, want '3' after triple-log dedup", rows[1][3])
	}
}

func TestExportLogsCSV_EscapesSpecialChars(t *testing.T) {
	s := NewStudio()
	tricky := `message with, comma "and quotes" and
newline`
	s.LogEvent("info", "test", tricky)
	csvStr := s.ExportLogsCSV()
	// Parsing must round-trip — encoding/csv handles all the escaping.
	rows := parseCSV(t, csvStr)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2; csv=%q", len(rows), csvStr)
	}
	if rows[1][4] != tricky {
		t.Errorf("message round-trip failed:\ngot  %q\nwant %q", rows[1][4], tricky)
	}
}

func TestExportLogsCSV_TimestampIsRFC3339(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "test", "msg")
	csvStr := s.ExportLogsCSV()
	rows := parseCSV(t, csvStr)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	ts := rows[1][0]
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", ts, err)
	}
}

func TestExportLogsCSV_PreservesRedaction(t *testing.T) {
	// THE SECURITY guard: if the in-memory log is redacted (iter 870+),
	// CSV export must keep the redaction in place. We're not allowed to
	// un-redact during export.
	s := NewStudio()
	const secret = "sk-DO-NOT-EXPORT-2345678901234567"
	s.LogEvent("error", "test", "leak attempt: "+secret)
	csvStr := s.ExportLogsCSV()
	if strings.Contains(csvStr, secret) {
		t.Errorf("CSV export LEAKED redacted secret: %q", csvStr)
	}
	if !strings.Contains(csvStr, "<redacted:sk-key>") {
		t.Errorf("CSV missing redaction marker; csv=%q", csvStr)
	}
}

func TestExportLogsCSV_LazyInitSafe(t *testing.T) {
	// Bare &Studio{} (no NewStudio) should still work for export thanks to
	// ensureEventLog's sync.Once init pattern from iter 710+.
	s := &Studio{}
	csvStr := s.ExportLogsCSV()
	rows := parseCSV(t, csvStr)
	if len(rows) != 1 {
		t.Errorf("expected header-only for fresh &Studio{}; got %d rows", len(rows))
	}
}

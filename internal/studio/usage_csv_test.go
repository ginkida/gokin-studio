package studio

import (
	"strings"
	"testing"
)

// TestExportProjectUsageCSV_Basic builds a minimal project + verifies the
// CSV has the expected header + one session row + the totals row.
func TestExportProjectUsageCSV_Basic(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "CSVTest")

	out, err := s.ExportProjectUsageCSV(info.ID)
	if err != nil {
		t.Fatalf("ExportProjectUsageCSV: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + session + total), got %d: %v", len(lines), lines)
	}
	// Header sanity.
	if !strings.Contains(lines[0], "Session") || !strings.Contains(lines[0], "Cost (USD)") {
		t.Errorf("header row missing expected columns: %q", lines[0])
	}
	// Last line is the TOTAL row.
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "TOTAL,") {
		t.Errorf("last row should start with TOTAL, got %q", last)
	}
}

// TestExportProjectUsageCSV_WithUsage seeds usage data on a session and
// confirms it lands in the CSV with the right values.
func TestExportProjectUsageCSV_WithUsage(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "WithUsage")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	sess := p.sessions["default"]
	p.mu.Unlock()
	sess.mu.Lock()
	sess.Name = "MainChat"
	sess.usage = &SessionUsage{
		TotalCostUSD:      0.1234,
		TotalInputTokens:  1000,
		TotalOutputTokens: 500,
		TotalCacheTokens:  200,
		TurnCount:         3,
		LastTurnAt:        1700000000000, // ~2023-11-14
	}
	sess.mu.Unlock()

	out, err := s.ExportProjectUsageCSV(info.ID)
	if err != nil {
		t.Fatalf("ExportProjectUsageCSV: %v", err)
	}
	if !strings.Contains(out, "MainChat") {
		t.Errorf("expected session name in CSV, got: %s", out)
	}
	if !strings.Contains(out, "0.1234") {
		t.Errorf("expected cost 0.1234 in CSV, got: %s", out)
	}
	if !strings.Contains(out, ",1000,") {
		t.Errorf("expected input tokens 1000 in CSV, got: %s", out)
	}
	// ISO timestamp from LastTurnAt.
	if !strings.Contains(out, "2023-11-14") && !strings.Contains(out, "2023-11-15") {
		t.Errorf("expected ISO timestamp from LastTurnAt in CSV, got: %s", out)
	}
}

// TestExportProjectUsageCSV_Validation covers reject paths.
func TestExportProjectUsageCSV_Validation(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ExportProjectUsageCSV(""); err == nil {
		t.Error("expected error for empty projectID")
	}
	if _, err := s.ExportProjectUsageCSV("no-such-id"); err == nil {
		t.Error("expected error for unknown project")
	}
}

// TestExportProjectUsageCSV_NeverUsedSession a session with no recorded
// usage should appear with zero values + empty "Last turn" cell.
func TestExportProjectUsageCSV_NeverUsedSession(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Fresh")
	out, err := s.ExportProjectUsageCSV(info.ID)
	if err != nil {
		t.Fatalf("ExportProjectUsageCSV: %v", err)
	}
	// Should contain a row with zero cost + zero counts.
	// Default-session row format: "Chat 1,0.0000,0,0,0,0,"
	if !strings.Contains(out, ",0.0000,0,0,0,0,") {
		t.Errorf("expected zero-usage row for default session, got: %s", out)
	}
}

// TestFmtCurrency confirms the helper formats with 4 decimals.
func TestFmtCurrency(t *testing.T) {
	cases := map[float64]string{
		0:        "0.0000",
		0.1234:   "0.1234",
		12.5:     "12.5000",
		1.23456:  "1.2346", // rounds to 4 decimals
		100:      "100.0000",
	}
	for in, want := range cases {
		if got := fmtCurrency(in); got != want {
			t.Errorf("fmtCurrency(%v) = %q, want %q", in, got, want)
		}
	}
}

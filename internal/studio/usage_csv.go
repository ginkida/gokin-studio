package studio

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// usage_csv.go (iter 600+) -- CSV export of per-project usage stats.
// Builds on iter 360+ ProjectUsageStats. Power users tracking spend in
// spreadsheets need a structured download — JSON is too verbose for that.
//
// Format: one row per session, plus a totals row at the bottom. Columns:
// Session, Cost(USD), Input tokens, Output tokens, Cache tokens, Turns,
// Last turn (ISO timestamp or empty for never-used).

// ExportProjectUsageCSV returns the per-session usage breakdown as a CSV
// string ready for download. Reuses ProjectUsageStats so the rendering
// stays consistent with the in-app modal.
func (s *Studio) ExportProjectUsageCSV(projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("projectID cannot be empty")
	}
	stats, err := s.ProjectUsageStats(projectID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	w := csv.NewWriter(&sb)

	// Header row.
	if err := w.Write([]string{
		"Session", "Cost (USD)", "Input tokens", "Output tokens",
		"Cache tokens", "Turns", "Last turn",
	}); err != nil {
		return "", fmt.Errorf("write header: %w", err)
	}

	// Per-session rows.
	for _, sess := range stats.Sessions {
		lastTurn := ""
		if sess.LastTurnAt > 0 {
			lastTurn = time.UnixMilli(sess.LastTurnAt).Format(time.RFC3339)
		}
		if err := w.Write([]string{
			sess.SessionName,
			fmtCurrency(sess.TotalCostUSD),
			strconv.Itoa(sess.TotalInputTokens),
			strconv.Itoa(sess.TotalOutputTokens),
			strconv.Itoa(sess.TotalCacheTokens),
			strconv.Itoa(sess.TurnCount),
			lastTurn,
		}); err != nil {
			return "", fmt.Errorf("write session row: %w", err)
		}
	}

	// Totals row at the bottom — explicitly labelled so a spreadsheet
	// import can identify and exclude it from per-row aggregations if
	// needed. Empty "Last turn" cell since the total has no single time.
	if err := w.Write([]string{
		"TOTAL",
		fmtCurrency(stats.TotalCostUSD),
		strconv.Itoa(stats.TotalInputTokens),
		strconv.Itoa(stats.TotalOutputTokens),
		strconv.Itoa(stats.TotalCacheTokens),
		strconv.Itoa(stats.TotalTurns),
		"",
	}); err != nil {
		return "", fmt.Errorf("write total row: %w", err)
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("flush csv: %w", err)
	}
	return sb.String(), nil
}

// fmtCurrency formats a USD float with 4 decimal places. Trailing zeros
// are kept (so a row of $0.0000 columns aligns visually) and there's no
// $-prefix because that would defeat numeric column sorting in
// spreadsheets — the column header makes the unit clear.
func fmtCurrency(usd float64) string {
	return strconv.FormatFloat(usd, 'f', 4, 64)
}

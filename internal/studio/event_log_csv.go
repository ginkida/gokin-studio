package studio

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"time"
)

// ExportLogsCSV returns the current in-memory event log (iter 710+) as a
// CSV string. Useful for support flows: "send me the last week of logs"
// becomes one button click + email attachment. Already-applied secret
// redaction (iter 870+) is preserved — the CSV contains the same scrubbed
// messages that the Logs viewer shows.
//
// Columns: timestamp (RFC3339), level, source, count, message. `count` is
// always ≥1 (we coerce the EventLogEntry.Count omitempty-zero case to 1
// for CSV readability). Messages are quoted by encoding/csv when they
// contain commas, quotes, or newlines — no special handling needed.
//
// Returns the header row even when the log is empty so the downloaded
// file is recognisable as a Gokin Studio log export.
func (s *Studio) ExportLogsCSV() string {
	s.ensureEventLog()
	snap := s.eventLog.Snapshot()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"timestamp", "level", "source", "count", "message"})
	for _, e := range snap {
		ts := time.UnixMilli(e.TimestampMs).Format(time.RFC3339)
		count := max(e.Count, 1)
		_ = w.Write([]string{
			ts,
			e.Level,
			e.Source,
			strconv.Itoa(count),
			e.Message,
		})
	}
	w.Flush()
	return buf.String()
}

package studio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	ReplayFileMaxBytes  = 8 << 20
	ReplayEventMaxBytes = 256 << 10
	ReplayMaxEvents     = 10_000
)

// ReplayEvent is a single event captured during a turn for crash recovery.
// Events are appended to {projectID}_{sessionID}.replay.jsonl as they happen;
// the file is deleted after the turn completes successfully.
type ReplayEvent struct {
	Type        string         `json:"type"` // user, tool_call, tool_result, assistant_text, thinking, complete
	Text        string         `json:"text,omitempty"`
	Tool        string         `json:"tool,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Success     *bool          `json:"success,omitempty"`
	TimestampMs int64          `json:"ts"`
}

// replayPath returns the file path for a session's replay log.
func replayPath(projectID, sessionID string) string {
	return filepath.Join(historyDir(), projectSessionStorageKey(projectID, sessionID)+".replay.jsonl")
}

// ReplayLogger serializes writes to a single session's replay log from the
// agent goroutine. One instance per in-flight SendMessage.
//
// Marking the logger closed (via Close or Complete) makes further Append
// calls no-ops. This prevents a tiny window where DeleteProject removes the
// replay file while an in-flight agent goroutine is still writing its last
// tool_result event — the goroutine would otherwise recreate the file.
type ReplayLogger struct {
	path         string
	mu           sync.Mutex
	closed       bool
	bytesWritten int64
}

// NewReplayLogger opens (or creates) the replay log for a session and
// truncates it — each new turn starts fresh. The caller is responsible for
// calling Complete() or Discard() when done.
func NewReplayLogger(projectID, sessionID string) *ReplayLogger {
	p := replayPath(projectID, sessionID)
	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		return &ReplayLogger{path: p, closed: true}
	}
	// Truncate any previous log; if the previous turn crashed, the caller
	// should have already surfaced it via LoadReplay before starting this one.
	f, err := openReplayFile(p)
	if err != nil {
		return &ReplayLogger{path: p, closed: true}
	}
	if err := f.Close(); err != nil {
		return &ReplayLogger{path: p, closed: true}
	}
	return &ReplayLogger{path: p}
}

func openReplayFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}

func openReplayAppend(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	return f, nil
}

// Append writes an event line to the replay log. Errors are swallowed because
// replay logging must never break the agent loop — a missing log is better
// than a broken turn.
func (r *ReplayLogger) Append(event ReplayEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	event.TimestampMs = time.Now().UnixMilli()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	if len(data) > ReplayEventMaxBytes || r.bytesWritten+int64(len(data)+1) > ReplayFileMaxBytes {
		return
	}
	f, err := openReplayAppend(r.path)
	if err != nil {
		return
	}
	line := append(data, '\n')
	n, writeErr := f.Write(line)
	_ = f.Close()
	if writeErr == nil && n == len(line) {
		r.bytesWritten += int64(n)
	}
}

// Complete removes the replay log and blocks further Appends. Call after a
// successful turn; the authoritative state now lives in the session history
// file.
func (r *ReplayLogger) Complete() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	_ = os.Remove(r.path)
}

// Close marks the logger closed without touching the file. Used when a turn
// was interrupted (error, ctx cancel) and we want to stop further writes but
// keep whatever was captured so far for the recovery banner.
func (r *ReplayLogger) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
}

// LoadReplay reads a session's replay log if it exists. Returns nil slice and
// nil error when the file is absent (the common, healthy case).
func LoadReplay(projectID, sessionID string) ([]ReplayEvent, error) {
	data, err := readRegularFileLimited(replayPath(projectID, sessionID), ReplayFileMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var events []ReplayEvent
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), ReplayEventMaxBytes+1)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt ReplayEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue // skip corrupt lines, keep going
		}
		events = append(events, evt)
		if len(events) >= ReplayMaxEvents {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// DiscardReplay removes a session's replay log without any checks. Called
// when the user opts to "discard" an interrupted turn from recovery UI.
func DiscardReplay(projectID, sessionID string) {
	_ = os.Remove(replayPath(projectID, sessionID))
}

// HasCompleteMarker returns true if the last event in the replay is a
// "complete" event. If true, the turn actually finished and we can drop the
// replay log as redundant on next read. In normal operation this shouldn't
// happen because Complete() removes the file, but it's a safety check.
func HasCompleteMarker(events []ReplayEvent) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "complete" {
			return true
		}
	}
	return false
}

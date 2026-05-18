package studio

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ReplayEvent is a single event captured during a turn for crash recovery.
// Events are appended to {projectID}_{sessionID}.replay.jsonl as they happen;
// the file is deleted after the turn completes successfully.
type ReplayEvent struct {
	Type      string         `json:"type"` // user, tool_call, tool_result, assistant_text, thinking, complete
	Text      string         `json:"text,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Success   *bool          `json:"success,omitempty"`
	TimestampMs int64        `json:"ts"`
}

// replayPath returns the file path for a session's replay log.
func replayPath(projectID, sessionID string) string {
	return filepath.Join(historyDir(), projectID+"_"+sessionID+".replay.jsonl")
}

// ReplayLogger serializes writes to a single session's replay log from the
// agent goroutine. One instance per in-flight SendMessage.
//
// Marking the logger closed (via Close or Complete) makes further Append
// calls no-ops. This prevents a tiny window where DeleteProject removes the
// replay file while an in-flight agent goroutine is still writing its last
// tool_result event — the goroutine would otherwise recreate the file.
type ReplayLogger struct {
	path   string
	mu     sync.Mutex
	closed bool
}

// NewReplayLogger opens (or creates) the replay log for a session and
// truncates it — each new turn starts fresh. The caller is responsible for
// calling Complete() or Discard() when done.
func NewReplayLogger(projectID, sessionID string) *ReplayLogger {
	_ = os.MkdirAll(historyDir(), 0o700)
	p := replayPath(projectID, sessionID)
	// Truncate any previous log; if the previous turn crashed, the caller
	// should have already surfaced it via LoadReplay before starting this one.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_ = f.Close()
	}
	return &ReplayLogger{path: p}
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
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
	_, _ = f.Write([]byte("\n"))
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
	f, err := os.Open(replayPath(projectID, sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []ReplayEvent
	sc := bufio.NewScanner(f)
	// Replay log lines can be long (tool results, bash output). Raise buffer
	// ceiling well beyond the default 64KB so we don't silently truncate.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
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

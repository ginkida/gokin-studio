package tools

import "testing"

func TestStagnationFingerprint_DistinguishesFilesForWriteEditDelete(t *testing.T) {
	for _, tool := range []string{"write", "edit", "delete"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"file_path": "/tmp/a.go"})
			b := stagnationFingerprint(tool, map[string]any{"file_path": "/tmp/b.go"})
			if a == b {
				t.Errorf("%s: different files produced same fingerprint (%q)", tool, a)
			}
			if a != "a.go" {
				t.Errorf("%s: fingerprint = %q, want basename a.go", tool, a)
			}
		})
	}
}

// TestStagnationFingerprint_ReadIncludesOffsetLimit pins the fix for the
// false-positive that users hit: paging through a ~3000-line file in 2000-line
// chunks produced the same fingerprint every time and tripped the 5-repeat
// stagnation abort. The fix encodes offset+limit into the fingerprint so
// forward progress through a file is visible.
func TestStagnationFingerprint_ReadIncludesOffsetLimit(t *testing.T) {
	p0 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 0, "limit": 2000})
	p1 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 2000, "limit": 2000})
	p2 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/tui.go", "offset": 4000, "limit": 2000})
	if p0 == p1 || p1 == p2 || p0 == p2 {
		t.Errorf("paged reads collapsed to same fingerprint: %q %q %q", p0, p1, p2)
	}

	// JSON unmarshal typically produces float64 for integer fields.
	pInt := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/x.go", "offset": 100, "limit": 50})
	pFloat := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/x.go", "offset": 100.0, "limit": 50.0})
	if pInt != pFloat {
		t.Errorf("int vs float64 produced different fingerprints: %q vs %q", pInt, pFloat)
	}

	noPaging1 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go"})
	noPaging2 := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go"})
	if noPaging1 != noPaging2 {
		t.Errorf("identical bare reads must share a fingerprint, got %q vs %q", noPaging1, noPaging2)
	}

	a := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/a.go", "offset": 0})
	b := stagnationFingerprint("read", map[string]any{"file_path": "/tmp/b.go", "offset": 0})
	if a == b {
		t.Errorf("different files produced same fingerprint: %q", a)
	}
}

// Edits targeting DIFFERENT parts of the same file must fingerprint differently.
func TestStagnationFingerprint_EditDistinguishesTargets(t *testing.T) {
	o1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func a()"})
	o2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func b()"})
	o1b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "old_string": "func a()"})
	if o1 == o2 {
		t.Errorf("different old_strings collapsed: %q", o1)
	}
	if o1 != o1b {
		t.Errorf("identical old_string edits must share a fingerprint: %q vs %q", o1, o1b)
	}

	l1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 10, "line_end": 15})
	l2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 50, "line_end": 55})
	l1b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "line_start": 10.0, "line_end": 15.0})
	if l1 == l2 {
		t.Errorf("different line ranges collapsed: %q", l1)
	}
	if l1 != l1b {
		t.Errorf("int vs float line ranges must match: %q vs %q", l1, l1b)
	}

	i0 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "insert_after_line": 0})
	i5 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "insert_after_line": 5})
	bare := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go"})
	if i0 == i5 || i0 == bare {
		t.Errorf("insert points not distinguished: i0=%q i5=%q bare=%q", i0, i5, bare)
	}
	if bare != "f.go" {
		t.Errorf("bare edit fingerprint = %q, want basename f.go", bare)
	}

	m1 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "edits": []any{map[string]any{"old_string": "a", "new_string": "b"}}})
	m2 := stagnationFingerprint("edit", map[string]any{"file_path": "/x/f.go", "edits": []any{map[string]any{"old_string": "c", "new_string": "d"}}})
	if m1 == m2 {
		t.Errorf("different multi-edit batches collapsed: %q", m1)
	}

	a := stagnationFingerprint("edit", map[string]any{"file_path": "/x/a.go", "line_start": 1, "line_end": 2})
	b := stagnationFingerprint("edit", map[string]any{"file_path": "/x/b.go", "line_start": 1, "line_end": 2})
	if a == b {
		t.Errorf("different files collapsed: %q", a)
	}
}

func TestStagnationFingerprint_BashStripsCdPrefix(t *testing.T) {
	withCd := stagnationFingerprint("bash", map[string]any{"command": "cd /some/long/path && go test ./..."})
	without := stagnationFingerprint("bash", map[string]any{"command": "go test ./..."})
	if withCd != without {
		t.Errorf("cd-prefixed (%q) and plain (%q) should fingerprint the same", withCd, without)
	}
}

func TestStagnationFingerprint_BashTruncatesLongCommands(t *testing.T) {
	long := stagnationFingerprint("bash", map[string]any{
		"command": "echo this command is considerably longer than sixty characters so it should be truncated by the fingerprint helper",
	})
	if len(long) > 60 {
		t.Errorf("bash fingerprint len = %d, want <= 60", len(long))
	}
}

func TestStagnationFingerprint_BashOnlyStripsCdWhenPrefix(t *testing.T) {
	// "&& cd" in the middle must NOT be stripped.
	cmd := "go test ./... && cd /tmp && ls"
	got := stagnationFingerprint("bash", map[string]any{"command": cmd})
	if got != cmd {
		t.Errorf("fingerprint = %q, want unchanged command", got)
	}
}

func TestStagnationFingerprint_GrepAndGlobUsePattern(t *testing.T) {
	g1 := stagnationFingerprint("grep", map[string]any{"pattern": "foo.*bar"})
	g2 := stagnationFingerprint("grep", map[string]any{"pattern": "baz"})
	if g1 == g2 || g1 != "foo.*bar" {
		t.Errorf("grep: g1=%q g2=%q", g1, g2)
	}
	gb := stagnationFingerprint("glob", map[string]any{"pattern": "**/*.go"})
	if gb != "**/*.go" {
		t.Errorf("glob fingerprint = %q", gb)
	}
}

// Regression: grep/glob fingerprints used to ignore the `path` argument, so
// searching the same pattern in different directories tripped the 5-repeat abort.
func TestStagnationFingerprint_GrepAndGlobIncludePath(t *testing.T) {
	for _, tool := range []string{"grep", "glob"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			b := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/ui"})
			if a == b {
				t.Errorf("same pattern in different paths must not collide: %q", a)
			}
			bare := stagnationFingerprint(tool, map[string]any{"pattern": "TODO"})
			if bare != "TODO" {
				t.Errorf("bare pattern fingerprint = %q, want TODO", bare)
			}
			same1 := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			same2 := stagnationFingerprint(tool, map[string]any{"pattern": "TODO", "path": "internal/app"})
			if same1 != same2 {
				t.Errorf("identical args must share fingerprint: %q vs %q", same1, same2)
			}
		})
	}
}

func TestStagnationFingerprint_CopyMoveUseSource(t *testing.T) {
	for _, tool := range []string{"copy", "move"} {
		got := stagnationFingerprint(tool, map[string]any{
			"source":      "/path/to/file.txt",
			"destination": "/other/file.txt",
		})
		if got != "file.txt->file.txt" {
			t.Errorf("%s fingerprint = %q, want file.txt->file.txt", tool, got)
		}
	}
}

func TestStagnationFingerprint_CopyMoveDifferentDest(t *testing.T) {
	fp1 := stagnationFingerprint("copy", map[string]any{
		"source":      "/src/file.txt",
		"destination": "/dst1/file.txt",
	})
	fp2 := stagnationFingerprint("copy", map[string]any{
		"source":      "/src/file.txt",
		"destination": "/dst2/other.txt",
	})
	if fp1 == fp2 {
		t.Errorf("copy to different destinations must not share a fingerprint, got %q for both", fp1)
	}
}

func TestStagnationFingerprint_WebFetchTruncatesURL(t *testing.T) {
	url := "https://example.com/very-long-path/that-exceeds-fifty-characters-eventually/nested/deep"
	got := stagnationFingerprint("web_fetch", map[string]any{"url": url})
	if len(got) > 50 {
		t.Errorf("web_fetch fingerprint len = %d, want <= 50", len(got))
	}
}

func TestStagnationFingerprint_WebSearchUsesQuery(t *testing.T) {
	got := stagnationFingerprint("web_search", map[string]any{"query": "golang test patterns"})
	if got != "golang test patterns" {
		t.Errorf("web_search fingerprint = %q", got)
	}
}

func TestStagnationFingerprint_UnknownToolReturnsEmpty(t *testing.T) {
	got := stagnationFingerprint("mystery_tool", map[string]any{"file_path": "/x"})
	if got != "" {
		t.Errorf("unknown tool should produce empty fingerprint, got %q", got)
	}
}

func TestStagnationFingerprint_MissingArgReturnsEmpty(t *testing.T) {
	if got := stagnationFingerprint("write", map[string]any{}); got != "" {
		t.Errorf("write: missing file_path should produce empty fingerprint, got %q", got)
	}
	if got := stagnationFingerprint("read", map[string]any{}); got != "" {
		t.Errorf("read: missing file_path should produce empty fingerprint, got %q", got)
	}
	if got := stagnationFingerprint("read", map[string]any{"file_path": ""}); got != "" {
		t.Errorf("read: empty file_path should produce empty fingerprint, got %q", got)
	}
	if got1, got2 := stagnationFingerprint("read", map[string]any{"offset": 0}),
		stagnationFingerprint("read", map[string]any{"offset": 100}); got1 != got2 || got1 != "" {
		t.Errorf("reads without file_path must share the empty fingerprint: got1=%q got2=%q", got1, got2)
	}
}

// Regression: run_tests/verify_code had no fingerprint, so narrowing a filter
// progressively (normal debugging) tripped the stagnation abort.
func TestStagnationFingerprint_RunTestsIncludesFilter(t *testing.T) {
	for _, tool := range []string{"run_tests", "verify_code"} {
		t.Run(tool, func(t *testing.T) {
			a := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestFoo"})
			b := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestBar"})
			if a == b {
				t.Errorf("same path with different filter must not collide: %q", a)
			}
			c := stagnationFingerprint(tool, map[string]any{"path": "internal/app", "filter": "TestFoo"})
			if a != c {
				t.Errorf("identical args must share fingerprint: %q vs %q", a, c)
			}
			if got := stagnationFingerprint(tool, map[string]any{}); got != "" {
				t.Errorf("empty args should produce empty fingerprint, got %q", got)
			}
		})
	}
}

func TestStagnationFingerprint_WrongArgTypeReturnsEmpty(t *testing.T) {
	got := stagnationFingerprint("write", map[string]any{"file_path": 42})
	if got != "" {
		t.Errorf("wrong type for file_path should produce empty fingerprint, got %q", got)
	}
}

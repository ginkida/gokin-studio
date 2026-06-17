package studio

import (
	"strings"
	"testing"
)

func TestCheckStagnation_NotEnoughEntries(t *testing.T) {
	patterns := []string{"read:a.go@0+0", "read:a.go@0+0", "read:a.go@0+0"}
	if checkStagnation(patterns, "read:a.go@0+0") {
		t.Error("should not trigger with < stagnationLimit entries")
	}
}

func TestCheckStagnation_Triggers(t *testing.T) {
	p := "read:main.go@0+0"
	patterns := make([]string, stagnationLimit)
	for i := range patterns {
		patterns[i] = p
	}
	if !checkStagnation(patterns, p) {
		t.Error("should trigger when last stagnationLimit are identical")
	}
}

func TestCheckStagnation_DifferentPatternBreaks(t *testing.T) {
	patterns := []string{
		"read:a.go@0+0",
		"read:a.go@0+0",
		"read:b.go@0+0", // different
		"read:a.go@0+0",
		"read:a.go@0+0",
	}
	if checkStagnation(patterns, "read:a.go@0+0") {
		t.Error("should not trigger when tail is broken")
	}
}

func TestStagnationFingerprint_ReadIncludesOffsetLimit(t *testing.T) {
	fp := stagnationFingerprint("read", map[string]any{
		"file_path": "/project/main.go",
		"offset":    float64(2000),
		"limit":     float64(1000),
	})
	if !strings.Contains(fp, "main.go") {
		t.Errorf("fingerprint should contain filename, got %q", fp)
	}
	if !strings.Contains(fp, "2000") {
		t.Errorf("fingerprint should contain offset, got %q", fp)
	}
}

func TestStagnationFingerprint_ReadDifferentOffsetNotSame(t *testing.T) {
	fp1 := stagnationFingerprint("read", map[string]any{"file_path": "big.go", "offset": float64(0), "limit": float64(2000)})
	fp2 := stagnationFingerprint("read", map[string]any{"file_path": "big.go", "offset": float64(2000), "limit": float64(2000)})
	if fp1 == fp2 {
		t.Errorf("different offsets should produce different fingerprints, both got %q", fp1)
	}
}

func TestStagnationFingerprint_EditDistinguishesByOldString(t *testing.T) {
	fp1 := stagnationFingerprint("edit", map[string]any{"file_path": "f.go", "old_string": "foo"})
	fp2 := stagnationFingerprint("edit", map[string]any{"file_path": "f.go", "old_string": "bar"})
	if fp1 == fp2 {
		t.Errorf("different old_strings should produce different edit fingerprints")
	}
}

func TestStagnationFingerprint_BashStripsPrefix(t *testing.T) {
	fp1 := stagnationFingerprint("bash", map[string]any{"command": "cd /project && go build ./..."})
	fp2 := stagnationFingerprint("bash", map[string]any{"command": "go build ./..."})
	if fp1 != fp2 {
		t.Errorf("bash fingerprint should strip 'cd /path && ' prefix: %q vs %q", fp1, fp2)
	}
}

func TestBuildStagnationMessage_ContainsLoopGuard(t *testing.T) {
	msg := buildStagnationMessage("read", map[string]any{"file_path": "util.go"}, 5)
	if !strings.Contains(msg, "Loop guard") {
		t.Errorf("stagnation message should contain 'Loop guard', got: %q", msg)
	}
	if !strings.Contains(msg, "Do not call it again") {
		t.Errorf("stagnation message should contain 'Do not call it again', got: %q", msg)
	}
}

func TestBuildStagnationMessage_EditOldStringHint(t *testing.T) {
	msg := buildStagnationMessage("edit", map[string]any{
		"file_path":  "a.go",
		"old_string": "func foo() {",
	}, 3)
	if !strings.Contains(msg, "whitespace-sensitive") {
		t.Errorf("edit stagnation message should mention whitespace-sensitive matching")
	}
}

func TestStagnationKey_NoFingerprint(t *testing.T) {
	// A tool with no recognized args returns just the tool name as the key.
	key := stagnationKey("unknown_tool", map[string]any{})
	if key != "unknown_tool" {
		t.Errorf("expected 'unknown_tool', got %q", key)
	}
}

func TestStagnationKey_WithFingerprint(t *testing.T) {
	key := stagnationKey("bash", map[string]any{"command": "go test ./..."})
	if !strings.HasPrefix(key, "bash:") {
		t.Errorf("bash key should have 'bash:' prefix, got %q", key)
	}
}

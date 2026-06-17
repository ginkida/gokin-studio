package tools

import (
	"context"
	"strings"
	"testing"
)

// ---------- SemanticValidatorRegistry ----------

type alwaysMatchValidator struct{}

func (v *alwaysMatchValidator) Name() string          { return "always" }
func (v *alwaysMatchValidator) Matches(_ string) bool { return true }
func (v *alwaysMatchValidator) Validate(_ context.Context, _ string, _ []byte, _ string) []ValidationWarning {
	return []ValidationWarning{{Validator: "always", Severity: "warning", File: "f.go", Line: 1, Message: "always fires"}}
}

type neverMatchValidator struct{}

func (v *neverMatchValidator) Name() string          { return "never" }
func (v *neverMatchValidator) Matches(_ string) bool { return false }
func (v *neverMatchValidator) Validate(_ context.Context, _ string, _ []byte, _ string) []ValidationWarning {
	return []ValidationWarning{{Validator: "never", Severity: "error", File: "f.go", Line: 1, Message: "never fires"}}
}

func TestSemanticValidatorRegistry_RunAll(t *testing.T) {
	r := NewSemanticValidatorRegistry()
	r.Register(&alwaysMatchValidator{})
	r.Register(&neverMatchValidator{})

	warns := r.RunAll(context.Background(), "foo.go", []byte(""), "")
	if len(warns) != 1 {
		t.Fatalf("RunAll returned %d warnings, want 1", len(warns))
	}
	if warns[0].Validator != "always" {
		t.Errorf("validator = %q, want always", warns[0].Validator)
	}
}

func TestFormatWarnings_Empty(t *testing.T) {
	if s := FormatWarnings(nil); s != "" {
		t.Errorf("FormatWarnings(nil) = %q, want empty", s)
	}
	if s := FormatWarnings([]ValidationWarning{}); s != "" {
		t.Errorf("FormatWarnings([]) = %q, want empty", s)
	}
}

func TestFormatWarnings_AllSeverities(t *testing.T) {
	warns := []ValidationWarning{
		{Validator: "v1", Severity: "error", File: "a.go", Line: 3, Message: "msg-error"},
		{Validator: "v2", Severity: "warning", File: "b.go", Line: 0, Message: "msg-warning"},
		{Validator: "v3", Severity: "info", File: "c.go", Line: 7, Message: "msg-info"},
	}
	s := FormatWarnings(warns)

	for _, want := range []string{"[smart-validation]", "ERROR", "WARNING", "INFO", "msg-error", "msg-warning", "msg-info"} {
		if !strings.Contains(s, want) {
			t.Errorf("FormatWarnings missing %q in:\n%s", want, s)
		}
	}
}

func TestFormatWarnings_LineNumber(t *testing.T) {
	warns := []ValidationWarning{
		{Validator: "v", Severity: "warning", File: "foo.go", Line: 42, Message: "has line"},
		{Validator: "v", Severity: "info", File: "bar.go", Line: 0, Message: "no line"},
	}
	s := FormatWarnings(warns)

	if !strings.Contains(s, "foo.go:42") {
		t.Errorf("expected foo.go:42 in output:\n%s", s)
	}
	if strings.Contains(s, "bar.go:0") {
		t.Errorf("unexpected bar.go:0 in output (line 0 should be omitted):\n%s", s)
	}
}

func TestDefaultSemanticValidators_FourDistinctNames(t *testing.T) {
	vs := DefaultSemanticValidators()
	if len(vs) < 4 {
		t.Fatalf("DefaultSemanticValidators() returned %d, want at least 4", len(vs))
	}
	seen := make(map[string]bool)
	for _, v := range vs {
		name := v.Name()
		if seen[name] {
			t.Errorf("duplicate validator name: %q", name)
		}
		seen[name] = true
	}
}

// ---------- ExtractFilePaths ----------

func TestExtractFilePaths_Empty(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{})
	if len(paths) != 0 {
		t.Errorf("expected empty, got %v", paths)
	}
	paths = ExtractFilePaths(nil)
	if len(paths) != 0 {
		t.Errorf("expected empty for nil, got %v", paths)
	}
}

func TestExtractFilePaths_FilePathKey(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{"file_path": "foo.go"})
	if len(paths) != 1 || paths[0] != "foo.go" {
		t.Errorf("got %v, want [foo.go]", paths)
	}
}

func TestExtractFilePaths_PathKey(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{"path": "bar.go"})
	if len(paths) != 1 || paths[0] != "bar.go" {
		t.Errorf("got %v, want [bar.go]", paths)
	}
}

func TestExtractFilePaths_SourceAndDestination(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{"source": "a.go", "destination": "b.go"})
	if len(paths) != 2 {
		t.Fatalf("got %v (len=%d), want 2 paths", paths, len(paths))
	}
}

func TestExtractFilePaths_EmptyStringExcluded(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{"file_path": "", "path": "ok.go"})
	if len(paths) != 1 || paths[0] != "ok.go" {
		t.Errorf("got %v, want [ok.go]", paths)
	}
}

func TestExtractFilePaths_UnknownKeyIgnored(t *testing.T) {
	paths := ExtractFilePaths(map[string]any{"unknown_key": "secret.go"})
	if len(paths) != 0 {
		t.Errorf("expected empty for unknown key, got %v", paths)
	}
}

// ---------- GoQualityValidator ----------

func TestGoQualityValidator_Matches(t *testing.T) {
	v := &GoQualityValidator{}
	if !v.Matches("foo.go") {
		t.Error("Matches(foo.go) should be true")
	}
	if v.Matches("foo_test.go") {
		t.Error("Matches(foo_test.go) should be false")
	}
	if v.Matches("foo.sh") {
		t.Error("Matches(foo.sh) should be false")
	}
}

func TestGoQualityValidator_ResourceLeak(t *testing.T) {
	src := `package main

func main() {
	f, _ := os.Open("file.txt")
	fmt.Println(f)
	fmt.Println("done")
}
`
	v := &GoQualityValidator{}
	warns := v.Validate(context.Background(), "main.go", []byte(src), "")
	found := false
	for _, w := range warns {
		if strings.Contains(w.Message, "resource leak") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected resource-leak warning, got: %v", warns)
	}
}

func TestGoQualityValidator_NoLeakWithDefer(t *testing.T) {
	src := `package main

func main() {
	f, _ := os.Open("file.txt")
	defer f.Close()
}
`
	v := &GoQualityValidator{}
	warns := v.Validate(context.Background(), "main.go", []byte(src), "")
	for _, w := range warns {
		if strings.Contains(w.Message, "resource leak") {
			t.Errorf("unexpected resource-leak warning when defer Close() is present: %v", w)
		}
	}
}

func TestGoQualityValidator_DeprecatedIoutil(t *testing.T) {
	src := `package main
import "io/ioutil"
func main() {
	_ = ioutil.ReadFile("x")
}
`
	v := &GoQualityValidator{}
	warns := v.Validate(context.Background(), "main.go", []byte(src), "")
	found := false
	for _, w := range warns {
		if strings.Contains(w.Message, "deprecated") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ioutil deprecated warning, got: %v", warns)
	}
}

func TestGoQualityValidator_CleanSource(t *testing.T) {
	src := `package main
func main() { println("hello") }
`
	v := &GoQualityValidator{}
	warns := v.Validate(context.Background(), "main.go", []byte(src), "")
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings on clean source, got %d: %v", len(warns), warns)
	}
}

// ---------- SecurityValidator ----------

func TestSecurityValidator_Matches(t *testing.T) {
	v := &SecurityValidator{}
	if !v.Matches("foo.go") {
		t.Error("Matches(foo.go) should be true")
	}
	if v.Matches("foo_test.go") {
		t.Error("Matches(foo_test.go) should be false")
	}
	if v.Matches("foo.txt") {
		t.Error("Matches(foo.txt) should be false")
	}
}

func TestSecurityValidator_HardcodedKey(t *testing.T) {
	// Use a key long enough to pass the 20+ char threshold
	src := `package main
var apiKey = "sk-someRealKey1234567890abc"
`
	v := &SecurityValidator{}
	warns := v.Validate(context.Background(), "config.go", []byte(src), "")
	if len(warns) == 0 {
		t.Error("expected security warning for hardcoded API key, got none")
	}
}

func TestSecurityValidator_CommentSkipped(t *testing.T) {
	src := `package main
// api_key = "sk-xxx1234567890abcdefghij"
`
	v := &SecurityValidator{}
	warns := v.Validate(context.Background(), "config.go", []byte(src), "")
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings for commented key, got %d: %v", len(warns), warns)
	}
}

func TestSecurityValidator_OsGetenvSkipped(t *testing.T) {
	src := `package main
var apiKey = os.Getenv("API_KEY")
`
	v := &SecurityValidator{}
	warns := v.Validate(context.Background(), "config.go", []byte(src), "")
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings for os.Getenv, got %d: %v", len(warns), warns)
	}
}

// ---------- ShellValidator ----------

func TestShellValidator_Matches(t *testing.T) {
	v := &ShellValidator{}
	if !v.Matches("deploy.sh") {
		t.Error("Matches(deploy.sh) should be true")
	}
	if v.Matches("foo.go") {
		t.Error("Matches(foo.go) should be false")
	}
}

func TestShellValidator_ShebangAndSetE_NoWarnings(t *testing.T) {
	src := "#!/bin/bash\nset -euo pipefail\necho hi\necho there\necho done\necho fin\n"
	v := &ShellValidator{}
	warns := v.Validate(context.Background(), "deploy.sh", []byte(src), "")
	for _, w := range warns {
		if strings.Contains(w.Message, "shebang") || strings.Contains(w.Message, "set -e") {
			t.Errorf("unexpected warning with shebang+set -e: %v", w)
		}
	}
}

func TestShellValidator_MissingSetE(t *testing.T) {
	// More than 5 lines, no set -e
	src := "#!/bin/bash\necho a\necho b\necho c\necho d\necho e\necho f\n"
	v := &ShellValidator{}
	warns := v.Validate(context.Background(), "deploy.sh", []byte(src), "")
	found := false
	for _, w := range warns {
		if strings.Contains(w.Message, "set -e") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'set -e' warning, got: %v", warns)
	}
}

func TestShellValidator_UnquotedRmRf(t *testing.T) {
	src := "#!/bin/bash\nset -e\nrm -rf $DIR\necho done\necho a\necho b\n"
	v := &ShellValidator{}
	warns := v.Validate(context.Background(), "deploy.sh", []byte(src), "")
	found := false
	for _, w := range warns {
		if strings.Contains(w.Message, "unquoted variable") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected unquoted-variable warning for rm -rf $VAR, got: %v", warns)
	}
}

func TestShellValidator_ShortScriptNoShebangWarning(t *testing.T) {
	// ≤3 lines — shebang warning should not fire
	src := "echo hello\necho world\n"
	v := &ShellValidator{}
	warns := v.Validate(context.Background(), "tiny.sh", []byte(src), "")
	for _, w := range warns {
		if strings.Contains(w.Message, "shebang") {
			t.Errorf("unexpected shebang warning on short script: %v", w)
		}
	}
}

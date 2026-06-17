package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedGoPackage creates a small Go package in dir and returns paths to each file.
func seedGoPackage(t *testing.T, dir string) (fooPath, barPath string) {
	t.Helper()
	fooPath = filepath.Join(dir, "foo.go")
	barPath = filepath.Join(dir, "bar.go")
	if err := os.WriteFile(fooPath, []byte(`package testpkg

// MyFunc is a sample function.
func MyFunc() string {
	return "hello"
}

// MyType is a sample type.
type MyType struct {
	Value int
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(barPath, []byte(`package testpkg

import "fmt"

// CallMyFunc calls MyFunc for demonstration.
func CallMyFunc() {
	result := MyFunc()
	fmt.Println(result)
	var x MyType
	x.Value = 42
	_ = x
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

// ── GoToDefinitionTool ──────────────────────────────────────────────────────

func TestGoToDefinitionTool_Name(t *testing.T) {
	tool := NewGoToDefinitionTool("")
	if tool.Name() != "go_to_definition" {
		t.Fatalf("Name() = %q, want %q", tool.Name(), "go_to_definition")
	}
}

func TestGoToDefinitionTool_Declaration(t *testing.T) {
	tool := NewGoToDefinitionTool("")
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("Declaration() returned nil")
	}
	if decl.Parameters.Properties["symbol"] == nil {
		t.Fatal("declaration missing 'symbol' property")
	}
	if decl.Parameters.Properties["file"] == nil {
		t.Fatal("declaration missing 'file' property")
	}
}

func TestGoToDefinitionTool_MissingSymbol(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewGoToDefinitionTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for missing symbol")
	}
}

func TestGoToDefinitionTool_ASTFallback_FindsFunction(t *testing.T) {
	dir := resolvedTempDir(t)
	fooPath, _ := seedGoPackage(t, dir)
	tool := NewGoToDefinitionTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "MyFunc",
		"file":   fooPath,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "MyFunc") {
		t.Errorf("result should contain 'MyFunc', got: %s", res.Content)
	}
}

func TestGoToDefinitionTool_ASTFallback_FindsType(t *testing.T) {
	dir := resolvedTempDir(t)
	fooPath, _ := seedGoPackage(t, dir)
	tool := NewGoToDefinitionTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "MyType",
		"file":   fooPath,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	if !strings.Contains(res.Content, "MyType") {
		t.Errorf("result should contain 'MyType', got: %s", res.Content)
	}
}

func TestGoToDefinitionTool_ASTFallback_UnknownSymbol(t *testing.T) {
	dir := resolvedTempDir(t)
	fooPath, _ := seedGoPackage(t, dir)
	tool := NewGoToDefinitionTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "NonExistentSymbolXXX",
		"file":   fooPath,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Should still succeed (informational result) or fail gracefully
	_ = res
}

func TestGoToDefinitionTool_NonGoFile(t *testing.T) {
	dir := resolvedTempDir(t)
	pyFile := filepath.Join(dir, "main.py")
	if err := os.WriteFile(pyFile, []byte("def my_func():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGoToDefinitionTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "my_func",
		"file":   pyFile,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Non-Go files: either finds via grep fallback or returns an informative message
	_ = res.Content
}

// ── FindReferencesTool ──────────────────────────────────────────────────────

func TestFindReferencesTool_Name(t *testing.T) {
	tool := NewFindReferencesTool("")
	if tool.Name() != "find_references" {
		t.Fatalf("Name() = %q, want %q", tool.Name(), "find_references")
	}
}

func TestFindReferencesTool_Declaration(t *testing.T) {
	tool := NewFindReferencesTool("")
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("Declaration() returned nil")
	}
	if decl.Parameters.Properties["symbol"] == nil {
		t.Fatal("declaration missing 'symbol' property")
	}
}

func TestFindReferencesTool_MissingSymbol(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewFindReferencesTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for missing symbol")
	}
}

func TestFindReferencesTool_FileScanFallback_FindsRef(t *testing.T) {
	dir := resolvedTempDir(t)
	_, barPath := seedGoPackage(t, dir)
	tool := NewFindReferencesTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "MyFunc",
		"file":   barPath,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	// The file scan should find MyFunc in bar.go (the callsite)
	if !strings.Contains(res.Content, "MyFunc") {
		t.Errorf("result should contain 'MyFunc', got: %s", res.Content)
	}
}

func TestFindReferencesTool_FileScanFallback_NoMatches(t *testing.T) {
	dir := resolvedTempDir(t)
	seedGoPackage(t, dir)
	tool := NewFindReferencesTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "AbsolutelyNeverUsedSymbolXYZ123",
		"file":   filepath.Join(dir, "foo.go"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Should succeed with "no references found" message
	_ = res
}

func TestFindReferencesTool_MultipleFiles(t *testing.T) {
	dir := resolvedTempDir(t)
	// Create multiple files that all reference a shared symbol
	for i := 0; i < 3; i++ {
		content := fmt.Sprintf("package testpkg\n\nfunc file%d() {\n\t_ = SharedSymbol\n}\n", i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", i)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Define the symbol in a main file
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package testpkg\n\nconst SharedSymbol = 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewFindReferencesTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "SharedSymbol",
		"file":   filepath.Join(dir, "main.go"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	if !strings.Contains(res.Content, "SharedSymbol") {
		t.Errorf("result should contain 'SharedSymbol', got: %s", res.Content)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func TestContainsWholeWord(t *testing.T) {
	cases := []struct {
		line, word string
		want       bool
	}{
		{"foo := MyFunc()", "MyFunc", true},
		{"// CallMyFunc is the outer", "MyFunc", false}, // substring in CallMyFunc
		{"var MyType struct{}", "MyType", true},
		{"MyTypeFoo := 1", "MyType", false},   // prefix
		{"_ = someMyType{}", "MyType", false}, // suffix
		{"return MyFunc, nil", "MyFunc", true},
		{"", "x", false},
		{"x", "x", true},
	}
	for _, c := range cases {
		got := containsWholeWord(c.line, c.word)
		if got != c.want {
			t.Errorf("containsWholeWord(%q, %q) = %v, want %v", c.line, c.word, got, c.want)
		}
	}
}

func TestReadGoFileContent_NonExistent(t *testing.T) {
	_, err := readGoFileContent("/no/such/file/definitely.go")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestReadGoFilesInDir_ReturnsGoFiles(t *testing.T) {
	dir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("package p"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := readGoFilesInDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return a.go but not a_test.go or readme.txt
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			t.Errorf("readGoFilesInDir returned test file: %s", f)
		}
		if !strings.HasSuffix(f, ".go") {
			t.Errorf("readGoFilesInDir returned non-Go file: %s", f)
		}
	}
	if len(files) == 0 {
		t.Fatal("expected at least one .go file")
	}
}

func TestFindDeclsInGoFile_Function(t *testing.T) {
	src := []byte(`package p

func Foo() {}
func Bar() {}
type Baz struct{}
`)
	lines := findDeclsInGoFile("fake.go", src, "Foo")
	if len(lines) == 0 {
		t.Fatal("expected at least one match for Foo")
	}
}

func TestFindDeclsInGoFile_Type(t *testing.T) {
	src := []byte(`package p

func Foo() {}
type Baz struct{}
`)
	lines := findDeclsInGoFile("fake.go", src, "Baz")
	if len(lines) == 0 {
		t.Fatal("expected at least one match for Baz")
	}
}

func TestFindDeclsInGoFile_NotFound(t *testing.T) {
	src := []byte(`package p

func Foo() {}
`)
	lines := findDeclsInGoFile("fake.go", src, "DoesNotExist")
	if len(lines) != 0 {
		t.Fatalf("expected no matches, got %d", len(lines))
	}
}

func TestFindDeclsInGoFile_InvalidSyntax(t *testing.T) {
	src := []byte(`NOT VALID GO CODE {{{{`)
	// Should not panic, returns nothing
	lines := findDeclsInGoFile("bad.go", src, "anything")
	_ = lines
}

func TestGoToDefinitionTool_PathTraversal(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewGoToDefinitionTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{
		"symbol": "SomeSymbol",
		"file":   "../../etc/passwd",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// Should fail — path traversal rejected
	if res.Success {
		t.Fatal("expected failure for path traversal attempt")
	}
}

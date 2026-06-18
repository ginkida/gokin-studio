package tools

import (
	"strings"
	"testing"
)

// TestTryFuzzyReplace_BlankLineInsideRegion_CorrectNotCorrupt locks the
// normToOrig index-mapping that prevents a silent file-corruption class on the
// most-used coding tool. The "BlankLines" fuzzy strategy DROPS blank lines, so
// match indices found in the normalized (shorter) line list must be mapped back
// onto the ORIGINAL line indices before splicing — otherwise the replacement
// lands on the WRONG lines (gluing/orphaning code) while reporting success.
// GLM/DeepSeek routinely omit internal blank lines when reconstructing snippets,
// so this path runs in practice. Unlike upstream gokin (which removed the
// strategy and errors out), studio handles it and produces the CORRECT result.
func TestTryFuzzyReplace_BlankLineInsideRegion_CorrectNotCorrupt(t *testing.T) {
	// Content has a blank line inside the function body; old_string omits it.
	content := "package main\n\nfunc Process() error {\n\tx := compute()\n\n\treturn save(x)\n}\n"
	old := "func Process() error {\n\tx := compute()\n\treturn save(x)\n}"
	newS := "func Process() error {\n\treturn save(compute())\n}"

	got, strategy, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("expected a correct fuzzy replacement, got error: %v", err)
	}
	want := "package main\n\nfunc Process() error {\n\treturn save(compute())\n}\n"
	if got != want {
		t.Errorf("BlankLines fuzzy replace corrupted the file (strategy=%q):\n got=%q\nwant=%q", strategy, got, want)
	}
	// Defensive: the leading context (package decl + its blank line) and the
	// surrounding structure must be intact — no orphaned/glued lines.
	if got[:len("package main\n\n")] != "package main\n\n" {
		t.Errorf("leading context corrupted: %q", got)
	}
}

// TestTryFuzzyReplace_BlankLinesPrecedingMatch verifies blank lines BEFORE the
// matched region don't shift the splice — the original region boundary must be
// computed from the mapped original indices, not the normalized ones.
func TestTryFuzzyReplace_BlankLinesPrecedingMatch(t *testing.T) {
	// Several blank lines precede the target; old_string omits them.
	content := "import x\n\n\n\nfunc target() {\n\treturn 1\n}\n\ntrailer()\n"
	old := "func target() {\n\treturn 1\n}"
	newS := "func target() {\n\treturn 2\n}"

	got, strategy, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("expected a fuzzy replacement, got error: %v", err)
	}
	// The replacement must hit the function, leave the leading blanks + the
	// trailing call intact, and not duplicate/drop anything.
	want := "import x\n\n\n\nfunc target() {\n\treturn 2\n}\n\ntrailer()\n"
	if got != want {
		t.Errorf("preceding-blank-line mapping corrupted the file (strategy=%q):\n got=%q\nwant=%q", strategy, got, want)
	}
}

// TestTryFuzzyReplace_BlankLineMappingPreservesTrailer guards the most dangerous
// corruption shape from the original bug report: code AFTER the matched region
// (which would be "orphaned" or "glued" by a bad index walk) stays exactly put.
func TestTryFuzzyReplace_BlankLineMappingPreservesTrailer(t *testing.T) {
	content := "a := 1\n\nfunc f() {\n\n\tg()\n\n\th()\n}\n\nz := 99\n"
	old := "func f() {\n\tg()\n\th()\n}"
	newS := "func f() {\n\tg2()\n}"

	got, _, err := tryFuzzyReplace(content, old, newS, false)
	if err != nil {
		t.Fatalf("expected a fuzzy replacement, got error: %v", err)
	}
	// The trailing "z := 99" and the leading "a := 1" must survive verbatim.
	if got[:len("a := 1\n\n")] != "a := 1\n\n" {
		t.Errorf("leading code corrupted: %q", got)
	}
	if got[len(got)-len("\nz := 99\n"):] != "\nz := 99\n" {
		t.Errorf("trailing code orphaned/glued: %q", got)
	}
	if want := "func f() {\n\tg2()\n}"; !strings.Contains(got, want) {
		t.Errorf("replacement body wrong: %q", got)
	}
}

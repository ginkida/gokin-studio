package wsl

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Apply* REPLACES cmd.Env — it has to, because wsl.exe needs a real Windows
// environment to start, and the overlay it layers on top is the only thing
// WSLENV carries into the distro. It also CLEARS cmd.Dir, because a UNC path is
// not a legal working directory for the wsl.exe process itself; the distro-side
// directory travels in the plan (retarget sets Dir: ""). So both fields must be
// final BEFORE the call: a later Env assignment discards the overlay, and a
// later Dir assignment restores the illegal path that apply() removed.
//
// Assigning cmd.Env afterwards is silent: the command still runs, still on the
// host's Windows environment, and every injected variable simply never arrives
// inside the distro. That exact bug has already been written twice in this
// repository — once in bash.go and once in tasks/task.go — and neither time did
// anything fail. Comments at the call sites did not prevent the second one, so
// this checks it mechanically instead.
func TestNoEnvOrDirAssignmentAfterApply(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var problems []string

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		wslPkg, ok := localName(file, "github.com/ginkida/gokin-studio/internal/engine/wsl")
		if !ok {
			return nil
		}
		rel := filepath.ToSlash(mustRel(t, root, path))

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			for _, bad := range lateFieldAssignments(fn.Body, wslPkg) {
				problems = append(problems, fmt.Sprintf("%s:%d: %s.%s is assigned AFTER %s.Apply%s "+
					"(line %d) — the assignment wins and the overlay is lost",
					rel, fset.Position(bad.assignPos).Line, bad.target, bad.field, wslPkg,
					bad.applyName, fset.Position(bad.applyPos).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("move these assignments above the Apply* call:\n  %s", strings.Join(problems, "\n  "))
	}
}

// exprKey renders the dotted name of a command expression — "cmd", "t.cmd",
// "s.proc.cmd" — so a struct field is tracked as precisely as a local. Anything
// with a non-name component (an index, a call) has no stable key and returns "",
// which the caller treats as "not analysable" rather than "fine".
func exprKey(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		base := exprKey(e.X)
		if base == "" {
			return ""
		}
		return base + "." + e.Sel.Name
	case *ast.StarExpr:
		return exprKey(e.X)
	case *ast.ParenExpr:
		return exprKey(e.X)
	}
	return ""
}

// withinHostFallback recognises `if !wsl.Apply…(cmd, …) { cmd.Dir = …; … }`,
// which is the documented use of Apply*'s bool return: the body runs only when
// nothing was routed, so those assignments cannot clobber an overlay. Without
// this the guard would fail correct code the moment someone uses the return
// value it was designed to be used with.
func withinHostFallback(body *ast.BlockStmt, assign *ast.AssignStmt, applyPos token.Pos) bool {
	guarded := false
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			stmt, ok := node.(*ast.IfStmt)
			if !ok {
				return true
			}
			unary, ok := stmt.Cond.(*ast.UnaryExpr)
			if !ok || unary.Op != token.NOT {
				return true
			}
			call, ok := unary.X.(*ast.CallExpr)
			if !ok || call.Pos() != applyPos {
				return true
			}
			if stmt.Body != nil && assign.Pos() >= stmt.Body.Pos() && assign.End() <= stmt.Body.End() {
				guarded = true
			}
			return true
		})
	}
	walk(body)
	return guarded
}

type lateAssignment struct {
	target    string
	field     string
	assignPos token.Pos
	applyPos  token.Pos
	applyName string
}

// lateFieldAssignments finds `x.Env = …` or `x.Dir = …` that appear textually
// after a wsl.Apply*(x, …) call in the same function.
//
// Source order is not execution order in general, but every routing call site
// in this codebase is straight-line code inside one function, and the two real
// occurrences of this bug were both plain later statements.
//
// Known limits, all of them deliberate rather than overlooked:
//   - a closure that defers the assignment slips past;
//   - names are tracked textually with no scope analysis, so two different
//     commands both called `cmd` in sibling blocks are conflated (over-reports);
//   - a pointer alias (`c := cmd; c.Env = …`) is a different key and slips past.
//
// The one shape it must never over-report is the documented host fallback,
// which withinHostFallback excludes explicitly.
func lateFieldAssignments(body *ast.BlockStmt, wslPkg string) []lateAssignment {
	type applyCall struct {
		name string
		pos  token.Pos
	}
	applied := map[string]applyCall{}

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != wslPkg || !strings.HasPrefix(sel.Sel.Name, "Apply") {
			return true
		}
		// The command is always the first argument. It is NOT always a plain
		// identifier: tasks/task.go — one of the two sites where this bug was
		// actually written — routes `wsl.ApplyExec(t.cmd, …)` and later
		// assigns `t.cmd.Env`. Requiring *ast.Ident made the guard blind to
		// exactly the code it was written for.
		if target := exprKey(call.Args[0]); target != "" {
			// Keep the earliest call: an assignment after any of them is wrong.
			if prev, seen := applied[target]; !seen || call.Pos() < prev.pos {
				applied[target] = applyCall{name: strings.TrimPrefix(sel.Sel.Name, "Apply"), pos: call.Pos()}
			}
		}
		return true
	})
	if len(applied) == 0 {
		return nil
	}

	var late []lateAssignment
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Env" && sel.Sel.Name != "Dir") {
				continue
			}
			target := exprKey(sel.X)
			if target == "" {
				continue
			}
			call, routed := applied[target]
			if !routed || assign.Pos() < call.pos {
				continue
			}
			if withinHostFallback(body, assign, call.pos) {
				continue
			}
			late = append(late, lateAssignment{
				target: target, field: sel.Sel.Name,
				assignPos: assign.Pos(), applyPos: call.pos, applyName: call.name,
			})
		}
		return true
	})
	return late
}

// The guard is only worth having if it fires, and every call site it protects
// is Windows-only, so prove it against a fixture instead.
//
// The struct-field case is the important one: it mirrors tasks/task.go, which
// holds its command in `t.cmd`. An earlier version of this guard required a
// plain identifier and so was blind to one of the two sites it cites as its own
// motivation — it would have passed green on the regression it exists to catch.
func TestApplyOrderGuardDetectsALateAssignment(t *testing.T) {
	const src = `package p

import (
	"os"
	"os/exec"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

func good(dir string) *exec.Cmd {
	cmd := exec.Command("git", "status")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	wsl.ApplyExec(cmd, wsl.DetectFor(dir), []string{"git", "status"}, nil)
	cmd.WaitDelay = 0
	return cmd
}

func bad(dir string) *exec.Cmd {
	cmd := exec.Command("git", "status")
	wsl.ApplyShell(cmd, wsl.DetectFor(dir), "git status", nil)
	cmd.Env = os.Environ()
	return cmd
}

type task struct{ cmd *exec.Cmd }

func (t *task) badStructField(dir string) {
	t.cmd = exec.Command("git", "status")
	wsl.ApplyExec(t.cmd, wsl.DetectFor(dir), []string{"git", "status"}, nil)
	t.cmd.Env = os.Environ()
	t.cmd.Dir = dir
}

func (t *task) goodStructField(dir string) {
	t.cmd = exec.Command("git", "status")
	t.cmd.Env = os.Environ()
	t.cmd.Dir = dir
	wsl.ApplyExec(t.cmd, wsl.DetectFor(dir), []string{"git", "status"}, nil)
}

func hostFallback(dir string) *exec.Cmd {
	cmd := exec.Command("git", "status")
	if !wsl.ApplyShell(cmd, wsl.DetectFor(dir), "git status", nil) {
		cmd.Dir = dir
		cmd.Env = os.Environ()
	}
	return cmd
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		found[fn.Name.Name] = len(lateFieldAssignments(fn.Body, "wsl"))
	}
	for name, want := range map[string]int{
		"good":            0,
		"bad":             1,
		"badStructField":  2, // both Env and Dir
		"goodStructField": 0,
		"hostFallback":    0, // the documented use of Apply*'s bool return
	} {
		if found[name] != want {
			t.Errorf("%s: %d findings, want %d", name, found[name], want)
		}
	}
}

// exprKey is what makes the struct-field case visible, so pin its contract.
func TestExprKeyRendersDottedNamesAndRefusesTheRest(t *testing.T) {
	for src, want := range map[string]string{
		"cmd":         "cmd",
		"t.cmd":       "t.cmd",
		"s.proc.cmd":  "s.proc.cmd",
		"(cmd)":       "cmd",
		"cmds[0]":     "", // no stable key: report nothing rather than the wrong thing
		"build().cmd": "",
	} {
		expr, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		if got := exprKey(expr); got != want {
			t.Errorf("exprKey(%q) = %q, want %q", src, got, want)
		}
	}
}

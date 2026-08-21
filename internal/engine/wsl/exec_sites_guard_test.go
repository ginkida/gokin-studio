package wsl

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A WSL project's files live inside the distro, so a command that runs on the
// Windows host sees a \\wsl.localhost\... working directory, a Windows PATH and
// none of the distro's toolchain. Routing is therefore not an optimisation: an
// unrouted site is a feature that silently does the wrong thing for every WSL
// user, and it is invisible in review because it looks like ordinary code.
//
// This test enumerates every exec site under internal/ and requires each one to
// be accounted for. A NEW site fails the test until it is routed or classified
// here with a reason, which is the only mechanism that scales — I cannot run
// Windows, so nothing downstream of this will catch it.

// execDisposition explains a site that the scan does not see routing for.
type execDisposition struct {
	// caller, when set, is the "<path>#<func>" that routes this command. Some
	// sites only build an *exec.Cmd and hand it back; routing happens one frame
	// up, which a per-function scan cannot see. The caller is verified to be
	// routed, so this cannot be used to launder a genuine gap.
	caller   string
	hostOnly bool
	reason   string
}

// hostOnly marks a site that must NOT be routed.
func hostOnly(reason string) execDisposition { return execDisposition{hostOnly: true, reason: reason} }

// notYetRouted marks a real gap: this command would benefit from running inside
// the distro but does not yet. Listing it keeps it visible instead of letting
// it blend into the deliberate exemptions.
func notYetRouted(reason string) execDisposition { return execDisposition{reason: reason} }

// routedByCaller marks a builder whose command IS routed, one frame up.
func routedByCaller(caller, reason string) execDisposition {
	return execDisposition{caller: caller, reason: reason}
}

// unroutedExecSites is keyed by "<path>#<receiver>.<function>". Sites whose own
// body routes the command are detected automatically and must NOT appear here.
var unroutedExecSites = map[string]execDisposition{
	// ---- routed by the caller, not by the builder ----
	"internal/engine/tools/verify_code.go#verifyNodeRunnerCommand": routedByCaller(
		"internal/engine/tools/verify_code.go#(*VerifyCodeTool).Execute",
		"builds the npm/pnpm/yarn/bun command and returns it; Execute routes it into the distro"),
	"internal/studio/plugin_hooks_shell_unix.go#pluginHookCommand": routedByCaller(
		"internal/studio/plugin_hooks.go#executePluginCommandHook",
		"builds the /bin/sh command; the caller replaces it with wsl.exe for a WSL project"),
	"internal/studio/plugin_hooks_shell_windows.go#pluginHookCommand": routedByCaller(
		"internal/studio/plugin_hooks.go#executePluginCommandHook",
		"builds the cmd.exe command; the caller replaces it with wsl.exe for a WSL project"),

	// ---- deliberately host-only ----
	"internal/engine/wsl/runtime_windows.go#probeCommand": hostOnly(
		"the single builder for every wsl.exe probe — listing distros and asking whether this wsl.exe " +
			"supports --cd. There is no distro to run inside yet; this is what discovers them"),

	"internal/engine/security/sandbox.go#newSandboxedCommand": hostOnly(
		"wraps a host command in sandbox-exec/bwrap; neither exists in the distro, " +
			"and a routed command is already confined by the distro boundary"),
	"internal/engine/security/sandbox_darwin.go#DetectWorkspaceIsolation":         hostOnly("macOS-only sandbox probe"),
	"internal/engine/security/sandbox_darwin.go#(*SandboxedCommand).applySandbox": hostOnly("macOS-only sandbox-exec"),
	"internal/engine/security/sandbox_linux.go#DetectWorkspaceIsolation":          hostOnly("Linux-only sandbox probe"),
	"internal/engine/security/sandbox_linux.go#(*SandboxedCommand).applySandbox":  hostOnly("Linux-only bwrap"),

	"internal/engine/tools/computer_action_darwin.go#runComputerAppleScript": hostOnly("drives the host desktop"),
	"internal/engine/tools/computer_action_windows.go#runComputerPowerShell": hostOnly("drives the host desktop"),
	"internal/engine/tools/computer_app_darwin.go#foregroundApplication":     hostOnly("reads the host foreground app"),
	"internal/engine/tools/computer_app_windows.go#foregroundApplication":    hostOnly("reads the host foreground app"),
	"internal/engine/tools/computer_screenshot_darwin.go#runMacOSScreenCapture": hostOnly(
		"captures the host screen"),
	"internal/engine/tools/computer_screenshot_windows.go#runWindowsScreenCapture": hostOnly(
		"captures the host screen"),
	"internal/engine/tools/notifications.go#(*NotificationManager).sendNativeNotification": hostOnly(
		"a desktop notification belongs to the host session, not the distro"),

	"internal/studio/mcp.go#connectMCP": hostOnly(
		"MCP connectors are global, not per-project: loadMCPServers reads one app-level file and " +
			"MCPServerConfig carries no project reference, so connectMCP receives no directory and there is " +
			"no distro to choose. A connector the user configured for the app belongs to the app"),
	"internal/studio/wake_darwin.go#acquirePlatformWakeLease": hostOnly("host power management"),
	"internal/studio/wake_other.go#acquirePlatformWakeLease":  hostOnly("host power management"),
	"internal/studio/preview_process_windows.go#killPreviewProcess": hostOnly(
		"kills a host PID by handle; the preview server is not routed either"),
	"internal/engine/tasks/task_windows.go#killProcessGroup": hostOnly(
		"taskkill receives the Windows PID of the host-side command wrapper (including wsl.exe); " +
			"that PID has no meaning inside a distro, so the process-tree control command must remain on the host"),
	"internal/studio/open_in_filemanager.go#var:execCommand": hostOnly(
		"launches the host file manager and host GUI editors, which take the UNC path by design; " +
			"a routed explorer.exe would run inside the distro and reach no desktop"),

	"internal/engine/agent/workspace_isolation.go#prepareGitWorktree": hostOnly(
		"internal/engine/agent is the unused standalone runner; studio runs its own loop " +
			"in project.go and never reaches this code"),
	"internal/engine/agent/workspace_isolation.go#runGitCommandWithInput": hostOnly(
		"same unused package"),

	// ---- real gaps, not yet routed ----
	"internal/engine/hooks/manager.go#(*Manager).executeHook": notYetRouted(
		"two blockers, not one: ExpandCommand inlines the HOST environment into the script before it is " +
			"executed, so a distro shell would receive a Windows $PATH spliced in unquoted and an empty " +
			"$HOME — routing alone would break it differently rather than fix it. It is also unreachable: " +
			"hooks.NewManager and (*Executor).SetHooks have no callers, and studio's real hook path is " +
			"internal/studio/plugin_hooks.go, which IS routed"),
	"internal/studio/preview_server.go#(*Studio).StartSessionPreviewServer": notYetRouted(
		"a real gap, but routing it needs two facts measured first, both in scripts/wslprobe: (1) killing " +
			"the wsl.exe relay must actually stop the dev server inside the distro, or a routed preview " +
			"leaks a server holding the port — strictly worse than today; (2) a server bound to 127.0.0.1 " +
			"INSIDE the distro must be reachable from the host's 127.0.0.1, which is what the preview pane " +
			"and the diagnostics bridge proxy connect to. resolvePreviewExecutable also resolves node/npm " +
			"against the working directory, so it has to move inside the distro at the same time"),
}

// execSites is the scan result for one repository.
type execSites struct {
	routed   map[string]bool
	unrouted map[string]bool
	// routes holds every function that makes a routing decision, whether or not
	// it also launches a process. A builder's caller usually does not call
	// exec.Command itself, so routedByCaller could not verify anything without
	// this.
	routes map[string]bool
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test file")
		}
		dir = parent
	}
}

// localName returns what a package is called inside this file, so an aliased or
// renamed import cannot hide a call site behind a name the scan is not looking
// for. A dot-import returns "." and is reported separately: it makes the call
// spell as a bare Command(...) with no selector to match, so rather than pretend
// to handle it the scan refuses to run.
func localName(file *ast.File, importPath string) (string, bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return importPath[strings.LastIndex(importPath, "/")+1:], true
	}
	return "", false
}

// siteKey names a function precisely enough that two same-named methods in one
// file cannot share an entry — semantic_tools.go and plan_mode.go both declare
// Execute several times, and a shared key would let one method inherit an
// exemption written about a different one.
func siteKey(rel string, fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		name = receiverName(fn.Recv.List[0].Type) + "." + name
	}
	return rel + "#" + name
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "(*" + receiverName(t.X) + ")"
	case *ast.IndexExpr: // generic receiver
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// scanExecSites walks every non-test file under internal/, including the bodies
// of package-level func literals, which is where this codebase puts its
// process-launch test seams.
func scanExecSites(t *testing.T) execSites {
	t.Helper()
	root := repoRoot(t)
	sites := execSites{routed: map[string]bool{}, unrouted: map[string]bool{}, routes: map[string]bool{}}
	fset := token.NewFileSet()

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Parse regardless of build tags: a Windows-only site is exactly what
		// this guard exists to catch and would be invisible to a macOS build.
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel := filepath.ToSlash(mustRel(t, root, path))

		execPkg, hasExec := localName(file, "os/exec")
		if !hasExec {
			return nil
		}
		if execPkg == "." {
			return fmt.Errorf("%s dot-imports os/exec; exec.Command has no selector to match "+
				"and this guard cannot see its call sites", rel)
		}
		wslPkg, _ := localName(file, "github.com/ginkida/gokin-studio/internal/engine/wsl")
		// Inside the wsl package itself the markers are called unqualified, so
		// there is no selector to match. Use "" as the sentinel for that.
		selfHosted := file.Name.Name == "wsl"

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				classify(sites, siteKey(rel, d), d.Body, execPkg, wslPkg, selfHosted,
					funcTakesWSLValue(d.Type, wslPkg))
			case *ast.GenDecl:
				// var x = func() { ... exec.Command ... } — a *ast.GenDecl, and
				// therefore skipped entirely by a Decls-only FuncDecl walk.
				for _, spec := range d.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range value.Names {
						if i >= len(value.Values) {
							break
						}
						lit, ok := value.Values[i].(*ast.FuncLit)
						if !ok {
							continue
						}
						classify(sites, rel+"#var:"+name.Name, lit.Body, execPkg, wslPkg, selfHosted,
							funcTakesWSLValue(lit.Type, wslPkg))
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites.routed)+len(sites.unrouted) < 30 {
		t.Fatalf("only %d exec sites found; the walk is broken and this guard is asleep",
			len(sites.routed)+len(sites.unrouted))
	}
	return sites
}

func mustRel(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

// classify records one function under the key, if it launches a process at all.
//
// The routing marker is deliberately narrow. Counting ANY wsl reference would
// let a cosmetic `if wsl.IsWSLPath(dir) { warn() }` flip a genuine gap to
// "routed", and the stale check would then demand its accurate exemption be
// deleted. Only three families redirect a command: Apply* mutates an *exec.Cmd,
// Retarget* computes the replacement, and DetectFor is the sole way to obtain
// the Target either needs — a function that calls it is choosing a target, not
// describing a path (IsWSLPath, CanonicalKey and friends do that, and are not
// markers).
func classify(sites execSites, key string, body *ast.BlockStmt, execPkg, wslPkg string, selfHosted, wslParam bool) {
	calls, routed := 0, wslParam
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			if selfHosted && isRoutingMarker(ident.Name) {
				routed = true
			}
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case pkg.Name == execPkg && strings.HasPrefix(sel.Sel.Name, "Command"):
			calls++
		case wslPkg != "" && pkg.Name == wslPkg && isRoutingMarker(sel.Sel.Name):
			routed = true
		}
		return true
	})
	if routed {
		sites.routes[key] = true
	}
	if calls == 0 {
		return
	}
	if routed {
		sites.routed[key] = true
	} else {
		sites.unrouted[key] = true
	}
}

// funcTakesWSLValue reports whether a function receives a wsl type, which means
// its caller chose the target and this function is the routing implementation.
func isRoutingMarker(name string) bool {
	return strings.HasPrefix(name, "Apply") || strings.HasPrefix(name, "Retarget") || name == "DetectFor"
}

func funcTakesWSLValue(sig *ast.FuncType, wslPkg string) bool {
	if sig == nil || sig.Params == nil || wslPkg == "" {
		return false
	}
	found := false
	for _, param := range sig.Params.List {
		ast.Inspect(param.Type, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == wslPkg {
				found = true
			}
			return true
		})
	}
	return found
}

func TestEveryExecSiteIsRoutedOrClassified(t *testing.T) {
	sites := scanExecSites(t)

	var missing []string
	for key := range sites.unrouted {
		if _, ok := unroutedExecSites[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these exec sites run on the Windows host with a UNC working directory and no "+
			"distro toolchain.\nRoute them through wsl.ApplyExec/ApplyShell/ApplyGit, or add each to "+
			"unroutedExecSites with a reason:\n  %s", strings.Join(missing, "\n  "))
	}
}

// A stale entry is as bad as a missing one: it silently blesses a site that no
// longer exists, and the next site to take that name inherits the exemption.
func TestNoStaleExecSiteClassifications(t *testing.T) {
	sites := scanExecSites(t)

	var stale, nowRouted []string
	for key := range unroutedExecSites {
		switch {
		case sites.unrouted[key]:
		case sites.routed[key]:
			nowRouted = append(nowRouted, key)
		default:
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	sort.Strings(nowRouted)
	if len(stale) > 0 {
		t.Errorf("unroutedExecSites names sites that no longer exist; delete them:\n  %s",
			strings.Join(stale, "\n  "))
	}
	if len(nowRouted) > 0 {
		t.Errorf("these sites route their own command now; remove them from unroutedExecSites:\n  %s",
			strings.Join(nowRouted, "\n  "))
	}
}

// routedByCaller is the one classification that asserts something WORKS, so it
// is the one that must be machine-checked. Without this it would be a way to
// write off a real gap with a plausible sentence.
func TestRoutedByCallerNamesARoutedCaller(t *testing.T) {
	sites := scanExecSites(t)
	for key, disposition := range unroutedExecSites {
		if disposition.caller == "" {
			continue
		}
		if !sites.routes[disposition.caller] {
			t.Errorf("%s claims to be routed by %s, but that function does not route any command "+
				"(it is %s)", key, disposition.caller, whereIs(sites, disposition.caller))
		}
	}
}

func whereIs(sites execSites, key string) string {
	switch {
	case sites.unrouted[key]:
		return "an unrouted exec site"
	case sites.routed[key]:
		return "routed"
	case sites.routes[key]:
		return "routing, but it launches no process"
	default:
		return "not an exec site at all — check the spelling, including the receiver"
	}
}

// Every classification must say why. An empty reason turns the table into a
// list of names, which is how an exemption outlives the fact behind it.
func TestEveryClassificationCarriesAReason(t *testing.T) {
	for key, disposition := range unroutedExecSites {
		if strings.TrimSpace(disposition.reason) == "" {
			t.Errorf("%s has no reason", key)
		}
	}
}

// The routed set is what actually works for WSL users, so hold a floor under
// it: a refactor that quietly drops routing from a call site would otherwise
// only show up as a smaller number nobody is looking at. This checks `routes`
// rather than `routed` because executePluginCommandHook routes a command it did
// not build — it is the routing frame even though it calls no exec.Command.
func TestKnownRoutedSitesStayRouted(t *testing.T) {
	sites := scanExecSites(t)
	for _, key := range []string{
		"internal/engine/tools/bash.go#(*BashTool).executeForeground",
		"internal/engine/tools/git_command.go#newGitCommand",
		"internal/engine/git/command.go#newCommandContext",
		"internal/engine/tasks/task.go#(*Task).Start",
		"internal/engine/tools/run_tests.go#(*RunTestsTool).Execute",
		"internal/engine/tools/verify_code.go#(*VerifyCodeTool).Execute",
		"internal/studio/terminal.go#newTerminalWithLogger",
		"internal/studio/plugin_hooks.go#executePluginCommandHook",
		"internal/engine/tools/git_pr.go#(*GitPRTool).runGH",
		"internal/studio/pull_request.go#(*Studio).runGH",
		"internal/engine/tools/semantic_tools.go#runGopls",
		"internal/engine/wsl/lookpath.go#LookPathFor",
		"internal/studio/git_status.go#runGitWithTimeout",
		"internal/studio/git_status.go#runBoundedGitReview",
		"internal/studio/git_commit.go#runGitErr",
		"internal/studio/session_worktree.go#runGitWorktreeCommandRaw",
	} {
		if !sites.routes[key] {
			t.Errorf("%s lost its WSL routing", key)
		}
	}
}

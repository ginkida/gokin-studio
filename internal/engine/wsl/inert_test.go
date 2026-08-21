package wsl

import (
	"os/exec"
	"reflect"
	"testing"
)

// The whole design rests on one claim: off Windows, nothing is retargeted, so
// macOS and Linux take byte-identical code paths. Every routing call site funnels
// through DetectFor -> Apply*, so proving the claim once at that seam proves it
// for all of them.
//
// This is the guard that catches a future change making DetectFor probe the
// machine, or an Apply* that mutates before checking.
func TestRoutingIsInertOnThisPlatform(t *testing.T) {
	// DetectFor is the single decision point. On a non-Windows build it must
	// return the host target for every input, including WSL-shaped ones.
	for _, dir := range []string{
		`\\wsl.localhost\Ubuntu\home\me\repo`,
		`\\wsl$\Ubuntu\home\me\repo`,
		`C:\Users\me\repo`,
		"/home/me/repo",
		"",
	} {
		if DetectFor(dir).IsWSL() {
			t.Fatalf("DetectFor(%q) routed into WSL on a non-Windows build", dir)
		}
	}

	// And every Apply* entry point leaves the command untouched.
	snapshot := func(cmd *exec.Cmd) (string, []string, string, []string, error) {
		return cmd.Path, append([]string(nil), cmd.Args...), cmd.Dir,
			append([]string(nil), cmd.Env...), cmd.Err
	}
	for name, run := range map[string]func(*exec.Cmd) bool{
		"ApplyExec": func(c *exec.Cmd) bool {
			return ApplyExec(c, DetectFor(c.Dir), []string{"git", "status"}, map[string]string{"A": "1"})
		},
		"ApplyShell": func(c *exec.Cmd) bool {
			return ApplyShell(c, DetectFor(c.Dir), "git status", map[string]string{"A": "1"})
		},
		"ApplyGit": func(c *exec.Cmd) bool {
			return ApplyGit(c, c.Dir, []string{"git", "status"})
		},
	} {
		cmd := exec.Command("git", "status")
		cmd.Dir = `\\wsl.localhost\Ubuntu\home\me\repo`
		cmd.Env = []string{"KEEP=1"}
		path, args, dir, env, cmdErr := snapshot(cmd)

		if run(cmd) {
			t.Fatalf("%s reported a rewrite on a non-Windows build", name)
		}
		gotPath, gotArgs, gotDir, gotEnv, gotErr := snapshot(cmd)
		if gotPath != path || !reflect.DeepEqual(gotArgs, args) ||
			gotDir != dir || !reflect.DeepEqual(gotEnv, env) || gotErr != cmdErr {
			t.Fatalf("%s mutated the command: path=%q args=%v dir=%q env=%v",
				name, gotPath, gotArgs, gotDir, gotEnv)
		}
	}
}

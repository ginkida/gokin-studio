package wsl

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// sentinelValue stands in for a real user secret. It is deliberately not
// credential-shaped, but it is distinctive enough that the assertions below can
// prove it never reaches a command line.
const sentinelValue = "value-that-must-not-reach-argv"

func overlayMap(t *testing.T, entries []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		out[name] = value
	}
	return out
}

// Values reach the distro through WSLENV — the mechanism Microsoft provides —
// not through an `env -- K=V` argv prefix. On Windows a process's command line
// is readable by other processes, and these are the user's Keychain/DPAPI
// secrets from Settings -> Local environment.
func TestInjectedVariablesTravelViaWSLENVNotArgv(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	plan, ok := RetargetShell(target, "printenv DATABASE_URL", map[string]string{
		"DATABASE_URL": sentinelValue,
	})
	if !ok {
		t.Fatal("not retargeted")
	}
	for _, arg := range plan.Args {
		if strings.Contains(arg, sentinelValue) {
			t.Fatalf("the value reached the command line: %#v", plan.Args)
		}
		if arg == "env" {
			t.Fatalf("the argv env prefix is still in use: %#v", plan.Args)
		}
	}
	overlay := overlayMap(t, plan.EnvOverlay)
	if overlay["DATABASE_URL"] != sentinelValue {
		t.Fatalf("the variable is not in the environment overlay: %#v", plan.EnvOverlay)
	}
	// "/u" shares Windows-to-WSL only, never back.
	if overlay["WSLENV"] != "DATABASE_URL/u" {
		t.Fatalf("WSLENV = %q, want the injected name only", overlay["WSLENV"])
	}
}

// The host command path builds its environment from an ALLOWLIST
// (security.safeEnvironment), so provider keys never reach an agent command
// there. Inheriting the parent environment for wsl.exe would quietly undo that.
func TestOverlayBlanksTheAppsOwnCredentials(t *testing.T) {
	overlay := overlayMap(t, EnvOverlay(map[string]string{"SAFE": "1"}))
	for _, name := range []string{"GLM_API_KEY", "KIMI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		value, present := overlay[name]
		if !present || value != "" {
			t.Fatalf("%s = %q (present=%v); it must be blanked for wsl.exe", name, value, present)
		}
	}
}

// WSLENV is pinned even with nothing to inject, so a value the user set for
// their own purposes cannot ferry the app's environment across the boundary.
func TestOverlayPinsWSLENVEvenWithNoInjection(t *testing.T) {
	overlay := overlayMap(t, EnvOverlay(nil))
	value, present := overlay["WSLENV"]
	if !present || value != "" {
		t.Fatalf("WSLENV = %q (present=%v); it must be pinned empty", value, present)
	}
}

// Windows environment names are case-insensitive, so an overlay must replace a
// differently-cased inherited entry rather than adding a duplicate — a
// duplicate would leave the original value reachable.
func TestMergeEnvIsCaseInsensitiveLastWins(t *testing.T) {
	merged := MergeEnv(
		[]string{`Path=C:\windows`, "GLM_API_KEY=inherited-token", "KEEP=1"},
		[]string{"glm_api_key=", "NEW=2"},
	)
	seen := map[string]int{}
	for _, entry := range merged {
		name, _, _ := strings.Cut(entry, "=")
		seen[strings.ToUpper(name)]++
	}
	if seen["GLM_API_KEY"] != 1 {
		t.Fatalf("duplicate GLM_API_KEY entries: %#v", merged)
	}
	for _, entry := range merged {
		if strings.Contains(entry, "inherited-token") {
			t.Fatalf("the inherited value survived: %#v", merged)
		}
	}
	if seen["KEEP"] != 1 || seen["NEW"] != 1 || seen["PATH"] != 1 {
		t.Fatalf("merge lost or duplicated entries: %#v", merged)
	}
	base := []string{"A=1"}
	if got := MergeEnv(base, nil); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("empty overlay changed the base: %#v", got)
	}
}

// wsl.exe needs a real Windows environment (SystemRoot and friends) to start at
// all, so the inherited block must survive rather than being cleared.
func TestApplyKeepsInheritedEnvironmentAndLayersOverlay(t *testing.T) {
	t.Setenv("GOKIN_WSL_MARKER", "inherited")
	target := modernTarget(`\\wsl.localhost\Ubuntu\home\me\repo`)
	cmd := exec.Command("git", "status")
	if !ApplyShell(cmd, target, "true", map[string]string{"INJECTED": "yes"}) {
		t.Fatal("not retargeted")
	}
	env := overlayMap(t, cmd.Env)
	if env["GOKIN_WSL_MARKER"] != "inherited" {
		t.Fatal("the inherited Windows environment was dropped; wsl.exe may not start")
	}
	if env["INJECTED"] != "yes" || env["WSLENV"] != "INJECTED/u" {
		t.Fatalf("overlay was not layered: INJECTED=%q WSLENV=%q", env["INJECTED"], env["WSLENV"])
	}
	if len(cmd.Env) < len(os.Environ()) {
		t.Fatalf("environment shrank from %d to %d entries", len(os.Environ()), len(cmd.Env))
	}
}

// Values are never shell-interpreted: they live in the environment block, so
// metacharacters in them are inert by construction.
func TestInjectedValuesAreNeverShellInterpreted(t *testing.T) {
	target := modernTarget(`\\wsl.localhost\Ubuntu\p`)
	plan, _ := RetargetShell(target, "true", map[string]string{
		"EVIL": "$(touch /tmp/pwned); `whoami`",
	})
	overlay := overlayMap(t, plan.EnvOverlay)
	if overlay["EVIL"] != "$(touch /tmp/pwned); `whoami`" {
		t.Fatalf("the value was altered: %q", overlay["EVIL"])
	}
	for _, arg := range plan.Args {
		if strings.Contains(arg, "touch /tmp/pwned") {
			t.Fatalf("the value reached argv: %#v", plan.Args)
		}
	}
}

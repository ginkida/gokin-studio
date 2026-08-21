// Command wslprobe checks, on a real Windows machine with a real WSL distro,
// the platform assumptions the WSL support in this repository is built on.
//
// Every one of these was derived by reading Go's source and Microsoft's docs
// rather than by running it, because the feature was developed on macOS. This
// program turns each derivation into a measurement, so a wrong premise shows up
// here instead of in front of a user.
//
// Usage, from a Windows shell (not from inside WSL):
//
//	go run ./scripts/wslprobe -distro Ubuntu -dir \\wsl.localhost\Ubuntu\home\me\someproject
//
// -dir must be a directory that already exists inside the distro. If omitted,
// the distro's own home directory is used. Nothing is written outside a
// temporary directory the probe creates inside the distro and removes at the
// end. Exit status is 0 when every assumption held.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/wsl"
)

type probe struct {
	name string
	// assumption is what the code believes. It is printed on failure so the
	// reader knows what breaks, not just that something differed.
	assumption string
	run        func(env) (detail string, err error)
}

type env struct {
	distro  string
	dir     string
	tmpHost string // scratch directory, Windows spelling
	tmpLin  string // the same directory, Linux spelling
	target  wsl.Target
}

func main() {
	distro := flag.String("distro", "", "WSL distribution name (default: the default distro)")
	dir := flag.String("dir", "", `an existing directory inside the distro, e.g. \\wsl.localhost\Ubuntu\home\me\proj`)
	flag.Parse()

	if runtime.GOOS != "windows" {
		fmt.Fprintf(os.Stderr, "wslprobe measures Windows behaviour and must run on Windows; this is %s.\n", runtime.GOOS)
		os.Exit(2)
	}
	if !wsl.Available() {
		fmt.Fprintln(os.Stderr, "wsl.exe was not found on PATH. Install WSL, or run this from a Windows shell rather than inside a distro.")
		os.Exit(2)
	}

	e, cleanup, err := setup(*distro, *dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(2)
	}
	defer cleanup()

	fmt.Printf("distro     %s\n", e.distro)
	fmt.Printf("directory  %s\n", e.dir)
	fmt.Printf("scratch    %s  (%s)\n", e.tmpHost, e.tmpLin)
	fmt.Printf("wsl.exe    %s\n", wsl.Executable())
	fmt.Printf("--cd       %v\n\n", wsl.HostCaps().SupportsCD)

	failed := 0
	for _, p := range probes() {
		detail, err := p.run(e)
		switch {
		case err != nil:
			failed++
			fmt.Printf("FAIL  %s\n      %v\n      the code assumes: %s\n", p.name, err, p.assumption)
		default:
			fmt.Printf("ok    %-46s %s\n", p.name, detail)
		}
	}

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d assumption(s) did NOT hold. Each one above names what the code believes; that belief is\n"+
			"wrong on this machine and the corresponding behaviour will be wrong too.\n", failed)
		os.Exit(1)
	}
	fmt.Println("Every assumption held on this machine.")
}

func setup(distro, dir string) (env, func(), error) {
	var e env
	states := wsl.States(context.Background())
	if len(states) == 0 {
		return e, nil, errors.New("no WSL distributions are registered")
	}
	e.distro = distro
	if e.distro == "" {
		e.distro = states[0].Name
		for _, s := range states {
			if s.Default {
				e.distro = s.Name
			}
		}
	}

	e.dir = dir
	if e.dir == "" {
		home, err := runIn(e.distro, "printf %s \"$HOME\"")
		if err != nil {
			return e, nil, fmt.Errorf("could not read $HOME in %q: %w", e.distro, err)
		}
		e.dir = wsl.Location{Distro: e.distro, LinuxPath: strings.TrimSpace(home)}.WindowsPath()
	}
	if _, ok := wsl.ParseWindowsPath(e.dir); !ok {
		return e, nil, fmt.Errorf("-dir %q is not a WSL UNC path", e.dir)
	}

	e.target = wsl.DetectFor(e.dir)
	if !e.target.IsWSL() {
		return e, nil, fmt.Errorf("DetectFor(%q) did not produce a WSL target; distro %q may not be registered",
			e.dir, e.distro)
	}

	stamp := fmt.Sprintf("gokin-wslprobe-%d", time.Now().UnixNano())
	e.tmpLin = "/tmp/" + stamp
	if _, err := runIn(e.distro, "mkdir -p "+wsl.ShellQuote(e.tmpLin)); err != nil {
		return e, nil, fmt.Errorf("could not create a scratch directory: %w", err)
	}
	e.tmpHost = wsl.Location{Distro: e.distro, LinuxPath: e.tmpLin}.WindowsPath()

	return e, func() {
		_, _ = runIn(e.distro, "rm -rf "+wsl.ShellQuote(e.tmpLin))
	}, nil
}

// runIn executes a script inside the distro and returns its stdout. It goes
// through wsl.exe directly rather than through the app's routing layer, so a
// bug in that layer cannot hide a failing probe.
func runIn(distro, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, wsl.Executable(), "-d", distro, "--exec", "bash", "-lc", script)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return wsl.DecodeConsoleOutput(out), nil
}

// freePort asks the OS for an unused port and releases it. A race with another
// listener is possible and harmless: the probe would simply fail to bind and
// report that, rather than reporting a false forwarding failure silently.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func probes() []probe {
	return []probe{
		{
			name:       "wsl -l -v output parses",
			assumption: "ParseDistroList understands this wsl.exe's table, including its UTF-16 encoding",
			run: func(e env) (string, error) {
				states := wsl.States(context.Background())
				if len(states) == 0 {
					return "", errors.New("States() returned nothing, so the distro list did not parse")
				}
				names := make([]string, 0, len(states))
				for _, s := range states {
					names = append(names, fmt.Sprintf("%s(v%d,running=%v)", s.Name, s.Version, s.Running))
				}
				return strings.Join(names, " "), nil
			},
		},
		{
			name:       "an ordinary distro directory is not a reparse point",
			assumption: "scanPathLinks sees no reparse point on a normal directory, which is what lets the validator tolerate an unresolvable path",
			run: func(e env) (string, error) {
				info, err := os.Lstat(e.dir)
				if err != nil {
					return "", fmt.Errorf("Lstat(%s): %w", e.dir, err)
				}
				if info.Mode()&os.ModeIrregular != 0 {
					return "", fmt.Errorf("an ordinary directory reports ModeIrregular (mode %v); the validator's "+
						"link scan would treat every path as suspicious and refuse it", info.Mode())
				}
				return fmt.Sprintf("mode %v, IsDir=%v", info.Mode(), info.IsDir()), nil
			},
		},
		{
			name:       "a Linux symlink appears as ModeIrregular, not ModeSymlink",
			assumption: "Go maps IO_REPARSE_TAG_LX_SYMLINK to ModeIrregular, so checkSymlink must treat that as a link or containment is bypassable",
			run: func(e env) (string, error) {
				if _, err := runIn(e.distro, "ln -sfn / "+wsl.ShellQuote(e.tmpLin+"/escape")); err != nil {
					return "", fmt.Errorf("could not create a symlink inside the distro: %w", err)
				}
				link := filepath.Join(e.tmpHost, "escape")
				info, err := os.Lstat(link)
				if err != nil {
					return "", fmt.Errorf("Lstat(%s): %w", link, err)
				}
				switch {
				case info.Mode()&os.ModeSymlink != 0:
					return "ModeSymlink (better than assumed: the ordinary symlink check already catches it)", nil
				case info.Mode()&os.ModeIrregular != 0:
					return "ModeIrregular, as assumed", nil
				default:
					return "", fmt.Errorf("a symlink reports mode %v — neither ModeSymlink nor ModeIrregular, so "+
						"NOTHING in the validator recognises it as a link and allowSymlinks=false is bypassable",
						info.Mode())
				}
			},
		},
		{
			name:       "EvalSymlinks on a distro directory",
			assumption: "AddProject and the path validator both tolerate a non-ENOENT failure here; if it succeeds instead, those fallbacks are simply unused",
			run: func(e env) (string, error) {
				resolved, err := filepath.EvalSymlinks(e.dir)
				if err == nil {
					return "succeeds (" + resolved + ") — the tolerance path is dead code, which is harmless", nil
				}
				if os.IsNotExist(err) {
					return "", fmt.Errorf("EvalSymlinks says the project directory does not exist (%v); "+
						"AddProject would reject the project outright", err)
				}
				return fmt.Sprintf("fails with %v — the tolerated case, as assumed", err), nil
			},
		},
		{
			name:       "a directory Windows cannot name is still usable",
			assumption: "the bash tool adopts a directory it cannot stat, because --cd reaches it by Linux path anyway",
			run: func(e env) (string, error) {
				const awkward = "logs/2026-08-11T09:00:00"
				linux := e.tmpLin + "/" + awkward
				if _, err := runIn(e.distro, "mkdir -p "+wsl.ShellQuote(linux)); err != nil {
					return "", fmt.Errorf("could not create %q: %w", linux, err)
				}
				host := wsl.Location{Distro: e.distro, LinuxPath: linux}.WindowsPath()
				_, statErr := os.Stat(host)

				out, err := runIn(e.distro, "cd "+wsl.ShellQuote(linux)+" && pwd -P")
				if err != nil {
					return "", fmt.Errorf("the distro could not enter %q either: %w", linux, err)
				}
				if strings.TrimSpace(out) != linux {
					return "", fmt.Errorf("pwd reported %q, want %q", strings.TrimSpace(out), linux)
				}
				if statErr == nil {
					return "host stat also works here — the adoption path is unnecessary on this build", nil
				}
				return fmt.Sprintf("host stat fails (%v) but the distro enters it: adoption is required, as assumed", statErr), nil
			},
		},
		{
			name:       "WSLENV carries an injected variable into the distro",
			assumption: "secrets and settings reach the distro through the environment block, never through argv",
			run: func(e env) (string, error) {
				const marker = "GOKIN_WSLPROBE_VALUE"
				cmd := exec.Command(wsl.Executable())
				if !wsl.ApplyShell(cmd, e.target, "printf %s \"$"+marker+"\"", map[string]string{marker: "carried"}) {
					return "", errors.New("ApplyShell declined to route, so the injection could not be tested")
				}
				for _, arg := range cmd.Args {
					if strings.Contains(arg, "carried") {
						return "", fmt.Errorf("the value appeared on the command line, where other processes can "+
							"read it: %q", cmd.Args)
					}
				}
				out, err := cmd.Output()
				if err != nil {
					return "", fmt.Errorf("the routed command failed: %w", err)
				}
				if got := strings.TrimSpace(wsl.DecodeConsoleOutput(out)); got != "carried" {
					return "", fmt.Errorf("the variable did not arrive: got %q. Every injected setting — and every "+
						"workspace variable the user configured — is silently missing inside the distro", got)
				}
				return "arrived via the environment, absent from argv", nil
			},
		},
		{
			name:       "the app's own credentials do not leak into the distro",
			assumption: "EnvOverlay blanks the provider keys, so a routed command cannot read them",
			run: func(e env) (string, error) {
				const key = "GLM_API_KEY"
				host := os.Getenv(key)
				restore := func() { _ = os.Setenv(key, host) }
				defer restore()
				if err := os.Setenv(key, "must-not-cross"); err != nil {
					return "", err
				}
				cmd := exec.Command(wsl.Executable())
				if !wsl.ApplyShell(cmd, e.target, "printf %s \"$"+key+"\"", nil) {
					return "", errors.New("ApplyShell declined to route")
				}
				out, err := cmd.Output()
				if err != nil {
					return "", fmt.Errorf("the routed command failed: %w", err)
				}
				if strings.Contains(wsl.DecodeConsoleOutput(out), "must-not-cross") {
					return "", errors.New("the provider API key was readable inside the distro; any command the " +
						"agent runs there could exfiltrate it")
				}
				return "blanked, as assumed", nil
			},
		},
		{
			name:       "a distro binary is invisible to host LookPath",
			assumption: "gh/gopls availability must be probed inside the distro; a host check would report them missing",
			run: func(e env) (string, error) {
				// A name that certainly exists in the distro and certainly not on Windows.
				const name = "dpkg-query"
				if _, err := runIn(e.distro, "command -v -- "+name+" >/dev/null 2>&1 || command -v -- rpm >/dev/null 2>&1"); err != nil {
					return "neither dpkg-query nor rpm exists in this distro; probe skipped", nil
				}
				_, hostErr := exec.LookPath(name)
				linErr := wsl.LookPathFor(context.Background(), e.target, name)
				if hostErr == nil {
					return fmt.Sprintf("%s happens to exist on Windows too; inconclusive here", name), nil
				}
				if linErr != nil {
					return "", fmt.Errorf("LookPathFor could not find %s inside the distro either (%v); the "+
						"target-aware availability check does not work, so gh and gopls would be reported missing", name, linErr)
				}
				return "host LookPath fails, LookPathFor succeeds — the target-aware check is doing real work", nil
			},
		},
		{
			name:       "a distro-bound 127.0.0.1 port is reachable from the host",
			assumption: "the preview pane and its diagnostics bridge dial 127.0.0.1 on Windows; if WSL does not forward that to a server bound inside the distro, a routed preview server would show nothing",
			run: func(e env) (string, error) {
				port, err := freePort()
				if err != nil {
					return "", err
				}
				script := fmt.Sprintf(
					"command -v python3 >/dev/null 2>&1 || exit 97; "+
						"python3 -c \"import http.server,socketserver;"+
						"socketserver.TCPServer((\\\"127.0.0.1\\\",%d),http.server.SimpleHTTPRequestHandler).serve_forever()\"", port)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cmd := exec.CommandContext(ctx, wsl.Executable())
				if !wsl.ApplyShell(cmd, e.target, script, nil) {
					return "", errors.New("ApplyShell declined to route")
				}
				if err := cmd.Start(); err != nil {
					return "", err
				}
				defer func() { cancel(); _ = cmd.Wait() }()

				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
					if dialErr == nil {
						_ = conn.Close()
						return fmt.Sprintf("port %d bound inside the distro answered on the host", port), nil
					}
					time.Sleep(500 * time.Millisecond)
				}
				if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 97 {
					return "python3 is not installed in this distro; probe skipped", nil
				}
				return "", fmt.Errorf("nothing answered on 127.0.0.1:%d within 15s. A preview or dev server "+
					"started inside the distro would be invisible to the preview pane, so it must keep "+
					"running on the host", port)
			},
		},
		{
			name:       "killing the relay stops the process inside the distro",
			assumption: "cancelling a command's context actually stops the work; if not, a cancelled turn leaves a process running",
			run: func(e env) (string, error) {
				marker := "gokin-wslprobe-sentinel-" + fmt.Sprint(time.Now().UnixNano())
				ctx, cancel := context.WithCancel(context.Background())
				cmd := exec.CommandContext(ctx, wsl.Executable())
				if !wsl.ApplyShell(cmd, e.target, "sleep 120 # "+marker, nil) {
					cancel()
					return "", errors.New("ApplyShell declined to route")
				}
				if err := cmd.Start(); err != nil {
					cancel()
					return "", err
				}
				time.Sleep(2 * time.Second)
				cancel()
				_ = cmd.Wait()
				time.Sleep(3 * time.Second)

				out, err := runIn(e.distro, "pgrep -fa "+wsl.ShellQuote(marker)+" | grep -v pgrep || true")
				if err != nil {
					return "could not check for survivors (pgrep unavailable); unverified", nil
				}
				if strings.TrimSpace(out) != "" {
					return "", fmt.Errorf("the process survived cancellation:\n      %s\n      a stopped or "+
						"timed-out command keeps running inside the distro", strings.TrimSpace(out))
				}
				return "the distro-side process died with the relay", nil
			},
		},
	}
}

# wslprobe — verifying the WSL assumptions on a real machine

The WSL support in this repository was written on macOS. Every Windows-specific
claim behind it was derived from Go's source and Microsoft's documentation
rather than measured, because no Windows machine was available while it was
built. `wslprobe` turns each of those derivations into a measurement.

Run it before trusting a WSL project with real work, and before any release
that ships WSL support.

## Running it

From a **Windows** shell — PowerShell or cmd, not from inside a distro:

```
go run ./scripts/wslprobe -distro Ubuntu -dir \\wsl.localhost\Ubuntu\home\me\someproject
```

Both flags are optional: the default distro and its `$HOME` are used otherwise.
`-dir` must already exist. The probe writes only into a temporary directory it
creates inside the distro and deletes when it finishes.

Exit status is 0 when every assumption held. Any `FAIL` line names both what was
observed and what the code believes, so the consequence is explicit.

## What it checks, and what a failure means

| Probe | If it fails |
|---|---|
| `wsl -l -v` output parses | The distro picker is empty and no project can be routed. |
| An ordinary distro directory is not a reparse point | The path validator's link scan treats every path as suspicious and file tools refuse everything. |
| A Linux symlink appears as `ModeIrregular` | If it is neither `ModeSymlink` nor `ModeIrregular`, nothing recognises it as a link and `allowSymlinks=false` is bypassable — a symlink in the repo escapes the project boundary. |
| `EvalSymlinks` on a distro directory | If it reports "does not exist", `AddProject` rejects the project outright. Succeeding is fine: the tolerance path is then simply unused. |
| A directory Windows cannot name is still usable | Confirms the bash tool must adopt a directory it cannot `stat`; a `logs/2026-08-11T09:00:00` is legal in Linux and impossible in a Windows path component. |
| `WSLENV` carries an injected variable | Every injected setting and configured workspace variable is silently missing inside the distro. Also asserts the value never appears in argv, where other processes could read it. |
| Provider credentials do not leak | The app's API keys would be readable by any command the agent runs inside the distro. |
| A distro binary is invisible to host `LookPath` | Justifies `wsl.LookPathFor`. If the target-aware probe cannot find the binary either, `gh` and `gopls` are reported missing for every WSL project. |
| Killing the relay stops the distro process | A cancelled or timed-out command leaves work running inside the distro. |

## What it cannot check

`wslprobe` measures the boundary, not the product. These need the app itself:

1. **Add a WSL project.** Sidebar → add → pick a distro from the picker rather
   than typing a UNC path. It should appear once, not twice, if you later add
   the same repo spelled `\\wsl$\...`.
2. **Run a turn that touches files.** `read`, `edit`, `glob`, `grep` on files in
   the repo. Paths the model sees should be `/home/...`, not UNC.
3. **Run a shell command that changes directory**, then another. The second must
   start where the first ended. Try a directory with a colon in the name.
4. **`git status` / `git diff`** in the chat, and the diff review pane.
5. **The integrated terminal** — it should open inside the distro, at the repo.
6. **A PR action** (`gh`) if you use one, and `go_to_definition` in a Go repo.
7. **Stop a running turn** mid-command, then check inside the distro that
   nothing survived.
8. **A per-chat worktree.** These are skipped for WSL projects by design; the
   session should show a "shared checkout" chip rather than an error.

Report a failure with the full probe output — each line already states the
assumption it was testing.

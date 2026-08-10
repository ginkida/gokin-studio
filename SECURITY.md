# Security Policy

## Reporting a vulnerability

If you discover a security issue in Gokin Studio, please report it privately
via GitHub's **"Report a vulnerability"** button on the Security tab, or by
opening a regular issue ONLY if the issue does not give an attacker any
useful information (i.e. is a question, not an exploit).

Do **not** disclose the issue publicly until a fix is available.

## What we protect

Gokin Studio runs locally on the user's machine and talks to LLM provider
APIs. Below is what the app commits to protect; if you find a gap, please
report it.

### API keys and credentials

- Stored at `~/.config/gokin-studio/config.yaml` with mode `0600`
  (owner-read-only).
- **Never logged** to the event log, even by the audit pipeline that records
  "key set"/"cleared"/"updated" status (see `internal/studio/settings_audit.go`
  + `project_audit.go`).
- **Redacted** in event log messages if accidentally pasted in via a frontend
  error path. Patterns covered: `sk-*` (16+ chars after prefix),
  `Authorization: Bearer …` (8-200 char token), JWT `eyJ.*.*.` (8+ chars per
  segment). See `internal/studio/log_redaction.go`.
- Same redaction is re-applied when loading the on-disk event log
  (`event_log_disk.go::appendDirect`) so a legacy pre-redaction
  `events.log` cannot leak old secrets via Snapshot, CSV export, or
  backup archives.
- Sent only to the configured provider's HTTPS endpoint.

### Backups and restore

- `Settings → Backup` writes a `.tar.gz` of the entire config directory.
  Backups CAN contain user content (chat history, system prompts) and the
  same redacted event log — they may not be safe to share publicly.
- Restore extracts to a sibling staging directory first, validates that
  `config.yaml` is present at the archive root, and uses an atomic
  rename to swap. The previous config is moved aside as
  `.gokin-studio.pre-import-<stamp>` so the restore is reversible.
- Path-traversal entries (`../`, absolute paths) in archive entries are
  rejected before extraction (see `internal/studio/data_archive.go`).

### File-system access

- The "Show in OS file manager" action (`internal/studio/open_in_filemanager.go`)
  refuses to open any path that is not inside `configDir()`. The
  `HasPrefix(abs, cfg+Separator)` check defends against
  `<cfg>-adjacent` bypass.
- Backup `Delete`/`Restore` accept a basename only and validate it
  matches `.gokin-studio.pre-import-*`, `.gokin-studio.pre-restore-*`,
  or `auto-backup-*.tar.gz` with no path separators or `..` segments.

### Remembered tool permissions

- Manual-mode `Always allow` rules are scoped to one project and contain only
  an allowlisted tool name plus a creation timestamp. Arguments, shell
  commands, environment values, file content, connector payloads, and secrets
  are never stored in a rule.
- The runtime classifies every concrete call before consulting a remembered
  rule. Destructive variants, external/network operations, plugin force-ask,
  and execution without an enforced workspace sandbox cannot inherit a grant.
- Shell, delete, SSH, MCP/connectors, Browser/computer use, schedules, PR
  writes, delegation, and unknown tools are not eligible. All remembered
  rules and their project scope are visible and revocable in Context.

### Network calls

- Provider and update endpoints are HTTPS. Explicitly reviewed external
  Browser tabs may fetch a user-entered public HTTP(S) origin through a local
  per-tab proxy; loopback is also used for OAuth callbacks, previews, and that
  proxy.
- Provider connection probes use a 5-second `context.WithTimeout` so a
  hung provider cannot block the UI.

### External Browser isolation

- Navigation approves an exact public origin; schemes, ports, and subdomains
  do not inherit approval. DNS results are checked for private/reserved ranges
  and the validated public IP is pinned for the connection.
- Upstream redirects, links, forms, resources, cookies, headers, and body sizes
  are mediated by the per-tab proxy. Resource URLs are HMAC-bound to one exact
  target; changing the encoded target invalidates the signature, and using a
  signed resource URL as a document re-enters the origin review flow.
- macOS permits first-party page scripts only after installing a native
  `WKNavigationDelegate` policy. The main Wails document may initially load a
  reviewed loopback frame, while code inside a child frame is confined to its
  exact local scheme, hostname, and port. Direct public/private navigation,
  `_top`, new windows, embedded frames, and refresh/link headers fail closed.
  Platforms without that native boundary use the nonce-only script-disabled
  mode. In both modes, subresources still pass the SSRF-validating transport.
- Model inspection and coordinate actions route only to the visible active tab.
  Each non-list call receives a fresh exact-action confirmation, then rechecks
  the project/session/tab, bridge token, origin, and latest public URL. Returned
  DOM data and optional PNGs are schema- and size-bounded; external page text is
  untrusted content, never instructions.

### Desktop links

- `gokin://` accepts only documented `studio/new`, `studio/chat/<id>`, and
  `studio/project/<id>` routes with a bounded, single-valued parameter set.
  Unknown routes, fragments, userinfo, ports, duplicate parameters, invalid
  identifiers, and oversized URLs/prompts are rejected.
- A `q=` value is placed into an editable composer draft. A desktop link can
  never send the prompt, start an agent, invoke a tool, or approve an action.
- Raw links and prompt contents are not written to diagnostics. Short-lived
  duplicate detection stores only a SHA-256 digest of the URL.

### Desktop update checks

- Automatic release checks are notify-only, can be disabled in Settings, and
  run at most once per 24 hours against the fixed
  `api.github.com/repos/ginkida/gokin-studio/releases/latest` endpoint. Studio
  sends no project, prompt, credential, or machine identifier.
- Responses are size-bounded and must contain a stable canonical
  `vMAJOR.MINOR.PATCH` tag. Release-page URLs are constructed locally for the
  fixed repository instead of trusting a remote URL field.
- Studio does not download or install update artifacts in the background while
  the project uses self-signed/community builds. Releases include
  `SHA256SUMS.txt`, and the release workflow fails if the Git tag does not
  match the version embedded in the binary.

### Tool execution

- The `bash` tool runs commands with the user's shell. By design, it can
  execute any command the user could type — this is the documented
  feature, not a vulnerability. Use only with trusted projects.
- Path validation in `internal/engine/security/path_validator.go` keeps
  file-read/write tools inside the active project directory.
- Where a workspace-isolation backend is available, shell, test, and
  formatter commands run inside it: a macOS Seatbelt profile
  (`internal/engine/security/sandbox_darwin.go`) or a Linux bubblewrap
  namespace. Both fail closed — if the backend is unavailable the command
  is refused rather than run unconfined. An escape from that confinement
  IS in scope; see below.
- This release adds further local execution surfaces, all of which are in
  scope: repository plugin hooks, MCP stdio server launch, MCPB extension
  install, computer-use synthetic input, and dev-server launch from
  `.claude/launch.json`.

## Out of scope

- The integrated PTY terminal is a feature, not an attack surface — it
  intentionally launches the user's shell with their environment.
- API key values typed by the user into a chat message are stored in
  the session history (and any backups). This is accepted behaviour —
  the user is the one who put the key there.
- Hardening the host OS itself (System Integrity Protection, Gatekeeper,
  Windows integrity levels) is the operating system's job, not this app's.
  Note that this does NOT put the app's own workspace isolation out of
  scope: a way to escape the Seatbelt/bubblewrap confinement described
  under "Tool execution", or to make it silently run unconfined, is a
  reportable vulnerability.

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

### Network calls

- All external endpoints are HTTPS. The only HTTP endpoints in the
  codebase are `http://localhost:11434` (Ollama default) and OAuth
  callback URLs (also localhost).
- Provider connection probes use a 5-second `context.WithTimeout` so a
  hung provider cannot block the UI.

### Tool execution

- The `bash` tool runs commands with the user's shell. By design, it can
  execute any command the user could type — this is the documented
  feature, not a vulnerability. Use only with trusted projects.
- Path validation in `internal/engine/security/path_validator.go` keeps
  file-read/write tools inside the active project directory.

## Out of scope

- The integrated PTY terminal is a feature, not an attack surface — it
  intentionally launches the user's shell with their environment.
- API key values typed by the user into a chat message are stored in
  the session history (and any backups). This is accepted behaviour —
  the user is the one who put the key there.
- macOS/Windows OS-level sandboxing: this app does not enforce
  sandbox restrictions; that is the OS's job.

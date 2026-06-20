# Gokin Studio

Desktop IDE for running LLM coding agents against multiple project directories at once. Each project gets its own chat sessions, agent workspace, integrated terminal, and a shared 45+ tool registry (file ops, git, grep/glob, bash, web fetch, planning, memory).

Built with [Wails v2](https://wails.io) — Go backend + React/TypeScript frontend.

**Status:** v1.2.0 · 1636 tests (801 studio + 835 tools) · ~90% studio coverage.

## Features

### Agent runtime
- **Five providers**: GLM-5.1 (default, Zhipu AI), MiniMax, Kimi (`kimi-for-coding` on api.kimi.com/coding, 262K ctx), **DeepSeek V4** (`deepseek-v4-pro` with thinking + `deepseek-v4-flash` fast variant, 128K ctx), Ollama (local)
- **Per-project provider + model** — switch in the sidebar without restarting; quick switcher with `Ctrl+M`
- **Per-project thinking toggle** — enable/disable extended reasoning per project with configurable budget; Kimi enables thinking by default; purple Brain badge shows when active
- **True streaming** — text arrives token-by-token via `chat:delta`; thinking blocks render collapsible
- **Agent loop up to 50 turns per send** with a 30-minute ceiling; `Stop` button cancels at any point
- **Auto-retry on transient errors** (429, 5xx, network, stream idle) — exponential backoff 2s → 4s → 8s, retry banner with live countdown
- **Humanized errors** — 401 / 403 / 404 / context-length / network / timeout all get actionable messages instead of raw stack traces
- **Crash recovery** — every user turn, tool call, tool result, and assistant text is appended to a per-session JSONL replay log; the next time you open the session, an interrupted turn shows "Recovered N tool calls, partial reply" with [Recover into chat] / [Discard] buttons
- **Real provider tokens** — input/output token counts come from the API usage block, not `chars/4`; context gauge uses the latest round, per-turn cost uses the sum
- **Token cost estimator** — every assistant turn shows approximate USD cost; running session total in the chat header; per-project monthly **budget** with amber/red warnings at 80%/100%
- **Per-project pinned context** — `pin_context` tool persists a note that gets prepended to every new turn (survives compaction + restarts)

### Chat UI
- **Multiple chat sessions per project** with inline tab rename (double-click), auto-name from the first user message, drag-to-reorder, pin-to-top, names persist across restarts
- **Conversation forking** — right-click any user message → "Branch from here" creates a new session pre-populated up to that point; parent lineage shown as a GitFork icon
- **Message pinning** — bookmark any message; pinned list with jump-to in a modal
- **Inline diff viewer** for `edit` / `write` tool results — LCS line diff with ±3 lines of context, `+N / −M` summary
- **Colored unified diff** for `git_diff` (green/red/hunk-header styling)
- **Syntax-highlighted `read` results** — language auto-detected from extension via rehype-highlight
- **Grouped grep output** — matches grouped by file, pattern highlighted, counts badge, file sections collapsible
- **Clickable glob / changed-files lists** — click inserts `@path` into the chat input
- **Inline edit any user message** (Pencil) → trims server-side history + re-sends; retry (↺) trims + re-runs the same text
- **Up/Down arrow** in the chat input recalls previous user messages (terminal-style)
- **Drop files** onto the chat to attach as fenced code blocks; `@path/to/file.ext` tokens in your message auto-expand to the file's contents before sending (up to 10 files, 50 KB each)
- **Inline @path autocomplete** — type `@<chars>` for live file filter; ↑↓ Enter to insert
- **File picker modal** (`Ctrl+P` / `Cmd+P`) with fuzzy filter over the project tree
- **Command palette** (`Ctrl+K`) — fuzzy project switcher + actions (new chat, clear chat, open files, open settings, model switcher, view memory, search across sessions)
- **Context gauge** in the chat header with tooltip showing real input/output token counts; warns at 75% of the model's window
- **Elapsed time** in the generating pill (`12s`, `1m 23s`, `1h 5m`) with live output-tokens counter
- **Changed-files summary** at the top of each assistant reply lists every `edit`/`write` from that turn as clickable chips
- **Draft input** per session — typing is preserved when you switch sessions AND across crashes (debounced disk save)
- **Search** within a session (`Ctrl+F`) or across all sessions in the project (`Ctrl+Shift+F`)
- **Agent activity timeline** (`Ctrl+Shift+A`) — modal listing every tool call in the session with chronological elapsed times
- **Quiet mode** — collapse tool/thinking messages into "N hidden" markers; per-project preference persisted
- **Custom snippets** — `/<name>` chat input macros saved globally; user-defined system prompt templates
- **Unread badge** + optional **toast** + optional **chime** on completion in background sessions; per-project mute
- **Slash commands**: `/clear`, `/export`, `/exportall`, `/exportjson`, `/importsession`, `/system`, `/search`, `/sessions`, `/summarize`, `/diagnose`, `/help`, `/budget`, `/memory`, `+ user-defined`

### Tools
- **File**: `read`, `write`, `edit` (exact / regex / multi-edit / line-range / insert), `delete`, `move`, `copy`, `mkdir`, `list_dir`, `tree`, `glob`, `grep`, `diff`
- **Git**: `git_status`, `git_diff`, `git_log`, `git_blame`, `git_branch`, `git_add`, `git_commit`, `git_pr`
- **Shell**: `bash` (with long-output collapse >30 lines), `kill_shell`, `task`, `task_output`, `task_stop`
- **Web**: `web_fetch`, `web_search`
- **Memory** (wired to persistent per-project stores): `memory` (remember/recall/forget/list), `memorize` (project-learning), `shared_memory` (cross-project scratchpad), `pin_context` (persistent system-prompt addition), `history_search`, `update_scratchpad`
- **Planning**: `enter_plan_mode`, `update_plan_progress`, `get_plan_status`, `exit_plan_mode` — rendered as progress cards with step lists
- **Agent coordination**: `ask_agent` (dispatch to another project's agent), `coordinate` (structured multi-task plan with graceful fallback), `ask_user` (surface a question to the human in the UI)

Ollama gets a smaller toolset (`core` + `git`) to match smaller context windows; cloud providers get the full suite plus memory/planning/agent.

### Reliability & Release Polish
- **First-run onboarding wizard** — guided 3-step setup for new installs (provider → API key with Test Connection → first project)
- **React ErrorBoundary** — render errors show a recovery UI (Reload / Try-recover / Show stack) instead of a white screen; auto-logged
- **Global error capture** — `window.onerror` + `unhandledrejection` route to the backend event log so async failures are inspectable
- **Application event log** — bounded ring buffer (500 entries) of recent backend events (config save failures, chat:retry, chat:error, frontend errors); deduplication of repeats within 2s; **persisted to disk** at `~/.config/gokin-studio/events.log` with size-capped rotation so events survive across restarts
- **Secret redaction in event log** — `sk-*` API keys, Bearer tokens, JWTs auto-redacted to `<redacted:KIND>` markers BEFORE storage and ALSO on disk-replay (defense-in-depth across persistence + iter 750+ backup archives + iter 890+ CSV exports)
- **Logs viewer** — Settings → Diagnostics → View logs: live ring buffer with level filter (all/info/warn/error) AND source filter (settings/project/agent/...); export to CSV (Excel-friendly, redaction preserved)
- **Diagnostics report** — Settings → Run diagnostics: one-shot health check covering config writability, API key presence (honours env-var fallbacks `GLM_API_KEY` / `MINIMAX_API_KEY` / `KIMI_API_KEY` / `DEEPSEEK_API_KEY` / `OLLAMA_HOST`), project directory existence, stale replay logs; copyable plaintext for support tickets
- **Provider connection test** — Test Connection button on each API key in Settings probes the provider's API and reports OK/latency/error
- **Settings audit log** — every change to global settings + per-project provider/model/budget/thinking/prompt/pinned/etc. logged to event log with old → new values. **API key values + system prompt contents are NEVER logged** (only "set"/"cleared"/"updated" status)
- **Backup & Restore** — Settings → Backup & Restore:
  - **Manual backup** writes a portable `.tar.gz` of `~/.config/gokin-studio/` (config + history + drafts + pins + memory + snippets) downloadable from the browser
  - **Auto-backup** — opt-in daily snapshot on app startup, retains last 7
  - **Browse + restore** — list and selectively restore from past rollback snapshots (pre-import + pre-restore) AND auto-backups; safety pre-restore backup created automatically so any restore is itself reversible
- **Cleanup** — manual + auto: removes stale replay logs (>7d manual / >30d auto), old pre-import/pre-restore snapshots, orphaned import-staging dirs, excess auto-backups beyond retention. Opt-out toggle.
- **"Show in OS file manager"** — cross-platform Reveal-in-Finder/Explorer for config dir + backups subdir, with path-traversal hardening (path must be inside config dir, with separator guard against `<cfg>-adjacent` bypass)
- **Atomic config + history writes** — temp-file-then-rename so a mid-write crash can't corrupt your project list or chat history
- **Auto-backup of in-progress agent turn** — per-session replay log; survives kill -9

### Other
- **Integrated PTY terminal** via `creack/pty` and xterm.js — per-project scratch terminal, or split-view side-by-side with chat
- **Inter-project dispatch** — send a task from one project's chat to another, result returns as a styled card
- **Smart welcome screen** — detects git state (branch, uncommitted files, recent commits) and language (Go/Python/Rust/Swift/Dart/Zig/C#/C++) for relevant first prompts
- **Project export/import** — full JSON envelope with project metadata + every session; bulk session export as Markdown or per-session as JSON
- **Dark / Light themes** — toggle in Settings

## Quick Start

### Prerequisites

- [Go 1.25+](https://go.dev/dl/)
- [Node.js 18+](https://nodejs.org/)
- [Wails CLI v2](https://wails.io/docs/gettingstarted/installation)

### Development

```bash
wails dev
```

### Build

```bash
wails build
# Binary: build/bin/gokin-studio
```

### Tests

```bash
go test ./internal/studio/ -v
go test ./internal/engine/tools/ -v
```

910 tests total (677 studio + 233 tools), 90.5% studio coverage. Covers: agent loop (tool execution, panic recovery, retry, context cancellation, chained tool rounds, parallel function calls), all public API methods (project CRUD, sessions, history, dispatch, memory, terminal wrappers), helper functions, config round-trip, replay logger, diagnostics + event log dedup + disk persistence, secret redaction patterns, fork lineage, usage stats, search, drafts, pins, snippets, project/session import/export, budget validation, provider health probes, manual + auto cleanup (with auto-backup retention), pre-import + pre-restore backup management (path-injection guards), settings + project audit logs (with secret-leak regression guards), "show in file manager" path-traversal defense, CSV log export.

## Architecture

```
main.go                  Wails entry point, embeds frontend/dist
internal/studio/         Wails-bound application layer
  app.go                 Studio: project/terminal/settings management, all public methods are Wails bindings
  project.go             Project workspace, agent loop, tool execution, memory/plan wiring
  session.go             ChatSession: per-session history, cancelFn, usage stats, pinned flag
  config.go              YAML config (~/.config/gokin-studio/config.yaml, 0600)
  history.go             Session history persistence (v1 bare / v2 versioned + parent + usage), atomic writes
  replay.go              Crash-recovery replay buffer (JSONL per session)
  messenger.go           StudioMessenger: routes ask_agent calls to other projects
  shared_memory.go       Process-wide cross-project scratchpad for shared_memory tool
  terminal.go            PTY sessions
  events.go              Event types (chat:delta / :text / :thinking / :tool_call / :tool_result / :complete / :error / :retry / :usage / :ask_user)
  diagnostics.go         Version constant, BuildInfo, GetDiagnostics, health checks (honours env-var fallbacks)
  event_log.go event_log_disk.go event_log_csv.go log_redaction.go  Ring buffer + disk persistence with rotation + CSV export + secret-redaction patterns
  resolved_settings.go   ResolveProviderKey (API-key lookup: setting > env > default)
  settings_audit.go project_audit.go  Diff old vs new state, log to event log; key + system-prompt VALUES never logged
  data_archive.go auto_backup.go backup_management.go  Manual + auto tar.gz backup of configDir; browse, restore, delete past snapshots; shared atomic-swap extractor
  cleanup.go             Manual + auto cleanup of stale replay logs, pre-import/pre-restore snapshots, orphaned staging dirs, excess auto-backups
  open_in_filemanager.go Cross-platform Reveal-in-Finder/Explorer (with path-traversal hardening)
  pricing.go             Per-model token pricing for cost estimator
  budget.go              Per-project budget validation
  drafts.go pins.go      Per-session draft text + bookmarked messages
  session_pins.go session_order.go  Sidebar tab pinning + drag-reorder persistence
  prompt_templates.go user_prompt_templates.go user_snippets.go  Curated + user-saved prompt library
  provider_health.go     Per-provider connectivity probe ("Test Connection") — honours env-var fallback
  git_status.go          Per-project git branch + uncommitted state for welcome screen
  fork.go                Conversation forking (Branch-from-here)
  summarize.go           One-shot LLM TL;DR of current session
  project_export.go session_export.go  JSON export/import envelopes
  usage_stats.go usage_csv.go         Per-project spend totals + CSV export
internal/engine/         Core engine (reused from cli tool)
  client/                Multi-provider LLM abstraction with pool + fallback + retry
  tools/                 45+ tool implementations, registry with ToolSet groups
  memory/                Persistent memory store + project-learning (wired to studio)
  plan/                  Plan manager + step tracking (wired to studio)
  agent/, context/, chat/  Additional framework — agent runner NOT wired (studio uses its own loop in project.go)
frontend/src/
  App.tsx                Root layout: sidebar + tab bar + status bar; mounts ErrorBoundary + onboarding wizard
  components/
    chat/ChatPanel.tsx   Main chat (streaming, tool cards, diff viewer, file picker, inline edit, recovery banner, search, activity timeline, summary/help/budget/pins modals)
    terminal/Terminal.tsx xterm.js + fit addon
    layout/Sidebar.tsx   Project list with search, drag-pin, mute, right-click menu
    layout/StatusBar.tsx Active project, provider/model, thinking + budget indicators
    layout/ToastStack.tsx  Completion toasts + budget-threshold warnings + audio chime
    layout/ErrorBoundary.tsx  React error boundary + global window error handlers
    onboarding/OnboardingWizard.tsx  First-run 3-step setup
    files/FilePicker.tsx Ctrl+P quick-open modal
    files/FileBrowser.tsx File-tree browser (Files tab), click-to-insert into chat
    project/ProviderSelect.tsx Per-project provider/model + thinking toggle
    palette/CommandPalette.tsx Ctrl+K command/project palette
    settings/SettingsPage.tsx API keys, default provider/model, Ollama URL, theme, diagnostics, logs viewer, snippets
  stores/                Zustand: projectStore, chatStore (messages + streaming + usage + drafts + retrying + unread), settingsStore
  hooks/useWailsEvents.ts Backend event subscriptions
  lib/mutedProjects.ts   Per-project localStorage prefs (mute, quiet, budget alert state, reset-all)
  style.css / App.css    Design tokens + component styles
```

## Key Flows

### Agent turn
1. User sends a message → appended to session history; replay log gets a `user` event
2. If the message contains `@path/to/file.ext` tokens, the frontend reads each file via `ReadFileContent` and appends its contents as fenced code blocks before handing the prompt to the backend
3. Backend compacts history if over the model's context window (preserves first message + last 3 exchanges, never splits `function_call` / `function_response` pairs) and calls `client.SendMessageWithHistory`
4. `ProcessStream` streams chunks; text deltas go out as `chat:delta`, tokens go out as `chat:usage` (last round + total so far)
5. If the response has `FunctionCalls`, each is executed against the tool registry; call + result events go out and get appended to the replay log
6. Loop until the model returns no more tool calls, or hits the 30-minute ceiling, or the user hits Stop
7. `chat:complete` fires with final token totals + estimated USD cost + pinned context state + current git branch; the replay log is deleted (authoritative state now lives in the versioned `{projectID}_{sessionID}.json` history file)

### Crash recovery
- Every turn event is appended to `~/.config/gokin-studio/history/{projectID}_{sessionID}.replay.jsonl`
- On normal completion, the file is deleted
- On abnormal termination (process kill, OS crash), it stays
- On next session load, `GetRecoveryEvents` reads it; the UI shows a banner summarising what was in flight and offers Recover / Discard
- Recover replays the tool calls / results / partial assistant text into the chat view — purely informational. Server-side history only carries the user message from the interrupted turn; if you want the agent to resume from where it was, tell it so in the next prompt

### Diagnostics & Logs
- **Settings → Run diagnostics** displays version, runtime, storage stats, and a list of health checks (config writability, API key presence, project dir existence, stale replays)
- **View logs** button inside the diagnostics modal opens the application event log (500-entry ring buffer); filter by Info / Warn / Error; events dedup'd within a 2s window with `×N` count
- **Copy report** + **Save as file** export a plaintext diagnostic report for support tickets

## Configuration

Settings live at `~/.config/gokin-studio/config.yaml` (created on first run, `0600` permissions).

### API keys

Any of:
- Set via the in-app **Settings** page (paste + "Test connection" button per provider)
- Or via env vars: `GLM_API_KEY`, `MINIMAX_API_KEY`, `KIMI_API_KEY`, `DEEPSEEK_API_KEY`
- Ollama: `OLLAMA_HOST` (default `http://localhost:11434`)

### Override config dir

```bash
GOKIN_CONFIG_DIR=/tmp/gokin-sandbox ./build/bin/gokin-studio
```

Useful for testing or running against a scratch config.

### Storage layout

```
~/.config/gokin-studio/
  config.yaml                              Project list + global settings (0600)
  history/                                 Per-session JSON history + crash-recovery replay logs
    {projectID}_{sessionID}.json
    {projectID}_{sessionID}.replay.jsonl   Deleted on chat:complete
  memory/{projectHash}.json                Persistent memory store entries
  drafts/{projectID}_{sessionID}.txt       Unsent chat input
  pins/{projectID}_{sessionID}.json        Bookmarked messages
  session-pins/{projectID}.json            Pinned session IDs per project
  session-order/{projectID}.json           User-defined tab ordering
  user_prompt_templates.json               Saved system-prompt templates
  user_snippets.json                       Saved /<name> chat-input macros
  events.log                               Persistent event log (size-capped, secret-redacted)
  backups/                                 Daily auto-backup tar.gz files (last 7 kept)
    auto-backup-YYYY-MM-DD.tar.gz
  .last-auto-cleanup                       mtime sentinel for 24h auto-cleanup throttle
  .last-auto-backup                        mtime sentinel for 24h auto-backup throttle
```

Sibling-of-`gokin-studio/` (not inside it):
```
.gokin-studio.pre-import-<stamp>/          Rollback snapshot from manual Import (90-day retention)
.gokin-studio.pre-restore-<stamp>/         Rollback snapshot from any Restore operation
.gokin-studio.import-staging-<stamp>/      Orphaned only on crashed Import (always pruned)
```

## Keyboard shortcuts

| Shortcut | Action |
|----------|--------|
| `Ctrl/Cmd + 1` | Switch to Chat view |
| `Ctrl/Cmd + 2` | Switch to Files view |
| `Ctrl/Cmd + 3` | Switch to Settings |
| `Ctrl/Cmd + B` | Toggle sidebar |
| `Alt + 1..9` | Jump directly to session N in the current tab order |
| `Ctrl/Cmd + K` | Command palette (fuzzy switcher + actions) |
| `Ctrl/Cmd + M` | Quick model switcher (Spotlight-style) |
| `Ctrl/Cmd + P` | File picker (quick open) |
| `Ctrl/Cmd + F` | Search messages in current session |
| `Ctrl/Cmd + Shift + F` | Search across all sessions in the project |
| `Ctrl/Cmd + Shift + A` | Agent activity timeline |
| `Ctrl/Cmd + Shift + P` | Focus sidebar project search |
| `Ctrl/Cmd + /`  or  `?` | In-app help modal |
| `Ctrl/Cmd + T` | New chat session |
| `Ctrl/Cmd + L` | Clear current chat |
| `Ctrl/Cmd + PgUp/Dn` | Cycle chat sessions |
| `Ctrl/Cmd + \`` | Toggle integrated terminal |
| `Enter` | Send message |
| `Shift/Ctrl + Enter` | Newline in message input |
| `Up arrow (empty input)` | Recall previous user message (Down to walk forward) |
| `j / k` | Navigate messages (vim-style) |
| `Esc` | Close modal / stop agent / cancel onboarding |
| Right-click message | Pin / Branch from here / Edit (user) |
| Right-click project | Rename / Pin / Mute notifications / Export / Delete |
| Right-click session tab | Pin to top / Rename |
| Double-click session tab | Rename session |

## License

MIT

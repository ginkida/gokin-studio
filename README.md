# Gokin Studio

Desktop IDE for running LLM coding agents against multiple project directories at once. Each project gets its own chat sessions, agent workspace, integrated terminal, and a shared 45+ tool registry (file ops, git, grep/glob, bash, web fetch, planning, memory).

Built with [Wails v2](https://wails.io) — Go backend + React/TypeScript frontend.

**Status:** v2.0.0 · GLM/Kimi-only desktop runtime.

The current Claude Desktop/Cowork comparison, including the deliberate
local-vs-cloud boundary, is tracked in [docs/CLAUDE_DESKTOP_PARITY.md](docs/CLAUDE_DESKTOP_PARITY.md).

## Features

### Agent runtime
- **Focused two-provider runtime**: GLM through Z.AI (`glm-5.2` latest/default with 1M context, plus `glm-5.1`, `glm-5`, `glm-5-turbo`, `glm-4.7`) and Kimi Code (`k3` latest with a 1,048,576-token context and native vision, `k3-256k`, `kimi-for-coding`, HighSpeed). Legacy provider configs are migrated to GLM and backend RPCs reject unsupported provider/model pairs. The picker exposes verified context, modality, reasoning, and output-limit metadata instead of a bare model ID.
- **Account-aware first-run model choice** — onboarding compares only GLM and Kimi, shows context/image/reasoning capabilities, preserves separate credential drafts, and lets the user choose the first-project model. A connection probe narrows the list to models advertised for that account and safely falls back to its recommended eligible model if the earlier choice is unavailable.
- **Native Kimi image input**: select, paste, or drag PNG/JPEG/GIF/WebP images into the composer. Media is sent as native multimodal content, stored as deduplicated binary blobs beside session history, and preserved by session/project export. Switching to text-only GLM keeps the images in history but omits them from provider context with a warning.
- **Per-project provider + model** — switch in the sidebar without restarting; quick switcher with `Ctrl+M`. Its account-aware GLM/Kimi listbox supports Arrow/Home/End navigation, exposes the actually applied model separately from keyboard focus, and reviews changes before commit. The composer keeps the active identity visible with provider-colored iconography, verified context size, native-vision capability, reasoning control, and connection state in an adaptive chip that collapses cleanly with the conversation width. Model changes use an in-app review step for context compaction, image omission, and prompt-cache invalidation instead of a native browser prompt. The Settings default applies only when a new project is created, so saving preferences never silently changes existing workspaces.
- **Native flagship reasoning controls** — the bundled GLM 5.2 baseline uses its documented `high`/`max` `reasoning_effort` contract (default `max`), while Kimi K3 uses `low`/`high`/`max` (Coding default `high`). Newer numeric GLM/Kimi families appear only after authenticated account discovery and inherit the matching native effort UI/protocol; unrelated model families remain blocked. Older catalog models keep the compatible thinking toggle/budget control. GLM Off sends an explicit `thinking.type=disabled`, so the provider default cannot silently turn it back on.
- **True streaming** — text arrives token-by-token via `chat:delta`; thinking blocks render collapsible
- **Agent loop up to 50 turns per send** with a 30-minute ceiling; `Stop` button cancels at any point
- **Auto-retry on transient errors** (429, 5xx, network, stream idle) — exponential backoff 2s → 4s → 8s, retry banner with live countdown
- **Automatic context-overflow recovery** — provider 400/413 rejections trigger bounded emergency compaction of the request snapshot and retry without rewriting the stored conversation or dropping the current task/tool result
- **Workspace-isolated code execution** — GLM 5.2/Kimi K3 `bash`, background shell tasks, `run_tests`, `verify_code`, automatic formatting, and post-edit delta checks share one fail-closed filesystem boundary and a private environment with no inherited API keys or host language caches. macOS uses a real `sandbox-exec` profile: only the connected project/private runtime are writable, while the real HOME, external volumes, Keychain access, Apple Events, and all IP sockets are blocked. Linux uses bubblewrap when its user-namespace probe succeeds, hiding HOME, mounting the host root read-only except for project/runtime binds, and unsharing the network namespace. A command/test/verification run can request full host networking through `network_access`, but that always receives a fresh exact-action approval card warning that the grant includes LAN/private services. Validation tools reject absolute, `..`, and symlink escapes. Built-in Git operations disable repository-controlled executable hooks and filesystem monitors; reviewed plugin hooks remain separate and explicit. Windows or Linux without bubblewrap is reported honestly as host execution and every model-requested exact command requires a fresh action approval—even in Skip—rather than silently claiming to be sandboxed; automatic formatter/delta execution fails closed.
- **Humanized, recoverable errors** — 401 / 403 / 404 / context-length / network / timeout failures show a concise explanation, collapsible technical details, and the relevant next action: open Settings, choose an available model, start a fresh chat, or retry the exact last text turn without overwriting the current composer draft
- **Crash recovery** — every user turn, tool call, tool result, and assistant text is appended to a per-session JSONL replay log; the next time you open the session, an interrupted turn shows "Recovered N tool calls, partial reply" with [Recover into chat] / [Discard] buttons
- **Real provider tokens** — input/output token counts come from the API usage block, not `chars/4`; context gauge uses the latest round, per-turn cost uses the sum
- **Write-only local environment editor** — Settings securely applies global variables to new agent commands, background tasks, integrated terminals, and preview servers. Saved values never cross back into React or enter Studio config, logs, exports, and backups: macOS uses a dedicated Keychain item and Windows uses a user-bound DPAPI blob. Variable names remain visible for replace/remove workflows; isolation-owned and loader-injection variables are rejected, secure-storage failures leave the previous active environment intact, and project `.claude/launch.json` values retain preview-specific precedence.
- **Token cost estimator** — every assistant turn shows approximate USD cost using current published GLM 5.2/5.1/5/Turbo/4.7 and Kimi K3 API-equivalent rates; running session total in the chat header; per-project monthly **budget** with amber/red warnings at 80%/100%. Kimi Code membership is credit-based, so its dollar figure remains an API-equivalent estimate rather than an invoice.
- **Per-project pinned context** — `pin_context` tool persists a note that gets prepended to every new turn (survives compaction + restarts)
- **Project knowledge from files or URLs** — attach bounded text/code/PDF/DOCX sources or explicitly fetch a public HTTP(S) page into a persistent local snapshot. Relevant excerpts are retrieved per turn behind an untrusted-content boundary, preserving GLM/Kimi prompt-cache stability. URL sources never refresh in the background: the Context panel shows the original URL and requires an explicit refresh; failed refreshes preserve the last good snapshot. URL validation blocks credentials, local/private/link-local targets and unsafe redirects, and web dialing pins the validated public IP to close the DNS-rebinding gap.
- **Reversible project archive** — hide an idle workspace without deleting its chats, memory, knowledge, artifacts, drafts, pins, or scheduled routines. Archived routines are suspended and do not hold the wake inhibitor; restore rebases them to the next future occurrence so missed runs do not burst. Archive/restore uses bounded atomic metadata with active-wins crash reconciliation, while permanent Delete remains a separate destructive action.
- **Moved-folder recovery** — when a connected workspace is renamed or moved outside Studio, Chat, Files, and Artifacts expose the same native folder picker and relink the existing project instead of forcing delete/re-add. The durable project ID, GLM/Kimi settings, chats, drafts, pins, schedules, artifacts, and knowledge remain intact; path-keyed agent memory is merged into the new namespace while the source copy is retained for recovery, and every path-bound client/tool/sandbox is rebuilt before the next turn.
- **Exact workspace continuity** — restart into the last selected project, validated chat session, and Chat/Files/Artifacts/Settings location instead of merely choosing the project whose agent ran most recently. The bounded frontend record stores IDs only; corrupt, stale, deleted, or oversized data falls back to backend-authoritative project/session ordering. Title-bar buttons, `Ctrl/Cmd+[ ]`, and dedicated Mouse Back/Forward buttons share the same validated project-local trail; auxiliary mouse events are consumed before WebView browser history can replace the Studio document, and approval modals/Quick Entry retain navigation ownership.

### Chat UI
- **Session-scoped 2D pane workspace** — Chat, file-by-file Diff with inline comments, Browser preview, Terminal, Files, Artifacts, Plan, scheduled Tasks, and Context can stay open together in a persisted recursive row/column split tree. Drop a pane on any left/right/top/bottom edge, or use its accessible directional controls; mouse and keyboard separators resize either axis. Stable keyed pane hosts preserve live chat, terminal, and browser state while rearranging. The Views menu restores closed panes, `Cmd/Ctrl+\\` closes the focused non-chat pane and collapses its empty branch, old horizontal layouts migrate automatically, and the global reset returns to Chat + Context. Project/context events route into the corresponding pane instead of stacking a modal. The quiet title bar still exposes project-local Back/Forward history across chats, Files, Artifacts, and Settings, while Settings drafts, reviews, diagnostics, and scroll state preserve their existing durability rules.
- **Multiple chat sessions per project** with inline tab rename (double-click), auto-name from the first user message, drag-to-reorder, pin-to-top, and durable names. Closing a tab reversibly archives the idle chat while preserving its transcript, draft, and worktree; the Archived chats manager restores it or offers a separately confirmed permanent delete. Overflowed tabs automatically reveal the active chat after search/shortcut navigation, accept horizontal wheel scrolling, expose roving Arrow/Home/End keyboard navigation, and use the existing confirmation flow for middle-click or Delete-key removal.
- **Automatic Git worktrees for parallel chats** — the first chat in a newly connected Git project and every new/forked chat receive a persistent managed checkout and branch. Agent file/Git/shell tools, private background tasks and plan state, the integrated terminal, `@path` autocomplete/expansion, chat file picker, side-chat context, Git summary, and PR monitor all follow the selected session. A root `.worktreeinclude` uses gitignore syntax to copy only explicitly matched files that Git also considers ignored (for example `.env`). Checkout hooks are disabled. Missing or tampered worktrees fail closed instead of falling back to shared files; dirty sessions cannot be deleted or shutdown-pruned, clean unused branches are removed, and branches containing new commits are retained.
- **Multi-tab session terminal** — each Terminal pane can keep several independent PTYs alive in the selected chat worktree. `+` adds a root tab; right-clicking a folder in Files or a directory path in chat opens another tab at that directory. Tabs expose status, close/restart, roving Arrow/Home/End navigation, and Delete-to-close. Backend cwd resolution rejects traversal, `.git`/`.gokin`, non-directories, and all symlink components before launching the shell.
- **Visual change review with inline feedback** — the `+N −M`/Review changes entry opens the active session worktree file-by-file, including staged, unstaged, untracked, renamed, deleted, binary, and safely truncated changes. Click a changed line to collect comments, use Enter to add each one, then Cmd/Ctrl+Enter to place one line-addressed feedback request in the GLM/Kimi composer rather than auto-sending it. **Review code** explicitly starts a read-only high-signal audit and renders the model's validated findings beneath exact diff lines; session, fingerprint, path, side, line, severity, count, and text bounds are enforced before display, stale results are rejected, and **Ask to fix** remains a reviewable draft. The file list supports Arrow/Home/End navigation, and Git summary, plan progress, review, and Commit all share the selected session checkout.
- **Session-aware live app preview and auto-verify** — `Ctrl/Cmd+Shift+P` opens a resizable embedded browser backed by the active chat worktree. `Ctrl/Cmd+Shift+S` or the crosshair selects an element inside the preview: a nonce-authorized in-frame overlay intercepts the click before the page can act, returns a fixed bounded DOM/ancestor/rectangle schema with query tokens removed, and creates a reviewable draft without sending it. Claude-compatible `.claude/launch.json` JSONC supports multiple configurations, `runtimeExecutable`/`runtimeArgs`, `program`/`args`, `cwd`, bounded repository `env`, `port`, `autoPort`, loopback `url`, and `autoVerify`; package.json `dev`/`start` scripts are detected as an unsaved fallback. Every repository-defined launch summary is shown in an exact review before direct execution, receives an isolated private HOME/cache plus loopback HOST/PORT, and exposes bounded logs, start/stop, reload, and external-browser controls. URL-only configurations can attach to an already running localhost server; external launch-config URLs stay blocked and use the separately reviewed Browser tabs instead. Clicking project-relative HTML/PDF/image/video paths in chat markdown or changed-file chips, Files, or Diff opens that exact session file in the same Browser pane. Static HTML keeps directory-relative CSS/JS/media through a bounded read-only loopback server; `os.Root`, an unguessable HttpOnly bootstrap, an offline CSP, extension/size allowlists, and a unique origin prevent traversal, outward-symlink reads, unauthenticated local access, and external exfiltration. A private ephemeral proxy preserves relative assets/HMR and adds a nonce-bound bridge. Each run receives a unique browser origin, and the explicit **Persist sessions** control can retain that configuration's bounded cookies and localStorage across server/app restarts without sharing them with another chat; disabling it, clearing the profile, or deleting its chat/project removes the private data. The session-routed `preview_browser` tool returns DOM, controls, runtime/resource errors, and a bounded viewport PNG from either the visible static file or running dev server to the active model turn; it can click, fill, scroll, and send a small key allowlist, while refusing actions that would leave the preview origin. With `autoVerify`, edits invalidate older evidence and trigger a bounded inspect-before-completion requirement. Closing a session/project or the app kills every server and diagnostics proxy.
- **Isolated external Browser tabs** — the Preview pane also hosts up to eight public HTTP(S) tabs per chat. Enter an address or click a web link in a response (Cmd/Ctrl-click keeps the system-browser path); Studio reviews the exact origin before first navigation and offers Allow once or Always allow. Every tab uses an unguessable loopback hostname, HttpOnly bootstrap, ephemeral per-tab cookie jar, bounded response/request bodies, and an SSRF-safe transport that validates every DNS answer and pins the connected public IP. Cross-origin links and redirects return to the same review flow, while a new local origin prevents one site's storage from reaching another. On macOS, normal first-party page scripts and event handlers run behind a native `WKNavigationDelegate` guard: React may initially load a reviewed loopback frame, but code inside that frame can navigate only within its exact local scheme/host/port. Direct public/private navigation, `_top`, new windows, nested frames, refresh headers, and unsigned/tampered resource URLs fail closed; static cross-origin resources are target-signed and every fetch still passes the SSRF transport. Hosts without an equivalent native guard automatically retain the nonce-only script-disabled mode. The session-routed `external_browser` tool can list safe tab metadata, inspect bounded visible DOM/diagnostics, and run reviewed coordinate click/fill/scroll/key actions only in the backend-recorded active tab. Every read or action receives a fresh exact-action confirmation—even in Skip mode—then revalidates the tab, bridge token, exact public URL, and origin; page content is always treated as untrusted data. Exact persistent origins can be revoked in Settings, never imply subdomains/ports, and are excluded from exports, auto-backups, and imports. Private, loopback, link-local, reserved, credential-bearing, and non-HTTP URLs remain blocked. Unsupported protocols, cross-origin dynamic APIs, nested embeds, DRM, WebSockets, or complex anti-proxy behavior still need the default browser.
- **Keyboard-orderable project sidebar** — project actions expose Move up/down through right-click, `Shift+F10`, or the Menu key, with the same durable ordering and pinned-group boundaries as drag-and-drop. Long action menus remain viewport-bounded and scrollable.
- **Desktop session-file tree and editor** — Files follows the active chat worktree and uses real tree/treeitem semantics with one roving tab stop. Arrow keys navigate visible entries, Left/Right collapse or expand folders, Home/End jump to the edges, and first-letter search moves without touching the mouse. Clicking ordinary source/text paths in Files, chat markdown, changed-file chips, or Diff opens a bounded UTF-8 editor in that chat's Files pane; `Cmd/Ctrl+S` writes atomically while a content revision detects agent/terminal changes and offers explicit Discard/Override instead of silent clobbering. Right-clicking a file path on those surfaces exposes the same session-resolved actions: attach it to the composer without sending, open it in an installed VS Code/Cursor/Zed, reveal it in Finder/Explorer, or copy its absolute worktree path. The header copies the absolute worktree path, Add to chat remains reviewable, unsaved navigation is confirmed, and accidental session switches retain at most eight in-memory drafts for the current app run. `.git`/Studio metadata, non-regular or outward-symlink paths, and traversal are rejected before any external app launches; binary/oversized/read-only files remain non-editable. HTML/PDF/image/video still route to Browser Preview and Office files to Artifacts.
- **Keyboard-first artifact library** — `Ctrl/Cmd+F` focuses its local search, Escape clears it, and Arrow/Home/End keys move one roving focus through the responsive card layout. Unavailable previews remain readable through `aria-disabled` cards instead of disappearing from the keyboard path.
- **Lazy workspace keep-alive** — after first use, Files preserves expanded folders and scroll position while Artifacts preserves its query, filter, sort, focus, scroll, preview width, and independent selected preview when returning to chat. Hidden live previews stop filesystem polling and unmount their sandboxed iframe, including network-enabled frames; reactivation performs a quiet freshness check while restoring the preview.
- **Follow-up queue while the agent works** — keep typing and queue up to 8 ordered text, PDF/Office, or Kimi-image turns; remove pending items individually, while Stop safely cancels the run and clears unattended follow-ups
- **Cross-session agent coordination** — GLM 5.2 and Kimi K3 can list the 20 most recently active local Studio chats except their own, read a bounded recent transcript without hidden reasoning or attachment bytes, send a visibly quoted and source-attributed message, or rename another ordinary chat. A free target starts immediately; a busy target receives the message through its existing ordered queue. Exact project/session IDs are re-resolved without falling back to another chat, current-session self-targeting is rejected, and unattended scheduled/plugin-agent runs cannot send or receive cross-session messages.
- **Local scheduled tasks + background readiness** — interval/daily/weekdays/weekly prompts plus manual-only routines, with pause, edit, delete, and Run now controls. GLM 5.2 and Kimi K3 can also list or create/manage routines through `scheduled_task`; every mutation receives an exact-action approval card showing prompt, cadence, local time, model, and future-run permission mode, even when the project uses Skip. Each run gets its own inspectable child chat and bounded status history while the desktop app is open. An explicit battery-aware setting acquires one ref-counted OS sleep inhibitor for concurrent agent runs and, when any automatic schedule is enabled, keeps the machine awake until schedules are paused/removed or the setting/app is shut down; manual-only routines never hold a wake lease. macOS uses `caffeinate -i`, Windows uses thread-scoped `SetThreadExecutionState`, and Linux uses `systemd-inhibit` when available; live status and acquisition errors are visible in Settings.
- **Local + remote MCP connectors** — manage stdio servers and remote Streamable HTTP endpoints from Settings. Install `.mcpb` desktop extensions through a manifest review/configuration flow; packages are digest-bound, traversal/symlink/zip-bomb guarded, atomically extracted, and disabled by default until tested. HTTP connectors support JSON/SSE responses, bearer/custom headers, or browser-based OAuth 2.1: RFC 9728 protected-resource discovery, RFC 8414/OIDC server metadata, mandatory S256 PKCE, resource audience binding, public-client DCR or an explicit client ID, rotating refresh tokens, and local disconnect. OAuth secrets never enter connector JSON or logs—they live in macOS Keychain or user-bound Windows DPAPI storage. Requests carry MCP `2026-07-28` metadata and `x-mcp-header`, with automatic negotiation back to stateful 2025 servers.
- **Claude-compatible plugins** — review and install ZIP bundles containing `.claude-plugin/plugin.json`, Skills, commands, specialist agents, dedicated `.mcp.json` or inline `mcpServers`, hooks, and scripts. Archives are digest-bound and traversal/symlink/zip-bomb guarded; scripts lose executable bits and new plugins stay disabled. Hooks have a separate command-by-command review and digest-bound arm switch: supported `PreToolUse`, `PostToolUse`, and `PostToolUseFailure` command handlers run with bounded time/output, while unsupported lifecycle or handler types stay visible but inert. A hook may deny, request approval, add context, or rewrite tool input, but rewritten input is validated and passed through the normal Manual/Auto/Skip policy again; hook “allow” can never bypass a Studio gate. Plan disables repository plugin hooks completely. Updating, disabling, or changing a hook file invalidates its arm state. Enabled `agents/*.md` definitions become a dynamic `plugin_agent` catalog: every delegation passes the normal approval gate, runs on the project's selected GLM/Kimi model in a separate inspectable child chat, enforces the agent's optional tool allowlist, blocks recursive delegation, and ignores plugin attempts to weaken permission/model policy. Bundled stdio/HTTP/OAuth connectors have a second exact-definition review, reject executable `headersHelper`, are re-read against a digest, receive collision-safe names, and import disabled without starting a process or contacting a server. `${CLAUDE_PLUGIN_ROOT}` is root-bound at import; `${ENV_VAR}` secrets remain unresolved on disk and expand only in memory when connecting. Enabled Skills use progressive disclosure through a read-only, root-confined resource tool, while commands appear as namespaced `/plugin-command` composer templates that never auto-send.
- **Conversation forking** — right-click any user message → "Branch from here" creates a new session pre-populated up to that point; parent lineage shown as a GitFork icon
- **Message pinning** — bookmark any message; pinned list with jump-to in a modal
- **Inline diff viewer** for `edit` / `write` tool results — LCS line diff with ±3 lines of context, `+N / −M` summary
- **Professional native documents** — GLM 5.2 and Kimi K3 can create editable DOCX reports, formula-aware XLSX workbooks, 16:9 PPTX decks, and UTF-8 PDF deliverables through the bounded `document_create` tool. Output is atomic and capped at 30 MiB; new files follow reviewed Auto while replacement is always an exact-action approval. Office formulas reject executable/external payloads, and the `read` tool extracts native Office content for later analysis.
- **Direct document attachments** — drag, paste, or pick PDF, DOCX, XLSX, and PPTX files for either GLM 5.2 or Kimi K3. Files are capped at 30 MiB each / 60 MiB per turn, stored outside history JSON, shown as downloadable message cards, and delivered to the model as bounded text behind an explicit untrusted-document boundary. Native images remain Kimi-only.
- **Inline custom visuals and artifact previews** — generated HTML/HTM/SVG, DOCX, XLSX, PPTX, and PDF appear as lazy inline cards in the GLM/Kimi conversation and can expand without leaving the answer. The same files open from Files/changed-file chips in a full preview. Every read follows that chat's isolated worktree. Web artifacts refresh live, support responsive widths and privately deduplicated per-session history (20 versions, 128 MiB/project), and run origin-isolated with external network access requiring opt-in. Restore changes only the selected worktree; deleting a chat removes its snapshots. Native Office previews are passive and never execute macros, scripts, external links, or embedded objects; downloads preserve the original file.
- **Standalone Artifacts library** — `Ctrl+4` opens a searchable catalog of HTML/SVG dashboards and native Office/PDF deliverables from the active chat worktree. Filter by type, sort by recency/name/size, see version counts and preview-limit warnings, then open the same secure full preview without finding the originating message. “New artifact” and “Update” hand a source-aware task to the current GLM/Kimi composer for review—never auto-send it or expose connector credentials to the iframe. Discovery is bounded, symlink-safe, and ignores private dependency trees while retaining generated `dist`/`build` outputs.
- **Native Quick Entry + voice/screen drafts** — optionally register independent system-wide text and voice controls while Gokin is running. On macOS, new installs offer Claude-style `Double-tap Option` for Quick Entry and an opt-in `Caps Lock` voice toggle; `Option+Space` and custom modifier chords remain available. Caps Lock opens the compact surface and starts dictation, then stops on the next press so the transcript can be reviewed; it replaces normal Caps Lock only while that explicit setting is active. Windows uses customizable chords (`Ctrl+Alt+Space` and `Ctrl+Alt+D` defaults). Registration requests macOS Accessibility access when needed, rejects invalid or occupied shortcuts visibly, supports conflict-free swaps between text and voice controls, and atomically restores all prior registrations if any replacement or settings save fails. macOS hosts the existing Wails WebView in a dedicated floating `NSPanel`, preserving the main workspace window instead of resizing it and keeping one backend/event/approval context; unsupported runtimes retain the geometry-restoring compact-window fallback. On macOS 14+, dictation is native: AVAudioEngine streams microphone buffers directly into Apple Speech.framework, while React and the GLM/Kimi path receive transcript text only; explicit Settings onboarding reports Speech Recognition and Microphone permissions independently. Other runtimes retain WebView Speech Recognition as a fallback. Graceful stop waits for Apple’s final transcription, cancel/shutdown tears capture down immediately, and no voice path auto-sends. The compact surface exposes five durable recent-chat drafts and hands desktop/window capture to the permission-gated composer. Both global controls are off by default.
- **Native desktop shell + deep links** — macOS File/View/Help menus expose new chat, project-folder connection, workspace navigation, search, command palette, sidebar, side chat, settings, shortcuts, and update checks with conventional Command-key accelerators. Menu commands use a fixed allowlist and bounded readiness hand-off, restore the existing process, reuse the same React actions, and cannot navigate behind an approval modal. Installed builds also register `gokin://` so a browser, script, or another app can open a new chat, an existing project/chat, Files, or Artifacts. `q=` is bounded and becomes an editable draft only—links cannot send, run tools, or approve actions. Strict route/parameter validation, single-instance forwarding, cold-start queuing, duplicate suppression, and missing-project/session errors keep navigation deterministic. On macOS, closing the workspace window leaves Gokin running in the background for Quick Entry and local schedules; reopening, a deep link, or Quick Entry restores the existing process. A real Quit on every desktop host takes a backend-authoritative count of running chats, queued follow-ups, and side questions and asks before stopping them; Keep Running and dialog failures cancel Quit, duplicate Cmd+Q events cannot stack sheets, and the warning contains counts only. Confirmed Quit still performs full cleanup while preserving already-written transcripts and recovery data.
- **Interactive MCP Apps host** — tools can attach standard `ui://` HTML resources to results. Gokin negotiates stable `io.modelcontextprotocol/ui` (2026-01-26), hides app-only tools from GLM/Kimi, validates bounded HTML/result payloads and declared CSP origins, then renders an isolated iframe with the standard lifecycle. App-originated `tools/call` requests are restricted to the producing server and `app` visibility, schema/size/rate checked, secret-redacted in the operation preview, and require fresh user approval; links and model-context mutation remain unadvertised.
- **Project Skills for GLM/Kimi** — discovers standard `SKILL.md` bundles under `.gokin/skills/` and Claude-compatible `.claude/skills/`. Only validated name/description metadata enters per-turn context; GLM 5.2 or Kimi K3 reads the full manifest and referenced resources only when the request matches. Discovery is bounded, rejects symlink roots/manifests, reports invalid bundles in the Context panel, and keeps skill scripts behind the normal approval gate.
- **Global user instructions** — one local profile for language, tone, formatting, and review conventions across every GLM 5.2/Kimi K3 project. Project prompts remain the more specific override; runtime permission and computer-use rules are appended last. Input is UTF-8 validated, bounded to 64 KiB, and audit logs record only set/update/clear metadata—not the content.
- **Permission-gated computer use** — a per-project Screen toggle exposes screenshot plus reviewed click/type/key controls on macOS and Windows. Foreground identity comes from the OS, permission is scoped by app and turn, every input action shows its exact coordinates/text/chord, sensitive credential/wallet apps are blocked, and the target is revalidated immediately before execution. The composer camera is an explicit user action with full-desktop and native window/region modes: it leaves a reviewable Kimi K3 image attachment or a private GLM 5.2 Vision MCP path and never auto-sends. Disabling Screen remains an emergency stop that cancels the running agent and removes the tools.
- **Session Plan + Manual / Auto / Skip execution modes** — Plan is an ephemeral override for one chat: the model receives only a strict read-only declaration allowlist, every non-allowlisted call is independently denied without an approval path, and plugin hooks, MCP actions, specialist agents, computer use, shell/tests, memory mutation, and plan-lifecycle self-exit are unavailable. A visible composer banner requires the user to switch explicitly to implementation. Manual asks before ordinary mutations and can remember an explicit **Always allow** grant for one bounded local tool in the current project; the Context pane lists each scope and revokes it. Rules store no arguments or file content and are reclassified per call, so replace/delete variants remain exact-reviewed. Auto applies a deterministic allowlist to bounded project-local edits and reviews uncertain actions, while Skip bypasses ordinary prompts; these three remain the durable folder default. Shell, permanent deletion, computer/browser use, SSH, network access, connectors, scheduling, PR writes, and unknown tools can never receive a persistent project grant and remain exact-action gates where applicable.
- **Colored unified diff** for `git_diff` (green/red/hunk-header styling)
- **Syntax-highlighted `read` results** — language auto-detected from extension via rehype-highlight
- **Grouped grep output** — matches grouped by file, pattern highlighted, counts badge, file sections collapsible
- **Clickable project paths and changed-files lists** — ordinary text/source paths open the session Files editor; HTML/PDF/image/video paths open Browser Preview; Office paths open Artifacts; every surface keeps an explicit Add to chat action instead of auto-sending
- **Inline edit any user message** (Pencil) → trims server-side history + re-sends; retry (↺) trims + re-runs the same text
- **Up/Down arrow** in the chat input recalls previous user messages (terminal-style)
- **Drop files** onto the chat to attach as fenced code blocks; `@path/to/file.ext` tokens auto-expand from that chat's isolated checkout before sending (up to 10 files, 50 KB each)
- **Inline @path autocomplete** — type `@<chars>` for live file filter; ↑↓ Enter to insert
- **File picker modal** (`Ctrl+P` / `Cmd+P`) with fuzzy filter over the active chat checkout
- **Command palette** (`Ctrl+K`) — fuzzy project switcher + actions (new chat, clear chat, open files/artifacts/settings, model switcher, view memory, search across sessions)
- **Context gauge** in the chat header with tooltip showing real input/output token counts; warns at 75% of the model's window
- **Elapsed time** in the generating pill (`12s`, `1m 23s`, `1h 5m`) with live output-tokens counter
- **Changed-files summary** at the top of each assistant reply lists every `edit`/`write` from that turn as clickable chips
- **Draft input** per session — typing is preserved when you switch sessions AND across crashes (debounced disk save)
- **Search** within a session (`Ctrl+F`) or across all sessions in the project (`Ctrl+Shift+F`)
- **Agent activity timeline** (`Ctrl+Shift+A`) — modal listing every tool call in the session with chronological elapsed times
- **Normal / Verbose / Summary transcript views** — the composer selector and `Ctrl+O` switch between collapsed technical activity, every tool/reasoning row, or prompts plus final replies/changed-file summaries. The preference persists per project, old quiet-mode choices migrate safely, search always reveals matching hidden rows, and keyboard message navigation follows only what is actually visible.
- **Ephemeral side chat** — `Cmd/Ctrl+;`, `/btw`, the header button, or the macOS View menu opens a read-only drawer that sees the current session context without changing it. Side questions stream from an isolated client with an empty tool allowlist, can run while the main agent works, contribute to usage/cost totals, and disappear on close or session switch without entering the transcript or session export.
- **Custom snippets** — `/<name>` chat input macros saved globally; user-defined system prompt templates
- **Unread badge** + optional **toast** + optional **chime** on completion in background sessions; per-project mute. Toast countdowns pause on hover, keyboard focus, and window blur, resume with their remaining time, support focused `Esc` dismissal, and disappear immediately when notifications are disabled.
- **Pull request CI monitor** — for a validated HTTPS/SSH `origin`, Studio uses the authenticated GitHub CLI to show the PR for the active session branch, bounded check results, and background pass/fail notifications. The expanded bar also discovers the bounded branch graph around that PR: parent/ancestor layers, child/descendant layers, and open siblings, with every outbound URL reconstructed from the validated origin and PR number. Supplementary discovery failure never hides the current PR or CI state. The empty-chat welcome remains stationary while status loads; its CI bar opens explicitly from the header, then stays visible once work begins. Auto-fix is opt-in and capped at three unique failing fingerprints per PR, while squash auto-merge requires a separate confirmation and revalidates the exact head commit before enabling GitHub's server-side merge. A separate opt-in setting checks recent local sessions in rotating bounded batches while Studio is open and reversibly archives clean, idle chats when their PR becomes merged or closed; running, dirty, unavailable, unattended, and last-active chats are retained with an explicit reason.
- **Slash commands**: `/clear`, `/export`, `/exportall`, `/exportjson`, `/importsession`, `/system`, `/search`, `/sessions`, `/summarize`, `/diagnose`, `/help`, `/budget`, `/memory`, `+ user-defined`

### Tools
- **File & documents**: `read` (text, DOCX, XLSX, PPTX), `write`, `edit` (exact / regex / multi-edit / line-range / insert), `document_create` (DOCX/XLSX/PPTX/PDF), `delete`, `move`, `copy`, `mkdir`, `list_dir`, `tree`, `glob`, `grep`, `diff`
- **Git**: `git_status`, `git_diff`, `git_log`, `git_blame`, `git_branch`, `git_add`, `git_commit`, `git_pr`
- **Shell**: `bash` (with long-output collapse >30 lines), `kill_shell`, `task`, `task_output`, `task_stop`
- **Web**: `web_fetch`, `web_search`
- **Memory** (wired to persistent per-project stores): `memory` (remember/recall/forget/list), `memorize` (project-learning), `shared_memory` (cross-project scratchpad), `pin_context` (persistent system-prompt addition), `history_search`, `update_scratchpad`. The Project Memory viewer lets the user inspect, correct, or delete individual facts; edits are UTF-8/NUL/size validated, preserve entry identity and scope, rebuild automatic tags, and invalidate retrieval context immediately.
- **Planning**: `enter_plan_mode`, `update_plan_progress`, `get_plan_status`, `exit_plan_mode` — rendered as progress cards with step lists
- **Agent coordination**: `search_session_transcripts` (read-only literal search across other local chats, optionally scoped to one project or archived chats; available in Plan mode and strips reasoning/tool/document/binary payloads), `session_agent` (list/read/message/rename/archive other local Studio chats, plus user-clickable new-task suggestions; archive always asks first), `ask_agent` (dispatch to another project's agent), `coordinate` (structured multi-task plan with graceful fallback), `ask_user` (surface a question to the human in the UI)

Both supported coding providers receive the full tool suite plus memory, planning, MCP, and agent coordination.

### Reliability & Release Polish
- **Desktop update awareness** — on launch, Studio performs a notify-only GitHub Releases check no more than once per 24 hours (with a Settings opt-out), and About includes an explicit **Check now** action. Release responses are size-bounded, accept only stable canonical semver tags, and cannot supply an arbitrary browser URL. The app never downloads or installs unsigned artifacts in the background; the release workflow verifies the tag matches the embedded app version and publishes `SHA256SUMS.txt` beside every platform artifact.
- **First-run onboarding wizard** — guided 3-step setup for new installs (GLM/Kimi provider + model → API key with Test Connection → first project). The authenticated probe intersects the response with Studio's two-provider allowlist, keeps an explicitly selected eligible model, and otherwise selects the best model the account advertises. Unverified setup uses the bundled flagship defaults—GLM 5.2 or Kimi K3—and surfaces any account mismatch on the first connection check instead of silently changing provider families.
- **React ErrorBoundary** — render errors show a recovery UI (Reload / Try-recover / Show stack) instead of a white screen; auto-logged
- **Global error capture** — `window.onerror` + `unhandledrejection` route to the backend event log so async failures are inspectable
- **Application event log** — bounded ring buffer (500 entries) of recent backend events (config save failures, chat:retry, chat:error, frontend errors); deduplication of repeats within 2s; **persisted to disk** at `~/.config/gokin-studio/events.log` with size-capped rotation so events survive across restarts
- **Secret redaction in event log** — `sk-*` API keys, Bearer tokens, JWTs auto-redacted to `<redacted:KIND>` markers BEFORE storage and ALSO on disk-replay (defense-in-depth across persistence + iter 750+ backup archives + iter 890+ CSV exports)
- **Logs viewer** — Settings → Diagnostics → View logs: live ring buffer with level filter (all/info/warn/error) AND source filter (settings/project/agent/...); export to CSV (Excel-friendly, redaction preserved)
- **Diagnostics report** — Settings → Run diagnostics: one-shot health check covering config writability, API key presence (honours `GLM_API_KEY` / `KIMI_API_KEY` fallbacks), project directory existence, stale replay logs; copyable plaintext for support tickets
- **Provider connection test** — Test Connection (or Enter in the key field) checks the credential currently in the field without silently saving it, reports OK/latency/error plus allowlisted models, and warns when the selected new-project default is unavailable for that tested GLM/Kimi account. The verified capability snapshot stays in memory only and marks unavailable choices in the project and `Ctrl+M` model pickers; editing or reloading credentials invalidates it. Revealed keys automatically remask on navigation, window blur, save, or discard while memory-only drafts remain intact.
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
- **Smart welcome screen** — detects git state (branch, uncommitted files, recent commits) and language (Go/Python/Rust/Swift/Dart/Zig/C#/C++) for relevant first prompts. A bounded chat-and-directory-keyed metadata snapshot settles before the visible welcome surface mounts; fixed text/card geometry and a stable scrollbar gutter keep async language, Git, PR, and font updates from re-centering or nudging the heading and composer.
- **Project export/import** — full JSON envelope with project metadata + every session; bulk session export as Markdown or per-session as JSON
- **System / Dark / Light appearance** — preview and save from Settings; System follows live macOS/Windows appearance changes, including the integrated terminal and isolated MCP App surfaces
- **System Reduce Motion support** — modal entrances, status pulses, toasts, transitions, terminal cursor animation, message jumps, search results, and Settings navigation follow the operating-system accessibility preference

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

The regression suite covers the agent loop (tool execution, panic recovery, retry, context cancellation, chained tool rounds, parallel function calls), public Studio APIs, project/session durability, provider boundaries, workspace isolation, MCP/OAuth/Apps, plugins and hooks, artifacts/documents, computer use, schedules, backup/restore, and secret/path-traversal defenses. Run the commands above for the authoritative current count and result instead of relying on a stale hand-maintained total.

## Architecture

```
main.go                  Wails entry point, embeds frontend/dist
internal/studio/         Wails-bound application layer
  app.go                 Studio: project/terminal/settings management, all public methods are Wails bindings
  project.go             Project workspace, agent loop, tool execution, memory/plan wiring
  session.go             ChatSession: per-session history, cancelFn, usage stats, pinned flag, ephemeral Plan override
  session_permission_mode.go Session-scoped Plan selection and atomic idle-turn guard
  permission_policy.go   Runtime Plan/Manual/Auto/Skip decisions and plugin-hook isolation
  config.go              YAML config (~/.config/gokin-studio/config.yaml, 0600)
  history.go             Session history persistence (v1 bare / v2 versioned + parent + usage), atomic writes
  replay.go              Crash-recovery replay buffer (JSONL per session)
  messenger.go           StudioMessenger: routes ask_agent calls to other projects
  shared_memory.go       Process-wide cross-project scratchpad for shared_memory tool
  terminal.go            PTY sessions
  events.go              Event types (chat:delta / :text / :thinking / :tool_call / :tool_result / :complete / :error / :retry / :usage / :ask_user)
  mcp*.go                Local/remote MCP, OAuth/PKCE secure storage, MCP Apps, bundles, and GLM/Kimi tool registration
  mcp_bundle.go          Safe MCPB desktop-extension review, configuration, extraction, and stdio install
  mcp_app.go             MCP Apps negotiation, ui:// sandbox data, same-server app calls, approval and rate limits
  plugins.go             Claude-compatible plugin ZIP review/install/state, Skills catalog and slash commands
  plugin_agents.go       Reviewed plugin-agent parser and isolated GLM/Kimi child-session runner
  plugin_hooks.go        Digest-armed plugin hook review and bounded tool lifecycle dispatch
  artifact_preview.go    Session-worktree-aware bounded artifact discovery/reader for sandboxed previews
  artifact_versions.go   Private per-session bounded/deduplicated history with integrity checks and atomic worktree restore
  session_file_editor.go Bounded session-worktree text snapshots, content revisions, conflict detection, and atomic saves
  session_file_actions.go Session-resolved attach/open/reveal/copy metadata with a fixed installed-editor allowlist
  session_terminal.go     Session-resolved terminal cwd validation for root and folder-launched PTYs
  preview_server.go preview_static.go preview_session_storage.go  Reviewed dev/static preview lifecycle, diagnostics proxy, and opt-in isolated browser-session persistence
  project_skills.go      Bounded project-local SKILL.md discovery and progressive-disclosure catalog
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
    chat/MCPAppView.tsx  Sandboxed MCP App renderer and read-only postMessage lifecycle host
    terminal/Terminal.tsx xterm.js + fit addon
    layout/Sidebar.tsx   Project list with search, drag-pin, mute, right-click menu
    layout/StatusBar.tsx Active project, provider/model, thinking + budget indicators
    layout/ToastStack.tsx  Completion toasts + budget-threshold warnings + audio chime
    layout/ErrorBoundary.tsx  React error boundary + global window error handlers
    onboarding/OnboardingWizard.tsx  First-run 3-step setup
    files/FilePicker.tsx Ctrl+P quick-open modal
    files/FileBrowser.tsx File-tree browser and web/Office/PDF artifact split view
    files/ArtifactLibrary.tsx Searchable active-session artifact catalog with filters and preview routing
    files/ArtifactPreview.tsx Origin-isolated web preview plus passive native-document/PDF preview
    files/InlineArtifactCard.tsx Lazy sandboxed artifact preview embedded in assistant answers
    project/ProviderSelect.tsx Per-project provider/model + thinking toggle
    palette/CommandPalette.tsx Ctrl+K command/project palette
    settings/SettingsPage.tsx GLM/Kimi keys, defaults, plugins, MCP connectors, theme, diagnostics, logs viewer, snippets
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
- Or via env vars: `GLM_API_KEY`, `KIMI_API_KEY`

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
| `Ctrl/Cmd + 4` | Switch to Artifacts |
| `Ctrl/Cmd + [` / `Ctrl/Cmd + ]` | Go back / forward through workspace views |
| `Ctrl/Cmd + B` | Toggle sidebar |
| `Alt + 1..9` | Jump directly to session N in the current tab order |
| `Ctrl/Cmd + K` | Command palette (fuzzy switcher + actions) |
| `Ctrl/Cmd + Shift + M` | Open permission mode menu |
| `Ctrl/Cmd + Shift + I` | Open model switcher (`Ctrl/Cmd + M` remains supported) |
| `Ctrl/Cmd + Shift + E` | Open reasoning effort menu |
| `1..9` in an open composer menu | Choose the numbered item |
| `Ctrl/Cmd + P` | File picker (quick open) |
| `Ctrl/Cmd + F` | Search the current Chat or Artifacts view |
| `Ctrl/Cmd + Shift + F` | Search across all sessions in the project |
| `Ctrl/Cmd + Shift + A` | Agent activity timeline |
| `Ctrl/Cmd + Shift + D` | Toggle the Diff pane for the selected chat |
| `Double-tap Option` (macOS, opt-in) | Open compact Quick Entry from any app |
| `Caps Lock` (macOS, opt-in) | Open Quick Entry and start / stop voice dictation |
| `Ctrl/Cmd + Shift + B` | Toggle the Browser / Preview pane (`Ctrl/Cmd + Shift + P` remains supported) |
| `Ctrl/Cmd + Shift + S` | Select a Preview element and place bounded DOM evidence in the draft |
| `Ctrl/Cmd + Shift + G` | Focus sidebar project search |
| `Ctrl/Cmd + /`  or  `?` | In-app help modal |
| `Ctrl/Cmd + T` | New chat session |
| `Ctrl/Cmd + L` | Clear current chat |
| `Ctrl/Cmd + PgUp/Dn` | Cycle chat sessions |
| `Ctrl/Cmd + \`` | Toggle integrated terminal |
| `Enter` | Send message |
| `Shift/Ctrl + Enter` | Newline in message input |
| `Up arrow (empty input)` | Recall previous user message (Down to walk forward) |
| `j / k` | Navigate messages (vim-style) |
| `Shift + F10` / Menu key | Open actions for the focused message, project, or session tab |
| `Esc` | Close modal / stop agent / cancel onboarding |
| Right-click message | Copy / Quote / Pin / Branch / Re-run (also `Shift + F10`) |
| Right-click project | Reorder / Rename / Pin / Mute / Export / Archive / Delete (also `Shift + F10`) |
| Right-click session tab | Pin / Reorder / Rename / Delete (also `Shift + F10`) |
| Double-click session tab | Rename session |

## License

MIT

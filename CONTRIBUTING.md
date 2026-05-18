# Contributing to Gokin Studio

Thanks for considering a contribution. Quick notes:

## Quick start

```bash
git clone https://github.com/ginkida/gokin-studio
cd gokin-studio
wails dev          # hot-reload dev (Go + React)
```

Prerequisites: Go 1.25+, Node 18+, [Wails v2 CLI](https://wails.io/docs/gettingstarted/installation).

## Running tests

```bash
go test ./internal/studio/        # ~670 backend tests (~3.5s)
go test ./internal/engine/tools/  # ~230 tool tests (~1s)
cd frontend && npm run build      # type-check + production bundle
```

The Studio test suite carries security regression guards (path-traversal,
secret-leak, retention enforcement). PRs that change `internal/studio/`
should keep coverage at ≥ 90%.

## Style

- **Go**: standard Go style, `gofmt`. No framework beyond Wails. Mutexes for
  concurrency (RWMutex pattern). Public methods on `Studio` are Wails
  bindings — names are PascalCase.
- **Frontend**: functional React + hooks, Zustand for state. CSS lives in
  `frontend/src/App.css` (no CSS-in-JS). All hooks declared **before** the
  first early return (Rules of Hooks).
- **No comments unless they explain WHY**. Identifiers should be
  self-explanatory.

## What to include in a PR

1. **One feature or one bug fix per PR.** If you're touching backup AND
   adding a model, split it.
2. **A test that fails without the fix.** Regression guards are how we
   stay shipped.
3. **No secrets** in fixtures. Use `sk-LEGACY-` / `Bearer xxxx` style
   strings — never paste real keys.
4. **No machine-specific paths** (`/Users/yourname/...`). Use
   `t.TempDir()` + `GOKIN_CONFIG_DIR` env override.
5. **Wails bindings regenerated** — if you added a new public Studio
   method, run `~/go/bin/wails generate module` so the frontend can
   import it.

## Architecture entry points

- `internal/studio/app.go` — Wails-bound `Studio` struct + lifecycle.
- `internal/studio/project.go` — agent loop, tool execution, history
  compaction.
- `internal/engine/client/` — multi-provider LLM abstraction (Anthropic,
  GLM, MiniMax, Kimi, DeepSeek, Ollama).
- `frontend/src/App.tsx` — root layout, sidebar, tab bar, status bar.
- `frontend/src/components/chat/ChatPanel.tsx` — chat UI + tool cards.

See `README.md → Architecture` for the full file map.

## Security

See [SECURITY.md](./SECURITY.md). Don't add code paths that log API key
values, system prompt content, or paste user file paths into events that
get persisted (event log, audit log, backups).

## Release process

1. Bump `Version` constant in `internal/studio/diagnostics.go`.
2. Update test count + coverage in `README.md` status badge.
3. Commit, tag, push:
   ```bash
   git commit -am "Release v1.0.x"
   git tag v1.0.x
   git push origin main --tags
   ```
4. `.github/workflows/release.yml` triggers on the tag — builds
   macOS / Linux / Windows binaries in parallel, packages them
   (.zip / .tar.gz), and attaches to a GitHub Release with
   auto-generated release notes.
5. CI (`.github/workflows/ci.yml`) runs on every PR + push: `go test`
   (with race detector for `internal/studio/...`), `go vet`,
   `npm run build`.

The macOS build is **unsigned** — users will see Gatekeeper warnings
on first launch. If/when we have an Apple Developer cert, set
`APPLE_CERTIFICATE` + related secrets and add `wails build -nsis`
on Windows.

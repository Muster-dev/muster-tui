# muster-tui architecture

muster-tui is the interactive face of `muster`. The bash CLI stays the engine
(hooks, locks, credentials, history, skills) and the headless interface
(`muster <cmd> --json`, `--minimal`). muster-tui never deploys anything itself:
it runs the CLI, Docker and SSH as child processes and renders what they say.

```
main.go ──► tui.New(Deps) ──► tea.Program (alt screen, mouse)
               │
               ├── engine   muster CLI wrapper (JSON + NDJSON streams, raw streams)
               ├── docker   docker CLI wrapper (--format '{{json .}}')
               ├── fleet    fleet machines/groups (CLI JSON, files fallback), ssh test
               ├── config   muster.json load/save (order-preserving), validation,
               │            hook manifest check, project scanner for onboarding
               ├── prompt   command registry, parser, fuzzy completer, history
               ├── theme    colours (AdaptiveColor, mustard #E8B931), glyphs, NO_COLOR
               └── auth     MUSTER_TOKEN handshake with the CLI
```

## Packages

| Package | Responsibility | Talks to |
|---|---|---|
| `internal/engine` | `Engine` runs `muster` with `MUSTER_TOKEN`, `NO_COLOR=1`, `MUSTER_NO_VERIFY` passthrough and the project dir as cwd. Typed results for `status`, `history`, `doctor`, `hooks`, `fleet list/status`; `Deploy` returns a `Stream[DeployEvent]`; `Raw` returns a `Stream[string]` for commands without JSON (logs, rollback, setup, hooks approve). Every call takes a `context.Context`; cancelling kills the whole process group. Unknown JSON fields are kept in `Event.Extra`. | `muster` binary |
| `internal/docker` | `Client` shells out to `docker … --format '{{json .}}'` for containers, stats, images, volumes, `system df`, and runs actions (restart/stop/start/rm/pull/prune) and `logs -f` streams. `ErrDaemonDown` is detected from stderr so screens can explain it. | `docker` binary |
| `internal/fleet` | Machines and groups from `muster fleet list --json` (muster 0.7 arrays of objects, or the older objects keyed by name) with a fallback to `remotes.json`; `Test` runs `ssh -o BatchMode=yes` with a short timeout; cloud login state from `~/.muster/settings.json` + `~/.muster/tokens/cloud.json`. | `muster`, `ssh`, files |
| `internal/config` | `Project` = path + an order-preserving JSON document (`jsonx.Object`) + typed accessors. Editing goes through the document so unknown keys and key order survive a save. `Validate` checks health/creds/order/remote/k8s. `HookChanges` compares hook files with `.muster/hooks.manifest`. `Scan` is a fast Go preview scanner (compose, Dockerfile, k8s, package.json/go.mod/…) used by onboarding. Global settings live in `~/.muster/settings.json`. | files |
| `internal/prompt` | `Registry` of commands (name, args kind, usage, summary, keybinding). `Parse` tokenises input (slash form, multi-word commands, flags, filler words) and returns a `Parsed` or an `Error` with a "did you mean" suggestion. `Complete` returns fuzzy-ranked candidates for the word under the cursor using a `Context` (services, machines, groups, containers). `History` persists to `~/.muster/tui_history` and supports reverse search. | — |
| `internal/theme` | All colours are `lipgloss.AdaptiveColor` so light and dark terminals work; one accent (mustard `#E8B931`, darker variant on light backgrounds). `NO_COLOR` switches lipgloss to the ASCII profile. Glyphs differ by state, so meaning never depends on colour alone. `theme.Static` disables animation for tests. | — |
| `internal/tui` | Root model, layout, task runner, screens. | everything above |

## The root model (`internal/tui/app.go`)

```
 header      muster · project · branch · summary · background spinner · relay
 ─────────
 rail │ main pane (active Screen)                 ← body
 ─────────
 suggestion bar (state + next useful action)
 › prompt                                         / commands · ? help
```

* **Layout** (`layout.go`): chrome is 5 rows. Rail width is `clamp(w/4, 22, 32)`; it is hidden in full-screen mode (Ctrl+O) and below 72 columns. Below 60×16 a "terminal too small" view is shown instead. Overlays (palette, help, confirm) render inside the main pane region; the completion popup replaces the bottom rows of the body. All width math uses ANSI-aware `lipgloss.Width` / `ansi.Truncate` helpers in `textutil.go`.
* **Focus**: `prompt` (default), `rail`, `main`. Tab with an empty prompt moves focus to the rail, Tab again to main. Commands that open an interactive screen move focus to main. In main/rail focus, keys the screen does not handle and that are printable fall through to the prompt (so you can always just start typing). Esc priority: overlay → screen (search box, form) → cancel the foreground task → focus back to prompt → clear the prompt → previous screen.
* **Ctrl+C**: clears the prompt if it has text; otherwise cancels the foreground task; otherwise arms quit ("Press Ctrl+C again to quit", 2 s window).
* **Shared state** (`state.go`): project config, per-service health (with "unhealthy since"), last deploy per service (from history), docker summary, fleet machines, cloud login, doctor warnings, hook changes. Only the root `Update` mutates it; screens get a read-only pointer for rendering.
* **Suggestion bar** (`suggest.go`): the active screen's hint wins when it has one (e.g. "api is building — Ctrl+O full log · Esc cancel"); otherwise a priority list derived from state (no token → how to pair; hooks changed → `approve`; unhealthy service → `logs api` · `rollback api`; Docker down; nothing deployed yet → `deploy --dry-run`; all good → `deploy`).

## Screens (`internal/tui/screen_*.go`)

```go
type Screen interface {
    Key() string                       // "dashboard", "deploy", "logs:api", …
    Title() string
    Init() tea.Cmd
    Update(tea.Msg) (Screen, tea.Cmd)  // keys only when active; other msgs broadcast
    View(w, h int) string
    Hint() string                      // suggestion bar override ("" = none)
    Keys() []KeyHelp                   // for the help overlay
}
```
Optional interfaces: `Capturer` (screen is in a text-entry mode: all keys go to it), `Closer` (cancel tasks when evicted).

Live screens are cached by key, so a deploy keeps running and updating while you look at logs or Docker. Non-key messages are broadcast to every live screen; each screen ignores task IDs it does not own.

| Screen | File | Data source |
|---|---|---|
| Onboarding | `screen_onboarding.go` | `config.Scan` preview → `muster setup --stack … --services … --order … --health …` (raw stream) |
| Dashboard | `screen_dashboard.go` | shared state (`status --json`, `history --json`, `doctor --json`, docker summary) |
| Deploy | `screen_deploy.go` | muster ≥ 0.7: one `deploy --json --stop-on-failure [--from svc]` stream, events routed by service; older: `deploy --json <svc>` per service. Stops on failure with Retry / Rollback / Skip / Abort, Likely cause / Try (`hint` event or `explain --json`), Fix (`explain --fix --yes` after a confirm), Explain with AI; dry run uses one `deploy --json --dry-run` call |
| Hook approval | `screen_approval.go` | muster ≥ 0.7: `hooks status --json`, Approve (after a confirm) runs `hooks approve --yes --json`; older: `config.HookChanges` + interactive `muster hooks approve` |
| Explain | `screen_explain.go` | `explain [svc] --json`; Fix runs `explain --fix --yes` after a confirm |
| Assistant | `screen_ask.go` | `ask --json` NDJSON events; approvals / follow-ups written to its stdin |
| Service detail | `screen_service.go` | config + state + `history --json <svc>` |
| Logs | `screen_logs.go` | `muster logs <svc> --json --tail 200 --follow` (≥ 0.7), `muster logs <svc>` or `docker logs -f` raw stream; ring buffer, follow, search, level filter, wrap, jump to error, copy |
| Docker | `screen_docker.go` | `docker ps/stats/images/volume ls/system df`; actions; exec shell via `tea.ExecProcess` |
| Servers | `screen_servers.go` | `fleet list --json` (0.7 array shape or the older keyed shape), `fleet status --json` (U), ssh test, `fleet deploy/status/rollback` raw streams, ssh via `tea.ExecProcess`, add-machine form → `muster fleet add` |
| History | `screen_history.go` | `history --json --all` with service/action/status filters |
| Doctor | `screen_doctor.go` | `doctor --json` / `doctor --fix --json` |
| Settings | `screen_settings.go` | `~/.muster/settings.json` + `muster.json` via `config.Project`, validation, reorder, `$EDITOR` escape hatch |
| Help / palette | `overlay.go` | registry + active screen keys |

## Async task runner (`internal/tui/tasks.go`)

Every engine/docker/ssh call is a task:

```go
id, cmd := tasks.Run(label, owner, fg, func(ctx) tea.Msg)          // one-shot
id, cmd := tasks.Stream(label, owner, fg, func(ctx) (<-chan tea.Msg, error))
```

`Run`/`Stream` register the task synchronously (inside `Update`), with a
`context.CancelFunc`, then return a `tea.Cmd` that does the work off the UI
goroutine. Results come back wrapped in `taskMsg{ID, Msg}`; streams are drained
one item per `tea.Cmd` (`listen`) so the UI never blocks. The root unwraps the
inner message, updates shared state, broadcasts it, and removes the task when it
ends. Foreground tasks are what Esc/Ctrl+C cancel; background tasks (status
refresh every 20 s, docker summary, fleet) show as a spinner in the header.
Cancelling kills the child's process group (`Setpgid` + `cmd.Cancel`), so
`docker compose` grandchildren die too.

## Engine contract and fallbacks

* **Version gating:** `muster --version` is read at startup into
  `State.Caps` (`engine.CapsFor`). `Caps.EngineAPI` (muster ≥ 0.7.0) turns on
  the engine-API code paths; older CLIs keep the earlier ones and commands
  that need 0.7 say how to update (`Model.needV07`).

* JSON commands need `MUSTER_TOKEN` (the CLI's `_json_auth_gate`). The token is
  resolved from `MUSTER_TOKEN`, `~/.muster/tui/config.json` (what `muster`
  writes when it launches the TUI) or the legacy `~/.muster-tui/config.json`.
  If none exists and the machine has no tokens yet, muster-tui bootstraps one
  with `muster auth create muster-tui --scope admin` (allowed without auth only
  for the first token). Otherwise it runs in a limited mode that reads
  `muster.json` directly and tells you how to pair. See README "Auth".
* Where the CLI has no JSON (setup scan, logs, rollback, k8s diagnostics, last
  deploy image, cloud agents), muster-tui reads files or parses raw output and
  the gap is recorded in `docs/ENGINE-GAPS.md`.
* Exit codes: 0 ok, non-zero failure. Today `deploy --json` exits 0 even on a
  failed service, so failure is derived from `done` events (and the optional
  `summary` event when present).

## Testing

* `internal/engine`: a fake `muster` shell script (`testdata/fake-muster`) that
  replays realistic NDJSON fixtures selected by arguments (success, failure,
  timeout, summary event, unknown fields, auth error).
* `internal/docker`: fake `docker` script with JSON fixtures, daemon-down case.
* `internal/prompt`: table tests for parse, did-you-mean, completion ranking,
  history.
* `internal/tui`: teatest golden snapshots (`testdata/*.golden`) for dashboard,
  deploy in progress, deploy failed + recovery, docker and onboarding at 80×24
  and 140×40, rendered with the ASCII profile, a fixed clock and static
  spinners; a resize-to-tiny test that walks sizes from 1×1 up.
  Update goldens with `go test ./internal/tui -update`.

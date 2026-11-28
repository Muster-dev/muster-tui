# muster-tui architecture

muster-tui is the interactive face of `muster`. The bash CLI stays the engine
(hooks, locks, credentials, history, skills) and the headless interface
(`muster <cmd> --json`, `--minimal`). muster-tui never deploys anything itself:
it runs the CLI, Docker and SSH as child processes and renders what they say.

```
main.go ──► tui.New(Deps) ──► tea.Program (alt screen, mouse unless --no-mouse)
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
 header      muster  ›  Servers › byricky                        screen status · background tasks
 body        the active Screen: a centred column (≤ 112) unless it is Wide
 status bar  project  ⎇ branch  ● Fleet Cloud              hints for this screen (≤ 5)    ? help  ctrl+k commands
```

* **Header = where you are.** The brand and breadcrumb on the left; a
  screen's title is the breadcrumb and screens never repeat it in their
  body. Screens that implement `Statuser` put a short status on the right
  ("● following", "✕ api failed · 12s"); otherwise the project's health
  summary goes there. The task spinner leaves out tasks the active screen
  owns (it shows those itself).
* **Status bar = context and keys.** Project, branch and Fleet Cloud on the
  left, the screen's hints and `? help  ctrl+k commands` on the right. When
  both don't fit (narrow terminals) the context moves up to the header's
  right side. "Press ctrl+c again to quit" takes the left side. An open
  popup shows only its own keys.
* **Layout and surfaces** (`surface.go`, after opencode's TUI): content is a
  centred column at most `contentMax` (112) wide; screens implementing
  `Wide` (deploy, docker, history, log streams, and the servers list when
  split) get the full width. Panels are filled surfaces, not boxes:
  `theme.Surface` (one step off the terminal background) and
  `theme.Element` (selected row bands, inputs). Both are derived from the
  terminal's real background (OSC 11, already queried by lipgloss) unless
  `MUSTER_THEME`/`COLORFGBG` pin the theme. `paint` re-applies the
  background after every reset so styled spans keep it; in the ASCII
  profile (NO_COLOR, tests) surfaces are just padding.
* **Result cards** (`card` in `surface.go`): fleet test/status, doctor,
  explain, a finished deploy and short rollback/restart output render as a
  card in the top third, as wide as its content (≤ 92): icon + headline
  ("All servers passed", "1 of 2 failed"), a dim subline, aligned rows, and
  clickable next actions (`Env.hints`, `key:` zones). Long bodies scroll.
* **Master-detail**: from `splitMin` (110) columns the servers list is a
  raised sidebar (≈ 30%, 40–50 cells) and the selected server's detail
  (`serverScreen` embedded) is the right pane, updated live; enter/tab
  focuses its actions, esc/left returns to the list. Narrower terminals keep
  the single list.
* **Home when empty**: a centred launcher (wordmark, tagline, four keys).
* **Footers match the screen.** Screens implement `Footerer` and list at
  most ~5 keys for their current state; nothing draws a key row of its own
  above the footer. A key written `enter/esc` replays its first key when
  clicked. Result views use `enter/esc back · r run again · y copy`; only the
  logs viewer shows search/level/wrap keys.
* **CLI text.** Where the CLI has no JSON, output shown in the TUI goes
  through `cleanOutput` (no colour codes or box drawing, "server" for
  "machine", no runs of blank lines).

* **Layout** (`layout.go`): chrome is 2 rows; the screen gets `w-2 × h-2`
  (or the centred column). Below 60×16 a "terminal too small" view is shown
  instead. Overlays (palette, help, confirm) implement `Size(bw, bh)` and
  are drawn as a filled panel a quarter of the way down (title row + `esc`,
  3 cells of padding, no border) over the dimmed screen; toasts float
  top-right in a small panel with a coloured bar; the root remembers the box (`ovRect`)
  so a click outside closes it. All width math uses ANSI-aware
  `lipgloss.Width` / `ansi.Truncate` helpers in `textutil.go`.
* **Keys** (`keys.go`): ctrl+c closes a popup, then cancels the foreground
  task, then arms quit (2 s window). An open overlay gets every key.
  `ctrl+k` always opens the palette; otherwise `Capturer` screens (forms,
  search boxes) get every key, then the active screen; keys it doesn't handle
  fall back to `/` `:` (palette), `?` (help), `esc` (cancel foreground task →
  leave full screen → back) and `q` (back, or quit on home).
* **Palette** (`overlay.go`): the one place to type. Items are built from
  state: recent commands, actions (project ones only in a project), servers
  ("Go to byricky"), services, this screen's keys, and — once you type —
  server × action and service × action combos and every registry command.
  Fuzzy-ranked with `prompt.FuzzyScore`. Typed text that parses becomes a
  "Run" row and its last word is completed with `prompt.Complete`; Enter on
  something that doesn't parse shows the parser's "did you mean" (Tab
  applies it). Rows run a command line through `runLineMsg` (so routing stays
  in `commands.go`), a `tea.Cmd` (confirmed server actions) or replay a
  screen key.
* **Mouse** (`zones.go`, `keys.go`): screens wrap clickable text with
  `env.mark(id, text)` (bubblezone markers, zero-width to lipgloss); the root
  `View` scans the frame and the mouse handler hit-tests the smallest region.
  `key:<k>` regions (footer hints) replay the key; `hdr:home` goes home;
  `ov:*` regions go to the overlay (`overlayClicker`); anything else goes to
  the active screen's `Click(id, double)` (`Clicker`). A second click on the
  same region within 400 ms is a double click. The wheel moves the overlay
  selection (`overlayWheel`) or is sent to the screen as ↑/↓.
* **Shared state** (`state.go`): project config, per-service health (with
  "unhealthy since"), last deploy per service (from history), docker summary,
  fleet servers and their last `fleet status --json` (`FleetHealth`), cloud
  login, doctor warnings, hook changes. Only the root `Update` mutates it;
  screens get a read-only pointer for rendering.
* **Server health**: `loadFleetHealth` runs `muster fleet status --json`
  (cloud servers through the relay, SSH servers over SSH) as the background
  task "checking servers": once when the fleet list first loads, every 30 s
  while a servers view is on screen, and on `r`. `Env.serverStatus` turns it
  into Online / Offline / Checking… / Needs pairing.

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
| Servers home / Servers | `screen_servers.go` | fleet list + `FleetHealth`; actions route to `fleet deploy/status/test/rollback` (deploy and rollback confirm first), add-server form → `muster fleet add` |
| Server | `screen_server.go` | one server: connection, mode, muster version, services from `fleet status --json`, actions; Logs (`fleet logs` → logs viewer) and Restart (pick a service, confirm, `fleet restart --yes --json`) when the CLI has them, disabled with a reason otherwise; no shell for cloud servers; pair screen → `muster fleet pair <name> --token` |
| Fleet result | `screen_fleet.go` | `fleet test` (`--json`, or the text output parsed by `fleet.ParseTestText`) and `fleet status --json` as a card: dot, name, Fleet Cloud/SSH, what happened, summary line; r runs again, y copies |
| Onboarding | `screen_onboarding.go` | `config.Scan` preview → `muster setup --stack … --services … --order … --health …` (raw stream) |
| Dashboard | `screen_dashboard.go` | shared state (`status --json`, `history --json`, `doctor --json`, docker summary, fleet health); with no project it is the servers home (or a welcome) |
| Deploy | `screen_deploy.go` | muster ≥ 0.7: one `deploy --json --stop-on-failure [--from svc]` stream, events routed by service; older: `deploy --json <svc>` per service. Stops on failure with Retry / Rollback / Skip / Abort, Likely cause / Try (`hint` event or `explain --json`), Fix (`explain --fix --yes` after a confirm), Explain with AI; dry run uses one `deploy --json --dry-run` call |
| Hook approval | `screen_approval.go` | muster ≥ 0.7: `hooks status --json`, Approve (after a confirm) runs `hooks approve --yes --json`; older: `config.HookChanges` + interactive `muster hooks approve` |
| Explain | `screen_explain.go` | `explain [svc] --json`; Fix runs `explain --fix --yes` after a confirm |
| Assistant | `screen_ask.go` | `ask --json` NDJSON events; approvals / follow-ups written to its stdin |
| Service detail | `screen_service.go` | config + state + `history --json <svc>` |
| Logs | `screen_logs.go` | `muster logs <svc> --json --tail 200 --follow` (≥ 0.7), `muster logs <svc>` or `docker logs -f` raw stream; ring buffer, follow, search, level filter, wrap, jump to error, copy |
| Docker | `screen_docker.go` | `docker ps/stats/images/volume ls/system df`; actions; exec shell via `tea.ExecProcess` |
| History | `screen_history.go` | `history --json --all` with service/action/status filters |
| Doctor | `screen_doctor.go` | `doctor --json` / `doctor --fix --json` |
| Settings | `screen_settings.go` | `~/.muster/settings.json` + `muster.json` via `config.Project`, validation, reorder, `$EDITOR` escape hatch |
| Help / palette / confirm | `overlay.go` | registry, state, active screen keys |

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
  `State.Caps` (`engine.CapsFor`); `muster fleet --help` is read too
  (`Caps.WithFleetHelp`) because fleet commands land between releases:
  `FleetTestJSON`, `FleetLogs`, `FleetRestart`. `Caps.EngineAPI` (muster ≥ 0.7.0) turns on
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
  servers home (master-detail when wide), welcome, palette, fleet test cards,
  server detail, deploy in progress / failed / done, explain, doctor, docker,
  settings and onboarding at 80×24, 120×40 and 200×55, rendered with the ASCII profile, a fixed clock and static
  spinners; a resize-to-tiny test that walks sizes from 1×1 up.
  Update goldens with `go test ./internal/tui -update`.

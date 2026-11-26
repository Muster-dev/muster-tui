# muster-tui v1 — product spec

Goal: the best deploy/ops terminal experience available. It should feel like Claude Code or Codex CLI (a calm full-screen app with a prompt at the bottom that understands what you mean) and help people run services, servers and Docker without memorising commands. The bash `muster` CLI stays the engine and the headless interface (`muster <cmd>`, `--json`, `--minimal`); muster-tui is what people see when they type `muster` in a terminal.

## Principles
- Never block the UI. Every engine/docker/ssh call is an async tea.Cmd with a visible spinner and is cancellable (Esc / Ctrl+C on the running task, double Ctrl+C to quit).
- Always show the next useful action. A one-line "suggestion bar" above the prompt explains state and offers the fix ("api is unhealthy for 3m · `logs api` · `rollback api`").
- Plain words, sentence case, no jargon walls. Errors say what happened and what to do.
- Keyboard first, mouse works. Works at 80×24, scales to big terminals. Light and dark terminals (lipgloss AdaptiveColor), NO_COLOR respected.
- Brand: muster mustard #E8B931 as the single accent; neutrals otherwise. Sparse, confident layout; no box-drawing clutter around every widget.

## Layout
```
 muster  acme-store  main            5 healthy · 1 deploying          relay ●
─────────────────────────────────────────────────────────────────────────────────
 Services                  │ Deploy · api                                  12s
 ● api      k8s  3/3   ↻   │ ✓ git pull origin/main
 ● web      http :3000     │ ⠹ build image  acme/api:4f2a9c1
 ○ redis    tcp  :6379     │   │ #9 [build 5/6] RUN npm run build
                           │ · health check
 Servers                   │ · notify discord
 ● prod-1   ssh   muster   │
 ● edge-1   cloud agent    │
                           │
 Docker                    │
 7 containers · 2.1 GB     │
─────────────────────────────────────────────────────────────────────────────────
 api is building — Ctrl+O full log · Esc cancel
 › deploy web█                                         / commands · ? help
```
- Left rail: Services, Servers (fleet + cloud agents), Docker summary. Tab/arrow to focus; Enter opens detail in the main pane.
- Main pane: the current screen (deploy progress, logs, service detail, docker, server detail, setup, settings, history, doctor).
- Bottom: suggestion bar + prompt.

## The prompt
- Type commands as words: `deploy`, `deploy api web`, `logs api`, `rollback api`, `restart web`, `status`, `doctor`, `history`, `setup`, `settings`, `docker`, `images`, `prune`, `servers`, `ssh prod-1`, `fleet deploy web`, `cloud login`, `help`, `quit`. Slash forms (`/deploy`) also work.
- Fuzzy autocomplete popup for commands, service names, machine names, container names; Tab completes; ↑/↓ history (persisted in ~/.muster/tui_history); Ctrl+R history search.
- Anything unrecognised: suggest the closest command ("Did you mean `logs api`?").
- Ctrl+K command palette listing every action with its keybinding.

## Screens
1. Welcome / onboarding — when the directory has no muster.json: explain in two lines, Enter runs a scan, show the detected services with inferred health checks and deploy order on one screen, "Create" / "Customize" / "Cancel". Customize edits per-service settings in a form. Ends on "Deploy now? (dry run first)".
2. Dashboard — the default: rail + main pane showing an overview (health per service with last deploy time and version/image, recent activity, warnings from doctor/build-context checks).
3. Deploy — per-service step list with spinners and timings, streamed output (collapsible per service, Ctrl+O full-screen log), health result, final summary. On failure: inline recovery choices Retry / Rollback / Skip / Abort plus the diagnosis (the CLI's k8s diagnostics / last log lines). Dry run mode shows the plan.
4. Hook approval — when hooks changed since approval or the project is new on this machine: list files, show a colored diff, Approve / Abort.
5. Service detail — config summary (stack, health, creds mode, git pull, remote, k8s names), live health, recent deploys, actions (deploy, logs, rollback, restart, open hook in $EDITOR, edit settings).
6. Logs — follow mode, `/` search with highlights, level filter, wrap toggle, jump to error, copy.
7. Docker — containers (name, image, status, ports, CPU/mem from `docker stats --no-stream`), images (repo:tag, size, age, dangling), volumes, `docker system df`. Actions: logs, restart, stop/start, exec shell (suspend TUI via tea.ExecProcess), pull, remove, prune (dangling images / stopped containers) with a confirmation showing reclaimable size. Works even outside a muster project.
8. Servers — fleet machines and groups (transport, mode, last status), cloud agents when logged in; actions: test connection, deploy, status, rollback, ssh (suspend TUI), add machine (form).
9. History — timeline of deploys/rollbacks with filters.
10. Doctor — checks with PASS/WARN/FAIL, one-key "fix what you can".
11. Settings — global settings and per-project muster.json editing with validation (health type/port/endpoint, credentials mode, git pull, deploy order with reordering, remote, k8s). "Open in $EDITOR" escape hatch. Advanced users can do everything the JSON can.
12. Help overlay (`?`) — keybindings per screen + top commands.

## Engine contract
- Prefer `muster <cmd> --json` (status, deploy, rollback, history, doctor, fleet list/status, setup scan). Where the CLI lacks JSON today, (a) read muster.json / ~/.muster files directly as a fallback and (b) list the missing JSON outputs in docs/ENGINE-GAPS.md so the CLI can add them. Do not edit the bash CLI repo from this task.
- Docker and SSH are called directly from Go.
- Exit codes from the CLI: 0 ok, non-zero failure.

## Quality bar
- `go vet`, `go test ./...` green. Tests with a fake `muster` binary for the engine, and teatest/golden snapshots for the main screens at 80×24 and 140×40.
- No panics on resize to tiny sizes; graceful "terminal too small" view below 60×16.
- Startup < 150 ms to first frame; status loads asynchronously.
- Module path `github.com/Muster-dev/muster-tui`; release workflow builds darwin/linux amd64/arm64.

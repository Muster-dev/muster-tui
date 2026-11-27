# muster-tui

The interactive terminal UI for [muster](https://github.com/Muster-dev/muster).
Type `muster` in a project and this is what you get: a calm full-screen app
with your services, servers and Docker on the left, the current task in the
middle, and a prompt at the bottom that understands what you mean.

```
 muster  acme-store  main            2 healthy · 1 unhealthy            ⠹ deploying api
────────────────────────────────────────────────────────────────────────────────────────
 Services                  │ Deploy · api                                          12s
 ● redis      tcp :6379    │  ✓ redis                                              3.1s
 ⠹ api        deploying    │  ⠹ api
 ✕ web        http :3000   │    ✓ git pull origin/main                             0.8s
 ○ worker     no check     │    ⠹ run deploy.sh                                    8.2s
                           │      │ #9 [build 5/6] RUN npm run build
 Servers                   │  · web
 ○ prod-1     ssh muster   │
                           │
 Docker                    │
 5/7 running · 2.1GB       │
────────────────────────────────────────────────────────────────────────────────────────
 ⠹ api — deploy.sh · Ctrl+O full log · Esc cancel
 › logs web                                                     Tab complete · Enter run
```

The bash `muster` CLI stays the engine and the headless interface. muster-tui
never deploys anything itself: it runs `muster <cmd> --json`, `docker` and
`ssh` as child processes and shows what they report.

## Install

```bash
go install github.com/Muster-dev/muster-tui@latest
```

or download a binary from
[Releases](https://github.com/Muster-dev/muster-tui/releases) (macOS and Linux,
amd64 and arm64), or build from source:

```bash
git clone https://github.com/Muster-dev/muster-tui.git
cd muster-tui
go build -o muster-tui .
```

Requires [muster](https://github.com/Muster-dev/muster) on your `PATH` (and
Docker for the Docker screen). Go 1.25+ to build.

muster 0.7.0 or newer is recommended. With an older CLI, muster-tui still
works, but `explain`, `ask`, `restart` via the CLI, in-app hook approval and
the single-call deploy are hidden. It says so at startup; run `muster update`.

## Use

```bash
muster            # launches muster-tui when it's installed (tui_mode = go)
muster-tui        # or run it directly
muster-tui -C ~/src/acme-store
```

Switch back to the classic bash menus with `muster settings --global tui_mode bash`
(or in Settings → Global → Interactive UI).

### The prompt

Type commands as words; `/deploy` works too. Tab completes commands,
services, machines and containers; anything unrecognised gets a
"did you mean" you can accept with Tab.

| Command | What it does |
|---|---|
| `deploy` | Deploy everything (asks first, offers a dry run) |
| `deploy api web` | Deploy those services, in order |
| `dry-run` / `plan` | Show the plan, change nothing |
| `logs api` | Follow logs |
| `rollback api` | Roll back (asks first) |
| `restart web` | Restart via the stack (restart hook, k8s, compose, docker) or redeploy |
| `explain [api]` | Why the last deploy failed: Likely cause / Try, and a one-key fix |
| `ask [question]` | Muster Assistant (Fleet Cloud): explains failures, proposes actions you approve |
| `status` | Check health now |
| `open api` | Service detail |
| `history [api]` | Deploy timeline |
| `doctor [--fix]` | Diagnose and fix |
| `docker`, `images`, `volumes`, `prune` | Docker — works outside a project |
| `exec <container>` | Shell inside a container |
| `servers`, `ssh prod-1` | Fleet machines |
| `fleet deploy\|status\|test\|rollback [target]` | Fleet operations |
| `fleet add prod-1 deploy@10.0.0.5` | Add a machine |
| `cloud login\|logout\|status` | Fleet Cloud |
| `setup` | Scan this folder and create muster.json |
| `settings [api]` | Edit global and project settings |
| `approve` | Review and approve changed hooks |
| `edit api [hook]` | Open a hook in `$EDITOR` |
| `cleanup` | Clean stuck processes and old logs |
| `retry`, `skip`, `abort` | Answer a failed deploy |
| `help`, `quit` | |

Filler words are fine (`logs for api`), so are unique prefixes (`dep api`).

### Keys

| Key | Everywhere |
|---|---|
| `/` | Start a command (popup lists them) |
| `Tab` | Complete · accept "did you mean" · move focus prompt → services → main |
| `↑` `↓` | Prompt history (persisted in `~/.muster/tui_history`) |
| `Ctrl+R` | Search history |
| `Ctrl+K` | Command palette: every action with its key |
| `?` | Help for the current screen |
| `Esc` | Close · cancel the running task · back |
| `Ctrl+O` | Full-screen main pane (full deploy log) |
| `Ctrl+C` | Clear the prompt · cancel the task · twice to quit |

Screen keys (also in `?`):

* **Deploy failure** — `r` retry · `b` rollback · `s` skip · `a` abort · `f` fix · `e` explain with AI, `←` `→` + Enter
* **Logs** — `/` search, `n`/`N`, `e`/`E` jump to errors, `L` level filter, `w` wrap, `y` copy, `End` follow
* **Docker** — `1`-`4` tabs, `l` logs, `r` restart, `s` stop/start, `x` shell, `d` remove, `p` pull, `P` prune
* **Servers** — `t` test, `U` fleet health, `Enter` ssh, `d` deploy, `u` status, `b` rollback, `a` add
* **Explain** — `f` apply the fix (asks first), `a` explain with AI, `l` logs, `d` deploy
* **Assistant** — `y`/`n` run or decline a proposed action, Enter sends a follow-up (empty finishes)
* **Service** — `d` deploy, `p` dry run, `l` logs, `b` rollback, `r` restart, `e` edit hook, `s` settings
* **Settings** — `[` `]` sections, Enter/Space edit, `←` `→` cycle, `J`/`K` reorder, `Ctrl+S` save, `e` `$EDITOR`
* **History** — `s` service, `f` result, `a` action filters · **Doctor** — `f` fix what you can

The mouse works for scrolling and for picking services in the left rail.

### Screens

* **Onboarding** — in a folder without `muster.json`: scan, review detected
  services with health checks and deploy order, customise, create, then
  "Deploy now? (dry run first)".
* **Dashboard** — health per service, last deploy, recent activity, things
  that need attention (doctor, Docker, changed hooks).
* **Deploy** — step list with timings and streamed output; on failure the
  likely cause and what to try, and Retry / Rollback / Skip / Abort, plus
  Fix when muster has a safe one and Explain with AI when you're signed in
  to Fleet Cloud.
* **Hook approval** — when hooks changed since you approved them: the files,
  risky commands, and a coloured diff against the approved copy; Approve
  asks first, then runs `muster hooks approve --yes`.
* **Explain** — muster's offline explanation of the last failure.
* **Assistant** — the Muster Assistant (`muster ask`): streamed answers, the
  tools it reads, and actions that only run after you say yes.
* **Service detail**, **Logs**, **Docker**, **Servers**, **History**,
  **Doctor**, **Settings** (global settings and every `muster.json` field,
  validated before saving; unknown keys and key order are kept).

## Headless and scripting

Use the CLI directly — it is the stable interface:

```bash
muster deploy --json            # NDJSON events
muster status --json
muster history --json --all
muster --minimal doctor         # plain text
MUSTER_TOKEN=… muster deploy api --json
```

## Auth

The CLI requires a token for every `--json` command. When `muster` launches
the TUI it creates an admin token named `muster-tui` and saves it with
`muster-tui --set-token`, so normally there's nothing to do. muster-tui
looks for it in `$MUSTER_TOKEN`, `~/.muster/tui/config.json`, then the
legacy `~/.muster-tui/config.json`. If none exists and the machine has no
tokens yet, muster-tui creates one itself. Otherwise it still opens (Docker,
settings and the project files work) and the suggestion bar explains how to
pair:

```bash
muster auth create muster-tui --scope admin
muster-tui --set-token <token>
```

muster-tui runs the CLI's token-checked commands one at a time: muster
0.5.x can corrupt `~/.muster/tokens/auth.json` when several `--json`
commands start at once (see [ENGINE-GAPS #0](docs/ENGINE-GAPS.md)). If it
finds that file empty, it restores the empty structure and pairs again.

## Environment

| Variable | Effect |
|---|---|
| `NO_COLOR` / `MUSTER_COLOR=never` | No colour (focus is marked with ▸ and brackets) |
| `MUSTER_THEME=light\|dark` | Skip terminal background detection |
| `MUSTER_BIN` | Path to the muster CLI |
| `MUSTER_DOCKER_BIN` | Path to docker |
| `MUSTER_TOKEN` | Token for `--json` commands |
| `VISUAL` / `EDITOR` | Editor for `edit` and Settings → `e` |

## Development

```bash
go vet ./...
go test ./...
go test ./internal/tui -update     # refresh golden snapshots
```

Architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). What the TUI
still needs from the CLI: [docs/ENGINE-GAPS.md](docs/ENGINE-GAPS.md).

## License

Apache 2.0 — see [LICENSE](LICENSE).

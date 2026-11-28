# Engine gaps

What muster-tui needs from the bash CLI that doesn't exist yet (checked
against muster `feat/easy-mode` at 91ddf83, 2026-11-26). Each item says what
the TUI does today as a fallback and the exact shape it would like. All
additions should keep the existing contract: NDJSON on stdout for streams, a
single JSON document for everything else, `{"error":"<code>","message":"…"}`
on stderr for failures, exit code 0 = ok, non-zero = failure. muster-tui
ignores fields it doesn't know, so extra fields are always safe.

Priority: **P1** blocks a spec'd feature or forces fragile parsing · **P2**
makes a screen noticeably better · **P3** nice to have.

## Status with muster 0.7.0

muster 0.7.0 shipped the engine API (`docs/ENGINE-API.md` in the muster
repo). muster-tui 1.1 uses it when `muster --version` is 0.7.0 or newer and
keeps the fallbacks below for older CLIs.

| # | Gap | 0.7.0 | muster-tui 1.1 uses |
|---|-----|-------|---------------------|
| 0 | token store race | fixed (best-effort locked write) | still serialises token-checked calls (harmless) |
| 1 | deploy takes one service | fixed | `deploy --json svc…` |
| 2 | stop-on-failure / resume | `--stop-on-failure`, `--from` | one stream per deploy, Skip/Retry resume with `--from` |
| 3 | setup scan as JSON | `setup --scan --dry-run --json` | not yet (onboarding keeps the Go preview scanner) |
| 4 | hook approval without a terminal | `hooks status --json`, `hooks approve --yes --json` | approvals screen; the CLI is the authority |
| 5 | k8s diagnostics in JSON | `diagnosis` event | Likely cause / Try in the failure view |
| 6 | preflight / gate details | partly (`files` on `hooks_not_approved`) | open: preflight checks as structured detail |
| 7 | version / image per service | open | — |
| 8 | restart | `restart <svc> --json` | `restart` command |
| 9 | rollback / logs as JSON | `rollback --json`, `logs --json --tail N [--follow]` | both |
| 10 | status details | stack, target, last deploy, lock holders, replicas | service screen, dashboard lock notice; open: latency and error detail |
| 11 | fleet | one shape for `fleet list` / `fleet status` | Servers view (both shapes parsed), `U` fleet health |
| — | failure explanations | `hint` event, `explain --json`, `explain --fix --yes` | failure view and `explain` screen, Fix after a confirm |
| — | assistant | `ask --json` | `ask` screen, Explain with AI |

The sections below are the original requests, kept for the items that are
still open.

---

## 0. Concurrent `--json` calls wipe the token store — P0 (bug)

`auth_validate_token` (lib/core/auth.sh) updates `last_used` on every
`--json` call with

```bash
local tmp="${MUSTER_TOKENS_FILE}.tmp"
jq … "$MUSTER_TOKENS_FILE" > "$tmp" && mv "$tmp" "$MUSTER_TOKENS_FILE"
```

All callers share one `.tmp` name and nothing locks, so concurrent calls
interleave and leave `~/.muster/tokens/auth.json` **empty — every API token
is gone**. Reproduce:

```bash
for i in 1 2 3 4 5 6; do MUSTER_TOKEN=$TOK muster history --json >/dev/null & done; wait
wc -c ~/.muster/tokens/auth.json     # 0
```

(6 of 6 rounds wiped it in testing.) Any frontend, script or CI job that
runs two `--json` commands at once hits this. After that, `muster auth
create` can't bootstrap either: `_auth_ensure_file` only initialises a
*missing* file and `jq` on an empty file writes nothing.

*Today:* the engine serialises the start of every token-checked call
(one at a time until the first output line), and if the CLI rejects the
token while the store is empty, muster-tui restores it to `{"tokens":[]}`
(only when it is 0 bytes) and pairs again, telling the user other tokens
must be recreated.

*Fix wanted:* write via `mktemp` in the same directory + `mv`, take a lock
(`mkdir`/`flock`), validate the JSON before replacing, treat an empty file
as `{"tokens":[]}` in `_auth_ensure_file`, or drop the `last_used` write
from the read path.

## 1. `deploy` takes only one service — P1

`muster deploy [flags] [service]` reads `$1` only, so
`muster deploy --json api web` silently deploys just `api`.

*Today:* the TUI runs one `deploy --json <svc>` per service (it does that
anyway to stop on failure) and one dry run per named service.

*Want:* accept a list, deploy in `deploy_order`:

```
muster deploy --json api web
```

## 2. Stop-on-failure / interactive recovery in JSON mode — P1

In `--json` mode a failed service is recorded and the next one starts
immediately, so a frontend can't offer Retry / Rollback / Skip / Abort
before the next service runs.

*Today:* the TUI orchestrates service by service (above), which loses
`deploy_phases`, one global deploy lock for the whole run and one summary.

*Want:* `--stop-on-failure` that ends the run after the first failure with
the summary marking the rest `"result":"not_run"`, so the TUI can make one
call and resume with `--from <svc>`:

```
muster deploy --json --stop-on-failure
muster deploy --json --from web          # resume after a fix / skip
```

## 3. Setup scan as JSON (dry run) — P1

Onboarding has to show detected services, health checks and order before
anything is written. `muster setup --scan` writes files immediately and
prints prose.

*Today:* muster-tui has its own Go scanner (compose, k8s manifests,
Dockerfile) and then calls `muster setup --stack … --services … --order …
--health …`. For anything else it falls back to `muster setup --scan`
without a preview. The two scanners can disagree (the CLI also turned the
compose `web` service's `nginx:alpine` image into a separate `nginx`
service in testing).

*Want:*

```
muster setup --scan --dry-run --json
```
```json
{
  "project": "acme-store",
  "root": "/srv/acme-store",
  "stack": "compose",
  "found": ["docker-compose.yml"],
  "services": [
    {"key": "redis", "name": "Redis", "kind": "infra", "image": "redis:7",
     "health": {"type": "tcp", "port": 6379, "timeout": 10, "enabled": true},
     "credentials": {"mode": "off"}, "skip_deploy": false, "source": "docker-compose.yml"},
    {"key": "api", "name": "API Server", "kind": "build", "build": "./api",
     "health": {"type": "http", "endpoint": "/health", "port": 8080, "timeout": 10, "enabled": true},
     "credentials": {"mode": "off"}, "skip_deploy": false, "source": "docker-compose.yml"}
  ],
  "deploy_order": ["redis", "api"],
  "warnings": ["api build context contains web/ — add web/ to .dockerignore"]
}
```

## 4. Hook approval without a terminal — P1

`muster hooks approve` calls `sudo -v`. A child process can't prompt, so
the TUI suspends itself (`tea.ExecProcess`) and lets sudo use the terminal.
That works, but there is no machine-readable way to ask *what* is pending
or to see the diff the CLI would approve.

*Today:* the TUI reads `~/.muster/trust/<sha256(path)>/hooks.manifest` and
`approved/` copies directly (with the legacy `.muster/hooks.manifest`
fallback) and computes the diff itself. This duplicates CLI internals
(trust-dir hashing, `_config/` keys) and will break if they move.

*Want:*

```
muster hooks status --json
```
```json
{
  "approved": true,
  "trust_dir": "/Users/me/.muster/trust/362f…",
  "pending": [
    {"key": "api/deploy.sh", "path": ".muster/hooks/api/deploy.sh", "state": "changed",
     "approved_copy": "/Users/me/.muster/trust/362f…/approved/api/deploy.sh",
     "dangerous": []},
    {"key": "_config/muster.json", "path": "muster.json", "state": "changed", "approved_copy": "…"}
  ]
}
```

and, for the approval itself, either `muster hooks approve --json` that
prints `{"event":"auth_required"}` before prompting, or a documented env var
for "this user already authenticated in the frontend".

## 5. k8s diagnostics in JSON — P1

The interactive deploy shows `k8s_diag` output (ImagePullBackOff, OOMKilled,
CrashLoopBackOff …) after a failure. JSON mode doesn't run it.

*Today:* the TUI pattern-matches the last 200 log lines for the same error
strings and shows plain advice.

*Want:* a `diagnosis` event after a failed `done`:

```json
{"event":"diagnosis","service":"api","kind":"k8s","pattern":"ImagePullBackOff",
 "summary":"Kubernetes can't pull acme/api:4f2a9c1","hint":"Check the tag exists and the cluster has registry credentials",
 "pod":"api-7d9c-xk2", "pod_status":"0/1 ImagePullBackOff", "details":["…describe lines…"],"logs":["…last pod log lines…"]}
```

## 6. Preflight and gate details — P2

`{"event":"error","reason":"preflight_failed"}` and `"hooks_not_approved"`
carry no detail; the explanation goes to stderr as prose (with `x` markers).

*Today:* the TUI shows stderr lines under the failed service.

*Want:* structured detail on the same event:

```json
{"event":"error","reason":"preflight_failed","message":"Docker Compose isn't available ('docker compose' failed)",
 "checks":[{"name":"docker compose","ok":false,"hint":"Install the Compose v2 plugin"}],"services":["api","web"]}
{"event":"error","reason":"hooks_not_approved","files":["api/deploy.sh","_config/muster.json"]}
```

## 7. Last deployed version / image per service — P2

The dashboard and service detail want "version/image" per service.
`history --json` only has `commit` (often empty).

*Want:* extra fields on history entries (and in the deploy `summary`):

```json
{"timestamp":"2026-11-26 13:40:05","service":"api","action":"deploy","status":"ok",
 "commit":"4f2a9c1","image":"acme/api:4f2a9c1","duration":42,"source":"local","log_file":"…"}
```

and ISO-8601 timestamps with a zone (`"2026-11-26T13:40:05-05:00"`); the
TUI parses both.

## 8. Restart — P2

There's no `muster restart <svc>`.

*Today:* the TUI runs `docker restart` on the container whose
`com.docker.compose.service` label matches, otherwise redeploys.

*Want:* `muster restart <svc> [--json]` backed by an optional
`restart.sh` hook (templates: `docker compose restart`, `kubectl rollout
restart`, PID restart for dev), emitting the same events as deploy.

## 9. Rollback and logs as JSON — P2

`rollback` and `logs` print prose. Rollback needs the same events as deploy
(start / log / done / health / summary); logs would benefit from a
`--tail N --no-follow` mode so the TUI can show history before following.

```
muster rollback --json api      # same event shapes as deploy
muster logs api --tail 500 --follow
```

## 10. Status details — P2

`status --json` returns only `healthy|unhealthy|disabled|no_health_check`.

*Want:* latency, what was checked and the error, plus k8s replica counts:

```json
{"project":"acme-store","checked_at":"2026-11-26T13:40:05Z","services":{
  "api":{"name":"API Server","status":"unhealthy","health_type":"http","target":"http://localhost:8080/health",
         "latency_ms":5003,"error":"timeout after 5s","replicas":{"ready":2,"desired":3},"deploying":false}}}
```

`deploying` (lock held) lets the rail show a deploy started from another
terminal.

## 11. Fleet — P2

* `fleet list --json` returns `{"machines":[],"groups":[]}` when empty but
  objects otherwise, and the raw `remotes.json` in legacy mode (with
  whatever keys it has). Please always return:
  ```json
  {"machines":[{"name":"prod-1","host":"10.0.0.5","user":"deploy","port":22,"transport":"ssh",
                "mode":"muster","hook_mode":"manual","project_dir":"/opt/acme","fleet":"default","group":"prod"}],
   "groups":[{"name":"prod","machines":["prod-1"]}]}
  ```
* `fleet test --json` (today only `fleet status --json` gives
  `reachable`); the TUI runs its own `ssh -o BatchMode=yes` instead.
* `fleet deploy --json` events should carry `machine` on every line so
  several machines can stream into one view; the TUI currently shows raw
  `fleet deploy` output.
* Cloud agents: `muster cloud agents --json`
  `[{"name":"edge-1","org":"org_1","online":true,"last_seen":"…","version":"…"}]`
  (the Servers screen only shows login state today), and
  `muster cloud status --json` `{"logged_in":true,"name":"…","org":"…","relay":"…","relay_reachable":true,"tunnel_installed":true}`.

## 12. Doctor fixes — P3

`doctor --json` checks have `name/status/category` only.

*Want:* `"detail"`, `"fixable": true|false` and `"fix":"what --fix would do"`
so the TUI can say "3 of these can be fixed automatically" and show them,
and `doctor --fix --json` reporting what it changed:
`{"fixed":[{"name":"…","action":"chmod +x .muster/hooks/api/deploy.sh"}]}`.

## 13. Settings and config — P3

* `muster settings --global --json` (all keys with defaults applied) and
  `muster settings --json` for the project; today the TUI reads
  `~/.muster/settings.json` and `muster.json` directly.
* `muster config validate --json` so the TUI's validation matches the
  CLI's exactly (it reimplements health/port/creds/order/remote/k8s
  checks).
* After the TUI saves `muster.json`, the CLI treats it as a changed config
  needing approval (`_config/muster.json`). That's correct, but a
  `muster config set <path> <value> --json` that edits and re-approves in
  one authenticated step would avoid a second prompt.

## 14. Auth for the TUI — P3

Every `--json` command needs `MUSTER_TOKEN` (`_json_auth_gate`).
`muster` creates a `muster-tui` admin token on launch and stores it with
`muster-tui --set-token`; muster-tui also bootstraps one itself with
`muster auth create muster-tui --scope admin` when the machine has no
tokens yet. If tokens exist but muster-tui has none (e.g. started directly
on a machine where another tool created tokens), it can't pair on its own.

*Want:* `muster auth pair-tui --json` → `{"token":"…","name":"muster-tui"}`
that is allowed for the local user (the same internal path the launcher
uses), or local `--json` reads that don't require a token when run by the
project owner.

## 15. Per-server actions and details — P2

The server screen (remote-first home) wants more than `fleet` offers today:

* **Logs and restart on a server — in progress.** muster-tui implements
  `muster fleet logs <server> <service> [--tail N] [--json]` (NDJSON, the
  server's own `muster logs --json` events) and `muster fleet restart
  <server> [service...] --yes [--json]` (`{"server","results":[{"service",
  "ok","error"}]}`). Both are detected from `muster fleet --help` (a `logs` /
  `restart` line), not the version; until the CLI ships them the server
  screen shows Logs and Restart disabled with "needs a newer muster".
* **Recent deploys on a server.** `fleet status --json` carries each
  service's `last_deploy` (often null); a `muster fleet history <name>
  --json` (the server's own deploy-events log) would fill a "Recent deploys"
  list.
* **Agent / muster version.** `fleet status --json` is gaining a per-server
  `muster_version` (may be ""); the server screen and the status card show
  it when present. An `agent_version` for cloud servers would let them warn
  about old agents.
* **Pairing hint.** `fleet list --json` reported a cloud server as
  `"paired": true` while `fleet deploy --dry-run` listed it as `unpaired`;
  for SSH servers `fleet status --json` says `"error": "unpaired"`. The TUI
  trusts `fleet status`, then `fleet list`. An explicit `paired` field in
  `fleet status --json` would remove the guess.
* **`ask` without a project.** `muster ask` needs a `muster.json`, so "Ask
  assistant" on a server is disabled in a folder without one. A
  `muster ask --machine <name>` that brings that server's context would make
  it work from the servers home.
* **`fleet test --json` — in progress.** `{"servers":[{"name","transport",
  "mode","ok","connect","muster","latency_ms","error"}],"passed","failed"}`,
  exit 1 when any failed. Detected from `muster fleet --help` (the `test`
  line mentions `--json`). Older CLIs: the TUI runs the text command, strips
  colour codes and box drawing and reads each row's tag (`cloud ok, muster
  ok`, `SSH fail`, `unpaired`, …) into the same card; latency is only known
  with `--json`.

## 16. Minor

* `history --json` without `--all` silently truncates to 20; the TUI
  always passes `--all`. A `--since <ts>` / `--limit N` would be cheaper.
* JSON string escaping in the CLI is `sed 's/\\/\\\\/g;s/"/\\"/g'` — tabs
  and control characters in hook output still produce invalid JSON lines.
  The TUI shows such lines as plain output instead of dropping them, but
  `jq -R` style escaping would be safer.
* `deploy --json` exits non-zero now (good); `status --json` exits 0 even
  when services are unhealthy — fine, but worth documenting.

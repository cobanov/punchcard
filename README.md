# punchcard

Time tracking for developers, with the commits attached.

Start a timer against a project, say what you are doing, stop it. When you stop,
punchcard fetches the commits you pushed during that stretch and attaches them to
the record. So a day's log is not "3 hours, refactor" — it is "3 hours, refactor,
7 commits, these files".

And it works in reverse: commits made while no timer was running are surfaced
back to you as a suggested record, which is how a forgotten timer gets recovered.
The evidence was on GitHub all along.

Free to use — run the hosted instance or self-host the identical single binary.

## Status

**v1 is the backend, plus three thin clients.** A working, documented API —
accounts, projects, timers, reports and GitHub commit matching — with a CLI, a
macOS menu bar app, and a small web client at `/app`.

The web client is deliberately minimal and meant to be replaced: one static file
with no build step, so whatever comes next can delete it without unpicking
anything.

## Quick start (self-host)

```bash
cp .env.example deploy/.env    # set POSTGRES_PASSWORD, APP_SECRET, DOMAIN
docker compose -f deploy/docker-compose.yml up --build -d
curl -fsS http://localhost/healthz     # {"status":"ok"}
```

Three secrets decide what works:

| Variable | Without it |
|---|---|
| `APP_SECRET` | the service refuses to start |
| `GITHUB_CLIENT_ID` / `_SECRET` | no GitHub sign-in and no commit matching |
| `GITHUB_TOKEN_KEY` | commit matching stays off — tokens are never stored unencrypted |

Generate the last one with `openssl rand -base64 32`.

## The CLI

The only client that exists today. It talks to the same public API as anything
else would — there is no privileged path.

```bash
go install github.com/cobanov/punchcard/cmd/punchcard-cli@latest
alias punchcard=punchcard-cli      # or rename the binary

punchcard login                    # opens the browser once
punchcard new capsarsiv Acme 2500 TRY
punchcard start caps "yorum sistemi refactor"
punchcard status
punchcard stop
punchcard today
```

Linking a repository to a project is optional — the scanner finds the
repositories you pushed to on its own. Link one when you want punchcard to guess
which project a stretch of unmatched commits belongs to.

`login` binds a loopback listener, opens the browser at the server's GitHub
sign-in, and trades the one-time code it gets back for a device token — the
token itself never passes through the browser, so it never reaches browser
history or OS logs. It is stored in your config directory, mode 0600.

The project name is a prefix: `caps` finds `capsarsiv`. An exact name always
wins, and an ambiguous prefix tells you what it matched.

`--json` on any command gives machine-readable output; `PUNCHCARD_URL` or
`--url=` points it at a self-hosted instance.

## Agent runs

An AI coding agent working in a repository is evidence of work in the same way a
commit is, so punchcard records it the same way: a local hook reports the
interval, and the interval lands under whichever session covers it. Runs never
start or stop a timer and never become billable time — they are attached to the
record you declared, and runs with no session around them show up as unmatched,
next to the commits.

For Claude Code:

```bash
punchcard hook install     # merges two hooks into ~/.claude/settings.json
punchcard sync             # send what has been recorded
```

`hook install` merges — it never rewrites hooks it did not write, and running it
twice does not stack a second copy.

**The integration contract is a file, not an API.** Any tool that can run a
command when it finishes a turn can report runs by appending one JSON object per
line to `$XDG_STATE_HOME/punchcard/queue.jsonl` (default
`~/.local/state/punchcard/queue.jsonl`):

```json
{"tool":"codex","external_id":"<stable id, resent safely>","started_at":"2026-08-25T14:02:11+03:00","ended_at":"2026-08-25T14:44:03+03:00","model":"o4","cwd":"/path/to/work","repo":"owner/repo","tool_calls":14}
```

Only `tool`, `external_id`, `started_at` and `ended_at` are required. `punchcard
sync` sends the queue and clears what the server took; `external_id` is the
idempotency key, so flushing twice costs nothing. The hook itself never touches
the network — it appends a line and returns, which is why it still works on a
plane and cannot make your editor wait on a server.

To send periodically, run `punchcard sync` from launchd, cron or a systemd
timer. Nothing is lost in the meantime: the queue simply grows.

> A run is **reported**, not verified. punchcard fetches commits from GitHub and
> can prove them; a run is a local client's account of itself, and the interface
> keeps that distinction visible rather than flattening the two into one number.

## Local development

```bash
export DOCKER_HOST=unix:///var/run/docker.sock   # or your Docker socket
make check                                        # the full gate
make run                                          # serve on :8080
```

`make check` runs `go vet`, golangci-lint, gosec, govulncheck, an OpenAPI drift
check, and the race-enabled test suite against a real Postgres started by
testcontainers. **It needs `DOCKER_HOST` exported**; without it the integration
tests skip and the run goes green having tested nothing.

## How it fits together

```
cmd/punchcard        the server binary: serve, migrate
cmd/punchcard-cli    the command-line client
internal/cli         the client's logic, tested against a fake API
db/migrations        the schema — the only source of truth (sqlc reads it)
db/queries           hand-written SQL; `make sqlc` generates the Go
internal/repo        every database access; nothing else imports pgx
internal/service     the domain: projects, sessions, reports, GitHub
internal/github      the GitHub REST client and the branch-walking scanner
internal/http        transport: huma operations, middleware, SSE
internal/http/app    the web client — one static file, no build step
apps/menubar         the macOS menu bar app (Swift)
```

Two rules the schema enforces rather than the code:

- **One open session per user.** A partial unique index. Overlapping sessions
  would make a commit belong to two records at once, and every report after that
  would be wrong.
- **One session per commit.** The other half of the same guarantee.

## Origin

punchcard's chassis — identity, the event outbox, webhooks, SSE, observability
and the self-host packaging — is derived from
[helva](https://github.com/cobanov/helva-todo), an agent-native todo service by
the same author. The domain is new; the parts that have nothing to do with
todos or timers were not written twice.

## Licence

MIT.

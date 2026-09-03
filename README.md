<p align="center">
  <strong>punchcard</strong><br>
  Time tracking that shows its work.
</p>

<p align="center">
  <a href="https://punchcard.cobanov.run">punchcard.cobanov.run</a> ·
  <a href="https://punchcard.cobanov.run/app">web app</a> ·
  <a href="https://punchcard.cobanov.run/docs">API</a>
</p>

<p align="center">
  <a href="LICENSE"><img alt="licence" src="https://img.shields.io/badge/licence-MIT-e9973f?labelColor=1a1a1a"></a>
  <img alt="go" src="https://img.shields.io/badge/go-1.26-e9973f?labelColor=1a1a1a">
  <img alt="tests" src="https://img.shields.io/badge/tests-173%20against%20real%20Postgres-e9973f?labelColor=1a1a1a">
  <img alt="self-host" src="https://img.shields.io/badge/self--host-one%20binary-e9973f?labelColor=1a1a1a">
</p>

---

A timesheet is a claim. "Three hours, refactor" cannot be checked by anyone,
including the person who typed it, which is why the last hour of a Friday is
always a guess.

punchcard keeps the claim and attaches the evidence to it. Start a timer against
a project, say what you are doing, stop it. On stop it asks GitHub what you
pushed while the timer was running and files those commits with the record, so
the day reads as three hours, refactor, seven commits, these files.

It also runs backwards. Commits pushed while no timer was running come back as a
suggested record: a timer you forgot to start is not lost work, because the
evidence was on GitHub the whole time.

- **Every branch, not just the default one.** `GET /repos/{o}/{r}/commits` walks the
  default branch and nothing else, so a week on a feature branch returns zero commits, and
  zero looks exactly like a week of no work. The scanner walks every branch, deduping by SHA.
- **Reports are derived from the evidence, not from what you guessed at 09:00.** A session
  asserts when you worked (the timer knows) and what you worked on (you guessed once). Each
  commit and agent run is resolved to a project on its own and the wall clock is split
  accordingly at read time. The declaration is the fallback for the quiet minutes, never the
  overwrite.
- **Agent turns count as evidence.** A local hook records each Claude Code turn under
  whichever session covers it. Runs never start a timer and never become billable minutes,
  and the interface keeps "fetched from GitHub and provable" visibly apart from "a local
  client's account of itself".
- **Money never touches a float.** Rates are integer minor units, amounts are
  `seconds * rate / 3600` in integer arithmetic. 333.33 an hour for ninety minutes has
  exactly one correct answer, and binary floating point is not how you get it.
- **Days end in your timezone.** 22:30 to 23:30 UTC belongs to tomorrow in Istanbul.
  Everything is stored UTC and only the presentation shifts, so the boundary is drawn once,
  in `users.timezone`.

## Try it

```sh
go install github.com/cobanov/punchcard/cmd/punchcard-cli@latest
alias punchcard=punchcard-cli      # or rename the binary

punchcard login                    # opens the browser once
punchcard new capsarsiv Acme 2500 TRY
punchcard start caps "yorum sistemi refactor"
punchcard stop
punchcard today
```

That signs in to the hosted instance. `PUNCHCARD_URL` or `--url=` points the
same binary at your own.

## The clients

All three talk to the same public API. There is no privileged path, so anything awkward for
one client is awkward for every client and belongs fixed in the API.

| | |
| --- | --- |
| **CLI** | `punchcard-cli`, above. `--json` on any command for machine-readable output. |
| **Web** | `/app`. React and Vite, built into the binary. The day drawn as a timeline, inline correction, projects and rates, reports with the commits behind them. |
| **Menu bar** | `apps/menubar`, macOS 14+. Keeps the running timer in view and says something when one has been running for eight hours. `make -C apps/menubar bundle`. |

The project name is a prefix, so `caps` finds `capsarsiv`. An exact name always wins, and an
ambiguous prefix tells you what it matched.

`login` binds a loopback listener and trades a one-time code for a device token, so the
token never passes through the browser and never reaches browser history or OS logs. It is
stored in your config directory, mode 0600.

Linking a repository to a project is optional: the scanner asks GitHub what the account
pushed to and looks there on its own. The mistake is worth naming, because it did not start
that way. Scanning only linked repositories turned an optional refinement into a setup step,
so someone who connected GitHub, started a timer and stopped it got nothing back, with no
error to explain it.

## Where the hour went

Half of one real instance's commits were filed under a project that had nothing
to do with them. The evidence knew where it belonged and nothing ever asked it.

So a report resolves each piece of evidence through a ladder (repository linked
to a project, then a match on the last path segment, then a project of the same
name, then nobody) and partitions the session's wall clock by what it finds.
`?attribution=declared` is the API default and keeps the old behaviour;
`?attribution=evidence` is what the web app sends, and the analytics screen
switches between them so the difference is visible rather than argued about.

A mis-labelled report is not fixed by rewriting session rows. Link the
repository or rename the project, because reports are living views over the
current project set. The CSV export is the freezing mechanism.

## Agent runs

An AI coding agent working in a repository is evidence of work in the same way a
commit is, so punchcard records it the same way.

```sh
punchcard hook install     # merges two hooks into ~/.claude/settings.json
punchcard backfill         # reads the last 90 days of local transcripts
```

`hook install` merges: it never rewrites hooks it did not write, and running it twice does
not stack a copy. `backfill` exists because a hook only sees turns after it is installed,
which on day one is none of them, while Claude Code has been writing a full transcript all
along.

**The integration contract is a file, not an API.** Any tool that can run a
command when it finishes a turn can report runs by appending one JSON object per
line to `$XDG_STATE_HOME/punchcard/queue.jsonl` (default
`~/.local/state/punchcard/queue.jsonl`):

```json
{"tool":"codex","external_id":"<stable id, resent safely>","started_at":"2026-08-25T14:02:11+03:00","ended_at":"2026-08-25T14:44:03+03:00","model":"o4","cwd":"/path/to/work","repo":"owner/repo","tool_calls":14}
```

Only `tool`, `external_id`, `started_at` and `ended_at` are required.
`external_id` is the idempotency key, so sending the same line twice costs
nothing. The hook itself never touches the network. It appends a line and
returns, which is why it still works on a plane and cannot make your editor wait
on a server.

**The queue drains itself.** The hook starts a detached flush a couple of minutes after a
turn, and `stop`, `status`, `today` and `week` each send what is waiting before they answer.
There is no scheduler to install.

```sh
punchcard sync                 # send everything right now
PUNCHCARD_NO_AUTOSYNC=1        # record only, never send by itself
```

An earlier version left this to the reader ("run `punchcard sync` from cron") and the result
was seventy-one turns sitting unsent for two days on the author's own machine, with nothing
saying so. An empty run band looks exactly like a day without an agent, which is why the
silence went unnoticed.

## Self-hosting

Postgres and one binary, which carries its own web app and migrations.

```sh
cp .env.example deploy/.env    # POSTGRES_PASSWORD, APP_SECRET, DOMAIN
docker compose -f deploy/docker-compose.yml up --build -d
curl -fsS http://localhost/healthz    # {"status":"ok"}
```

Three secrets decide what works:

| Variable | Without it |
| --- | --- |
| `APP_SECRET` | the service refuses to start |
| `GITHUB_CLIENT_ID` / `_SECRET` | no GitHub sign-in and no commit matching |
| `GITHUB_TOKEN_KEY` | commit matching stays off, because tokens are never stored unencrypted |

Generate the last one with `openssl rand -base64 32`.

## Development

```sh
export DOCKER_HOST=unix:///var/run/docker.sock   # or your Docker socket
make check                                        # the whole gate
make run                                          # serve on :8080
```

`make check` runs `go vet`, golangci-lint, gosec, govulncheck, an OpenAPI drift
check and the race-enabled suite against a real Postgres started by
testcontainers. **It needs `DOCKER_HOST` exported.** Without it the integration
tests skip and the run goes green having tested nothing, which is the failure
mode this line exists to prevent.

Two more of those: `make openapi` after any route change, because the gate fails when
`docs/openapi.json` drifts and one new field in an input struct is enough. And `make web`
after a frontend change, then restart, because the binary serves the embedded `dist`, not
`web/dist`, so a build you did not embed is a change nobody sees.

```
cmd/                 the server binary and the CLI
db/migrations        the schema, and the only source of truth (sqlc reads it)
db/queries           hand-written SQL; `make sqlc` generates the Go
internal/repo        every database access; nothing else imports pgx
internal/service     the domain: projects, sessions, attribution, reports
internal/github      the REST client and the branch-walking scanner
internal/http        transport: huma operations, middleware, SSE, embedded web
web/, apps/menubar   the React app and the macOS menu bar app
```

Two rules live in the schema rather than in Go, and they belong there:

```sql
CREATE UNIQUE INDEX one_open_session_per_user ON work_sessions (user_id)
    WHERE ended_at IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX idx_session_commits_commit ON session_commits (commit_id);
```

Attribution asks which session covers an instant and assumes the answer is at most one. Two
open sessions overlap, a commit lands in two records, and every report touching those days is
quietly wrong with no error anywhere. A check in the service layer would lose that race
between two clients.

## Origin

The chassis (identity, the event outbox, webhooks, SSE, observability, the self-host
packaging) comes from [helva](https://github.com/cobanov/helva-todo), an agent-native todo
service by the same author. The domain is new; the parts that have nothing to do with todos
or timers were not written twice.

## Licence

MIT.

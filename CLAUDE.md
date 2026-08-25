# punchcard — working notes

What follows is what the code cannot tell you: the traps, and the decisions whose
reasons are not recoverable from reading the result.

For what the project is and how to run it, see `README.md`. For why it is shaped
this way, see `docs/superpowers/specs/2026-08-25-punchcard-design.md`.

## How work is verified

**`make check` is the only gate**: `go vet` · `golangci-lint` · `gosec` ·
`govulncheck` · `openapi-check` · `go test -race ./...` against real Postgres ·
`build`. There is no CI; releases and deploys are by hand.

> **The trap:** `make check` needs `DOCKER_HOST` exported or `testutil.Postgres`
> **skips every integration test** and the run looks green having tested nothing.
> On this Mac (OrbStack):
> ```
> export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
> ```
> Confirm the output reads `ok … internal/http` (~10s) and `ok … internal/service`
> (~6s). A sub-second pass is a failed run wearing green. OrbStack also has to be
> *running*: `docker info` must answer before the suite means anything.

> **The other trap, back again:** the binary serves the EMBEDDED `dist`
> (`internal/http/webui/dist`), not `web/dist`. After a frontend change, run
> `make web` **and restart the server**, or you are looking at the previous
> build. Check with:
> ```
> curl -s localhost:8080/app | grep -o 'assets/index-[^"]*\.js'
> ```
> The served hash is the only thing that proves which build you are testing.
> And never write `make web && pkill -f "punchcard serve"` — `make web` fails on
> a type error, `&&` skips the kill, and the OLD server keeps answering with the
> OLD bundle.

**`make openapi` after any route change.** `openapi-check` fails the gate if
`docs/openapi.json` drifts from the code, and the drift is easy to cause: adding
a field to a handler's input struct changes the document.

## Invariants

### The two names that must never merge

helva called browser logins `sessions`. punchcard calls a stretch of work a
session. They are different tables and they are named apart on purpose:

- `auth_sessions` — cookie logins
- `work_sessions` — the timer

The API path `/v1/sessions` means the second one. If you ever find yourself
writing a query against a table called `sessions`, something has gone wrong.

### Two rules live in the database, not the service

```sql
CREATE UNIQUE INDEX one_open_session_per_user ON work_sessions (user_id)
    WHERE ended_at IS NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX idx_session_commits_commit ON session_commits (commit_id);
```

These are not belt-and-braces. Commit attribution asks "which session covers this
instant?" and assumes the answer is at most one. Two open sessions make time
ranges overlap, a commit lands in two records, and every report that touches
those days is quietly wrong — with no error anywhere. A service-layer check would
lose that race between two clients. Do not move these into Go.

The consequence for `StartSession`: closing the old session and opening the new
one happen in **one transaction**. Any other order hits the index and returns a
constraint violation to a user who did nothing wrong.

### Money is integer minor units

`hourly_rate_cents` is a `bigint`, amounts are `seconds * rate / 3600` in integer
arithmetic. No float touches money anywhere in this codebase. 333.33/hour for
ninety minutes has exactly one correct answer and binary floating point is not
how you get it.

### Day boundaries are drawn in the user's timezone

`users.timezone` exists for one reason: a session from 22:30 to 23:30 UTC belongs
to the *next* day in Istanbul. Reports group with
`date_trunc('day', started_at AT TIME ZONE $tz)`, never on the raw timestamp.
Everything is stored UTC; only presentation shifts.

### Linking a repository to a project is optional

The scanner asks GitHub which repositories the account pushed to since the
window began and looks there; explicitly linked repositories are added on top.

It did not start that way, and the mistake is worth remembering: scanning only
linked repositories turned an optional refinement into a setup step. Connect
GitHub, start a timer, stop it — and get nothing, with no error to explain it,
because the scanner had been told to look nowhere. Attribution is decided by
TIME, so the scanner never needed telling where to look, only when.

What linking is actually for: suggesting which project a stretch of unmatched
commits belongs to.

### Time decides containment; evidence decides labeling

`SessionCovering` and the run reconciler decide WHICH SESSION holds a piece of
evidence, by time alone — unchanged, and still resting on the one-open-session
index. Which PROJECT a report bills a minute to is a separate, derived answer:
`internal/service/attribution.go` resolves each piece of evidence through a
ladder (exact repo link → link by last path segment → project of the same name
→ nobody) and partitions the session's wall-clock accordingly at read time. The
declaration is the fallback for quiet and unclaimed minutes and is never
overwritten. `?attribution=declared` is the API default; the web app sends
`evidence`.

This exists because half of one real instance's commits were filed under a
project with nothing to do with them — the evidence knew where it belonged and
nothing ever asked it. See
`docs/superpowers/specs/2026-08-25-project-attribution-problem.md`.

Do not "fix" a mis-labeled report by rewriting session rows. Link the place or
rename the project — reports are living views over the current project set, and
the CSV export is the freezing mechanism. And keep the sweep exact: it works in
microseconds because session boundaries carry deliberate microsecond nudges, and
its allocations must sum to the clipped duration to the second.

### GitHub's commit listing only sees the default branch

`GET /repos/{o}/{r}/commits` with no `sha` parameter walks the default branch and
nothing else. Anyone working on a feature branch gets **zero** commits back — and
zero looks exactly like "no work happened", so it never gets reported as a bug.

The scanner therefore fetches the branch list and walks every branch, deduping by
SHA. This is the single most important thing in `internal/github`. Do not
"simplify" it back to one call.

Second trap, same feature: a commit that has not been pushed does not exist as far
as the API is concerned. The first scan at stop-time will miss it. That is why
the janitor re-queues the last seven days every hour — a commit written at 09:00
and pushed at 18:00 still finds its session.

### "Due now" is NULL, not a timestamp

`work_sessions.sync_next_at` being NULL means the scan is due immediately. A
real timestamp means one thing only: a backoff deadline.

It used to be set to `now()` on queueing — the DATABASE's clock — and then
compared against a time the Go process captured from ITS clock. A few
milliseconds of drift between the host and the Postgres container and a session
queued at stop-time was not yet "due", so the scan waited a whole tick. In tests
it failed outright, intermittently, which is the worst way to find out. NULL has
no clock in it.

### Events are scoped to the account

helva scoped its outbox and webhooks to a shared list because lists had members.
punchcard has no sharing: `events.user_id` is the filter, `project_id` rides
along for convenience. There is no membership to cache, invalidate, or revoke
mid-stream.

### Someone else's row is a 404, never a 403

A 403 confirms the row exists. Every ownership check returns `ErrNotFound`.

### The bare hostname needs an answer

v1 dropped the SPA, and for one afternoon nothing replaced it: chi's default
handler answered `/` with the words "404 page not found". The service was
healthy and every endpoint worked, but anyone who opened the URL — including its
author — saw a broken site.

`internal/http/landing` is the fix, and `r.NotFound` now answers in the same
problem+json shape as every other error rather than text/plain. If the SPA ever
lands, it replaces the landing page; it does not replace the NotFound handler,
because an unmatched `/v1/...` path is still an API error.

## What was deliberately left behind

The frontend toolchain came back in v1.1: `web/` is React + Vite + Tailwind v4,
built into `internal/http/webui/dist` and embedded. It was left out of v1 on
purpose and added on purpose — a live ticking timer, inline correction and a
drawn timeline are where a component framework earns its cost, and the vanilla
page it replaced had started growing `innerHTML` by the screenful.

Ported from helva and then removed, so you do not go looking for them: lists,
tasks, memberships, invites, the ordering (`position`) machinery, the activity
log, the Gemini chat, the MCP surface, offline sync, and the entire `web/` tree
with its embedded-dist deploy traps. The only HTML v1 serves is the landing
page, the legal documents and the OpenAPI explorer — all static, no build step.

Kept because they cost nothing and the CLI will want them: `internal/service/native_code.go`
(the browser-to-client code exchange a `punchcard login` command would use) and
the webhook/SSE machinery.

## Layout

```
cmd/punchcard/main.go   serve, migrate
db/migrations/          the schema; sqlc reads it as the source of truth
db/queries/             hand-written SQL → make sqlc → internal/repo/db
internal/repo/          all database access; the depguard rule keeps pgx here
internal/service/       domain logic and authorization
internal/github/        REST client + branch-walking scanner
internal/http/          huma operations, middleware, SSE
deploy/                 compose + Caddy for self-hosting
```

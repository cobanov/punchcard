# Agent runs — design and implementation handoff

**Status:** approved by Cobanov 2026-08-25, ready to implement.
**Executor:** read `CLAUDE.md` first — the `DOCKER_HOST` trap, the embedded-dist
trap and the naming rules all apply here. This document is self-contained
otherwise: every decision below was made with the user and is settled. Do not
re-litigate them; do ask if something is genuinely underspecified.

## What this is

punchcard's thesis is "you declare the time, punchcard finds the proof." Today
the proof is commits, pulled from GitHub. This feature adds a second kind:
**working intervals of AI coding agents** (Claude Code first; Codex and anything
else via the same contract), pushed from local hooks. A session's record grows
from "3h, refactor, 7 commits" to "3h, refactor, 7 commits, and Claude worked
14:02–15:31 in this repo."

## Decisions (locked)

1. **Evidence layer, not a timer.** Runs never start or stop `work_sessions`,
   never produce billable time on their own. They attach to sessions exactly the
   way commits do, and runs outside any session surface as unmatched, feeding
   the existing recovery flow.
2. **One row per turn** — prompt submitted → Stop hook fired. Not per Claude
   session (a terminal left open over lunch would fake a three-hour block), not
   per tool call (thousands of rows that no calendar-scale view can show).
3. **Transport is a local queue.** The hook only appends a JSONL line — no
   network, microseconds, works offline. The CLI flushes the queue in batches.
4. **No directory→project mapping table.** A matched run inherits its session's
   project, so the run's own project guess only matters for *unmatched*
   clusters — and there the existing suggestion ladder already answers it:
   linked repo via `project_repos` → project named like the repo/cwd basename →
   the "+ new *name*" create-offer (already shipped for commits). Queue lines
   carry **facts only** (cwd, repo); resolution happens server-side.
5. **Reported, not verified.** A commit is proof punchcard fetched from GitHub
   itself. A run is a client's claim that punchcard cannot check. The UI must
   keep this distinction visible: runs render dimmer, labeled by their tool,
   never with the amber that commits earn.

## Schema — migration `00005_agent_runs.sql`

Mirror the commits pair. Keep the comment style of the existing migrations:
comments carry the *why*.

```sql
-- +goose Up

-- agent_runs: working intervals reported by local agent hooks, cached
-- independently of any session — same reasoning as the commits table: a run
-- that happened while no timer was running is exactly the run the product
-- wants to surface back, and deleting a session must not delete its evidence.
CREATE TABLE agent_runs (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tool           text NOT NULL CHECK (char_length(tool) BETWEEN 1 AND 64),
    -- Client-generated idempotency key; the queue is flushed at-least-once,
    -- so re-sending a batch must not duplicate rows.
    external_id    text NOT NULL CHECK (char_length(external_id) BETWEEN 1 AND 200),
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz NOT NULL,
    model          text NOT NULL DEFAULT '',
    cwd            text NOT NULL DEFAULT '',
    repo_full_name text NOT NULL DEFAULT '',
    tool_calls     integer,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (ended_at >= started_at)
);
CREATE UNIQUE INDEX idx_agent_runs_unique ON agent_runs (user_id, tool, external_id);
CREATE INDEX idx_agent_runs_user_time ON agent_runs (user_id, started_at DESC);

-- session_agent_runs: which session a run was attributed to. UNIQUE on the
-- run for the same reason session_commits has it: sessions cannot overlap,
-- so a run in two sessions means the overlap invariant broke somewhere.
CREATE TABLE session_agent_runs (
    session_id   uuid NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    agent_run_id uuid NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, agent_run_id)
);
CREATE UNIQUE INDEX idx_session_agent_runs_run ON session_agent_runs (agent_run_id);

-- +goose Down
DROP TABLE session_agent_runs;
DROP TABLE agent_runs;
```

## API

**`POST /v1/agent-runs`** — batch upsert.

- Body: `{ "runs": [ { tool, external_id, started_at, ended_at, model?, cwd?,
  repo_full_name?, tool_calls? } ] }`. Cap the batch at 500; reject a run whose
  `ended_at < started_at` or whose duration exceeds 24h (a stale marker, not
  work) with 422.
- Upsert on `(user_id, tool, external_id)`; an existing row wins, the resend is
  counted but not rewritten. Response: `{ "accepted": n, "duplicates": m }`.
- Auth: the normal bearer path — the CLI's stored token already works.
- Run matching (below) happens in the same transaction as the insert.

Reads ride existing surfaces; there is no standalone GET in v1:

- Session detail gains `agent_runs: [...]` next to `commits`.
- The unmatched sweep gains runs (below).

`make openapi` after the route lands — `openapi-check` gates on it.

## Matching

A run attaches to the session whose `[started_at, ended_at)` covers **the run's
`started_at`** — the same "which session covers this instant?" question the
commits ask, leaning on the same DB-enforced fact that open sessions cannot
overlap. A run that started one minute before the timer falls unmatched; that
is honest, and recovery brings it back.

Triggers — all pure local SQL, none of the scanner's backoff/branch machinery:

1. **At ingest**, in the insert transaction.
2. **On session create / stop / edit / recover**, re-match unattached runs whose
   start falls inside the affected window (one statement, alongside the commit
   requeue that `sessions_edit.go` already does).

## Unmatched clusters

Extend `internal/service/unmatched.go`: a cluster's members can now be commits,
runs, or both, merged by the same 30-minute-gap rule. Runs improve the offer —
they carry real start/end, so a run-bearing cluster can suggest a precise
range instead of the 15-minute lead-in heuristic. The suggestion ladder and the
"+ new *name*" create-offer are unchanged; a run's `repo_full_name` (or cwd
basename when there is no repo) feeds the same name lookup the commits use.

## Queue contract (the tool-agnostic part)

`$XDG_STATE_HOME/punchcard/queue.jsonl` (default `~/.local/state/punchcard/`),
one JSON object per line, appended with `O_APPEND` + `flock`:

```json
{"tool":"claude-code","external_id":"<claude-session-id>:<turn-start-unix>",
 "started_at":"2026-08-25T14:02:11+03:00","ended_at":"2026-08-25T14:44:03+03:00",
 "model":"claude-opus-5","cwd":"/Users/cobanov/Developer/punchcard",
 "repo":"cobanov/punchcard","tool_calls":14}
```

Any tool that can run a command at turn end can integrate by appending this
line. That sentence is the whole integration story for Codex and friends —
document it in the README section for self-hosters.

## CLI

- **`punchcard hook emit --tool claude-code`** — reads the hook's JSON from
  stdin. On `UserPromptSubmit`: write a marker file
  `markers/<claude-session-id>` containing now. On `Stop`: read marker → build
  the line (git remote of cwd resolved here, `git config --get remote.origin.url`,
  parsed to `owner/repo`; absent remote = empty repo field) → append to queue →
  delete marker. Missing marker (compaction, crash, interrupt): skip silently —
  no honest window exists. Marker older than 24h: discard, don't emit.
- **`punchcard sync`** — flush the queue in batches; truncate only what the
  server accepted; on network failure leave everything for next time. Also run
  opportunistically (non-fatally) after `punchcard stop`.
- **`punchcard hook install`** — add the two hook entries to
  `~/.claude/settings.json`. **MERGE, never overwrite** — the user's existing
  `Stop` hook (`claude-task-notify.sh`, the ntfy notifier) must keep working;
  Claude Code supports multiple hooks per event. Make the edit idempotent.
  Print the launchd instructions for periodic `punchcard sync` rather than
  installing it in v1.

## Web UI (v1)

- **Session expanded row:** runs listed under the commits, dim (`text-dim` /
  `t-caption`), e.g. `claude-code · opus-5 · 42m · 14 tools`. No amber.
- **Unmatched cluster card:** when a cluster has runs, show the interval —
  "claude worked 14:02–15:31" — beside the commit count.
- No new tab, no new screen.

## Explicitly out of v1 (phase 2)

- `web/src/components/DayTimeline.tsx` is committed but **unwired** — a 24h
  strip of the day's sessions. Wiring it into the Today view is its own small
  task (the user wants it); runs joining it as a thin under-strip comes after.
- Codex installer, launchd auto-setup, and any capture of prompt *text* —
  the last one deliberately: prompts are sensitive and none of the value here
  needs them.

## Verification

Integration tests (real Postgres via testcontainers — remember `DOCKER_HOST`):

- Same batch POSTed twice → same row count, `duplicates` reported.
- Run inside an open-then-stopped session attaches; run outside stays
  unmatched and appears in the sweep alongside commits.
- Editing a session's range re-matches runs the new range now covers, and
  releases runs it no longer covers.
- Another user's runs are invisible (404-not-403 rule).
- 25h run and `ended_at < started_at` → 422.

Live: deploy with `./scratchpad/deploy.sh <version>` (gitignored but present in
this worktree; it ships `git archive HEAD`, so **commit before deploying**),
then verify the served bundle hash per CLAUDE.md, install the hooks on this
Mac, run one real turn, and see it attach.

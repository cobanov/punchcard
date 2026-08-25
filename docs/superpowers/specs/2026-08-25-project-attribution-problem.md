# Project attribution is broken — problem analysis for design

**Status:** problem statement, not a design. Written for a high-reasoning pass to
produce the design. Nothing here should be implemented as written.

**Audience:** someone with no context from the session that produced it. Read
`CLAUDE.md` and `README.md` first for what punchcard is and what its invariants
are; this document assumes both.

---

## The one-line version

punchcard files evidence by the clock and by nothing else. The evidence knows
which project it belongs to, and punchcard never asks it. **More than half of
this instance's commits are currently filed under a project that has nothing to
do with them**, and no part of the system notices.

## The evidence

From the live database, 2026-08-25, one real user, one real day of work.

Scale, for context:

| | |
|---|---|
| projects | 9 |
| projects with any linked repository | **3** |
| sessions | 10 |
| commits (all attached to a session) | 61 |
| agent runs | 114 (60 attached) |
| distinct repositories in commits | 6 |
| distinct repositories in agent runs | 6 |

**Commits filed under a project not linked to their repository — 32 of 61 (52%):**

| project the time was billed to | repository the commit came from | commits |
| --- | --- | --- |
| General | cobanov/punchcard | 7 |
| General | cobanov/herdrchat | 7 |
| ras-meta | cobanov/punchcard | 5 |
| dataland-agent | cobanov/punchcard | 4 |
| herdrchat | cobanov/punchcard | 4 |
| General | cobanov/helva | 2 |
| General | cobanov/claude-skills | 1 |
| General | cobanov/terminal-army | 1 |
| General | cobanov/atasozu | 1 |

**Agent runs filed under a project whose name does not match their repository — 20:**
`ras-meta ← punchcard (4)`, `General ← ghbar (3)`, `herdrchat ← punchcard (2)`,
`General ← longroad (2)`, `General ← herdrchat (2)`, and six more.

**Sessions whose evidence spans several repositories:**

| project | started | repositories in its own evidence |
| --- | --- | --- |
| General | 08:57 | atasozu, claude-skills, helva, herdrchat, punchcard, terminal-army |
| herdrchat | 09:28 | herdrchat, punchcard |

A 31-minute session carrying commits from six different repositories is not an
edge case in this data. It is what a morning looks like for this user.

If any of this were invoiced, the invoice would be wrong. That is the severity.

## Why it happens

There are **four independent notions of "where work happened"**, and no code path
ever reconciles any two of them.

1. **`projects`** — rows a human created. Identity is a uuid. Carries the name,
   client, rate, colour. This is what every report groups by.
2. **`project_repos`** — an *optional* link from a project to an `owner/repo`
   string. Three of nine projects have one. It is used for exactly one thing:
   suggesting a project for an unmatched cluster, and only when the cluster has
   exactly one repository which maps to exactly one project.
3. **`commits.repo_full_name`** — fetched from GitHub. Always known. Never
   compared to anything.
4. **`agent_runs.repo_full_name` and `.cwd`** — reported by a local hook. Often
   known. Never compared to anything.

Attribution runs on time alone. `SessionCovering` asks "which session covers this
instant?" and files the evidence there. That is the documented design — see
`CLAUDE.md`, *"Attribution is decided by TIME"* — and for commits inside a
correctly-declared session it is right. The failure is that **nothing checks the
answer against what the evidence itself says**, so a commit in `cobanov/punchcard`
lands in a session declared as `ras-meta` and the row is written without
complaint.

### The deeper mismatch

The database enforces one open session per user, and attribution assumes at most
one session covers any instant. That is a strong, deliberate invariant and the
commit-matching design rests on it.

But this user works on several projects **simultaneously**. The day view already
proves it: the agent-run band needs three lanes, and 13 distinct working streams
appeared in one day. A session model that permits one project per instant cannot
represent that, so the human is forced to pick one project for a stretch in which
three were worked on — and the evidence for the other two is then filed under the
wrong one.

**This is not a bug to fix inside the current model. The model and the reality
disagree.**

## What has already been done, and why it is not the fix

Several symptoms of this were patched today. They are cosmetic and should not be
mistaken for solutions — a future design may well delete them:

- The day strip merges adjacent runs per repository and packs overlaps into
  lanes (`web/src/components/DayTimeline.tsx`).
- `workKey`/`workLabel` in `web/src/lib/format.ts` reduce `cobanov/herdrchat` and
  a directory named `herdrchat` to one label — **in the presentation layer only**.
  The data model still holds them as unrelated strings.
- Directories that are ancestors of other work directories stop becoming
  fictional projects called "cobanov" and "developer".
- A session's agent runs are summarised per repository rather than per turn.

Every one of these is the interface papering over the fact that the data has no
idea what a project is.

## The questions the design has to answer

These are the actual decisions. They are not independent; the first one probably
determines the rest.

1. **When the evidence and the declaration disagree, which wins — and what does
   the user see?** Today the declaration wins silently. Plausible alternatives:
   the evidence wins; both are kept and reports can be run either way; the
   disagreement is surfaced as something to resolve. Each has a different failure
   mode and a different amount of work.

2. **Can a stretch of time belong to more than one project?** If yes, the
   one-session-per-instant invariant has to be re-examined — carefully, because
   commit attribution and the reports rest on it, and `CLAUDE.md` explains at
   length why it lives in the database. If no, then reports need a way to
   attribute *evidence* to projects independently of the session that contains
   it, and "hours per project" needs a definition that survives a session whose
   evidence points six ways.

3. **What is the canonical identity of a place work happens?** Candidates: the
   full `owner/repo`, its last segment, a working directory, a project uuid. The
   presentation layer currently guesses at the last segment; whatever is chosen
   should live in the data model, and the owner-collision limit (two owners, one
   repository name) should be a deliberate decision rather than an accident.

4. **How does a project acquire its links without anybody doing setup?** Only
   three of nine projects have one, which is why the suggestion ladder rarely
   fires. punchcard's stated philosophy is that linking is optional and the
   scanner finds repositories on its own — so the mapping probably has to be
   *learned* from evidence rather than configured. What learns it, when, and what
   happens when it learns wrong?

5. **What should "hours on project X" mean** when three agents ran in three
   repositories during one declared hour? Wall-clock time cannot be counted three
   times, and the interface has already hit this once: an unmatched cluster
   reported "2h 34m of agent work" across a window of 74 minutes, which was two
   true numbers arranged into a false impression.

## Constraints the design must respect

- **Honesty over convenience.** punchcard's whole thesis is that the record is
  backed by evidence. A design that guesses and does not say it is guessing is
  worse than the current bug, which at least fails visibly once you look.
- **Reported ≠ verified.** Commits are fetched from GitHub and can be proven;
  agent runs are a local client's claim. The distinction is currently carried
  through the schema, the API and the UI, and should survive.
- **No setup steps.** Repository linking is optional by design, and the last time
  it was a prerequisite it made the product look broken on day one
  (`CLAUDE.md`, *"Linking a repository to a project is optional"*).
- **Money is integer minor units**, days are cut in the account's timezone, and
  the `auth_sessions`/`work_sessions` naming rule holds. See `CLAUDE.md`.
- **There is existing data**, including this instance's, and 2275 backfilled
  agent runs per machine. A migration that discards attribution is not free.
- `make check` is the only gate, and it needs `DOCKER_HOST` exported or the
  integration tests silently skip.

## Where to look

| | |
| --- | --- |
| attribution by time | `internal/service/github_scan.go`, `SessionCovering` in `db/queries/work_sessions.sql` |
| agent-run attribution | `internal/service/agent_runs.go`, `reconcileAgentRuns` |
| the suggestion ladder | `internal/service/unmatched.go`, `describeCluster` |
| the optional link | `db/queries/projects.sql`, `ProjectsForRepo` |
| reports | `internal/service/reports.go`, `SummaryByProject` |
| the presentation-layer patches | `web/src/lib/format.ts`, `web/src/components/DayTimeline.tsx` |

## What a good outcome looks like

A design that, run against this instance's existing data, can say for each of
those 32 mis-filed commits either "this belongs to project X" with a reason, or
"this is genuinely ambiguous and here is how the user resolves it" — and that
does not require the user to configure anything before it starts being right.

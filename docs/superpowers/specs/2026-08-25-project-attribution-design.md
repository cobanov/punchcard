# Project attribution — the design

**Status:** designed 2026-08-25 against
[`2026-08-25-project-attribution-problem.md`](2026-08-25-project-attribution-problem.md);
awaiting user review. Read that document first — this one answers it and does
not restate its evidence.

---

## Summary

The bug is a conflation. A session bundles two different assertions — *"I was
working from 08:57 to 09:28"* (a time claim, which the timer genuinely knows)
and *"…on ras-meta"* (a project label, which the user guessed once at Start and
the day then outgrew). Reports treat the label as true for every second of the
claim, and the evidence — which carries its own, stronger label on every row —
is never consulted.

The design separates the two. **Records keep claims; reports derive labels.**
Sessions stay exactly what they are: single-project, non-overlapping,
DB-enforced wall-clock containers. Attribution becomes a **read-time
derivation**: each piece of evidence resolves to a project through a small
ladder, and reports can apportion each session's wall-clock across the projects
its evidence proves were active — with unevidenced minutes following the
declaration, and every number labeled with how it was produced.

punchcard's thesis extends by one clause: *you declare the time, punchcard finds
the proof — and the proof also says where the time went.*

No stored row is rewritten. No new attribution tables. The only persistence
change in the first two phases is that the recovery flow starts writing the
project↔repo links it already knows.

## The five answers

| # | Question | Decision |
|---|---|---|
| 1 | Evidence vs declaration — which wins? | **Neither overwrites the other.** The declaration stands as the record and the fallback; evidence supersedes it *in reports*, visibly, per minute it can vouch for. Disagreement is surfaced on the session, not silently resolved in either direction. |
| 2 | Can a stretch of time belong to several projects? | **As a record, no — as a report, yes.** The one-open-session invariant survives untouched. Multiplicity lives in the derived layer, where a session's wall-clock is partitioned (never double-counted) across the projects its evidence names. |
| 3 | Canonical identity of a place? | **Full form stored, last segment matched.** Evidence keeps its exact `owner/repo` or cwd. Matching uses the lowercased last path segment — the same rule the create-offer already uses to name projects — with exact `owner/repo` link matches taking precedence. The two-owners-one-name collision is accepted deliberately; explicit links break the tie. |
| 4 | How do links accrue without setup? | **From actions the user already takes, plus name equality that needs no persistence.** Recording a cluster into a project links that cluster's repos to it (today only the create-new path does this). A project named like a place matches it implicitly — no row written, so renames take effect immediately and nothing re-learns behind the user's back. There is no write-on-read. |
| 5 | What is "hours on X" under parallel work? | **Two named quantities, never mixed.** *Time* is person-hours: partitions wall-clock, sums exactly to the declared total, what billing uses. *Agent activity* is the sum of run durations: may exceed wall-clock, always labeled as activity. The UI already made this split once ("1h 13m with agents · 2h 34m across 36 runs"); this canonizes it. |

## The resolution ladder

Every piece of evidence gets a **place key**: for a commit, the lowercased last
segment of `repo_full_name`; for a run, the same if it has a repo, else the
lowercased basename of `cwd`, else nothing. Resolution runs at read time, in the
service layer, against the user's current projects and links:

1. **Exact link.** An active link whose `full_name` equals the evidence's full
   `owner/repo`. Strongest; survives owner collisions.
2. **Key link.** A link whose own last segment equals the place key — this is
   how a linked repo also claims the remoteless directory of the same name.
3. **Name match.** A project whose lowercased name equals the place key.
   Implicit, unpersisted: renaming a project immediately changes what it
   claims, in both directions.
4. **Unresolved.** No project claims the place. The evidence stays attached to
   its session and its minutes follow the declaration — but the place is shown
   by name with a one-click *create project* / *link to existing* affordance.
   (This is the existing "+ new *herdrchat*" ladder, moved inside the session.)

A rung that matches **more than one** project resolves to nothing and is
flagged *ambiguous* — the same refusal to guess that `describeCluster` already
practices, for the same reason: a coin-flip lands time on the wrong client's
invoice.

Deliberate non-rules: resolution never invents projects (a machine cannot know
a rate or a client); there is no ancestor-directory rule server-side (a cwd
like `~/` simply resolves to nothing); archived projects resolve like any other
(archiving affects the picker, not history).

## The apportionment sweep

Per session `[s, e)` with declared project `P₀`, computed in memory at read
time — per-user data is small enough that freshness beats materialization, and
a table would need staleness machinery (reconcile on every session edit, link
change, and rename) to answer questions a few hundred rows can answer directly.

1. **Intervals.** Each attached run contributes `[start, min(end, e))`. Each
   attached commit at `t` contributes `[t − 15m, t)` — the same fifteen minutes
   that is already the cluster lead-in and the idle-split threshold; one idea,
   one number. All intervals clipped to the session (and to the report range).
2. **Resolve** each interval's place through the ladder. Unresolved and
   ambiguous intervals are kept for display but contribute nothing to the
   partition.
3. **Sweep.** At every instant, let `A` be the set of resolved projects with an
   active interval. `A = ∅` → the second belongs to `P₀` (basis *declared*).
   `|A| = k` → each active project gets `1/k` of the segment (basis
   *evidence*); integer remainders are handed out one second each in project-id
   order, so the result is deterministic and sums **exactly**.

Properties, each of which becomes a test:

- Allocations sum to the clipped session duration, exactly, always.
- A range's grand total is identical under both report modes — the sweep only
  redistributes seconds between projects, never creates or destroys them.
  (Corollary: `SummaryByDay` and the stats strip are untouched by mode.)
- A session with no evidence is identical to today's report in every range.
- Same inputs → same output, to the second.
- The principal's `AllowsProject` filter applies in both modes.

Money follows the minutes: seconds apportioned to a project bill at **that
project's** rate through the existing `amountCents`. A General-declared hour
whose evidence is punchcard work prices as punchcard work in evidence mode —
that is the point, and the mode label owns the surprise.

## What the user sees

**Session detail** gains an *Evidence by project* block — the per-repo summary
shipped today, upgraded from string-grouping to resolution, with the reason on
each line:

```
punchcard   42m   linked        herdrchat   12m   name match
General      6m   declared, quiet
helva        8m   no project    [Create project] [Link to…]
```

**Session row:** a faint dot after the project name when any evidenced minute
resolved away from the declaration — `text-faint`, never amber (amber keeps
meaning running time and commit proof). The row is informational, not a task
queue; ignoring it is a valid resolution.

**Analytics** gains a two-way switch on the project summary: **By evidence /
As declared**, defaulting to *by evidence* in the web app, with a one-line
method caption: *"each minute goes to the project whose evidence was active;
shared minutes split evenly; quiet minutes follow the timer."* The API default
stays `declared` (`?attribution=` parameter) so existing clients keep their
numbers until they opt in. CSV export accepts the same parameter; in evidence
mode it emits one row per session × project with the apportioned seconds.

**Recovery** (`SessionFromCluster`) writes links for the cluster's repos to the
chosen project — the create-new path already does this; the choose-existing
path starts doing it too. That single change is how the link table stops being
three-of-nine sparse without anyone doing setup.

## Validation against the live data

Running the ladder over the problem document's 32 mis-filed commits: 27 resolve
by name match alone (`punchcard` ×20, `herdrchat` ×7 — both projects exist);
the other 5 (`helva` ×2, `claude-skills`, `terminal-army`, `atasozu`) hit
rung 4 and surface with a create/link affordance. The 08:57 "General" session partitions its 31 minutes across
punchcard and herdrchat by evidence with the helva/atasozu remainder following
the declaration — flagged, resolvable, never guessed. The 09:28 session whose
own note says "two projects in one stretch" finally reports as two projects.
That is the success criterion from the problem document, met without a single
configuration step.

## Alternatives rejected

- **Overlapping / multi-project sessions** — breaks the DB invariant commit
  attribution rests on, complicates the timer into the Clockify punchcard
  exists to escape, and still cannot bill one hour to three projects honestly.
- **Auto-splitting sessions to match evidence** — rewrites the user's own
  declarations, and interleaved work would shred a morning into confetti.
- **Evidence silently overwriting `session.project_id`** — same dishonesty as
  today with the direction reversed.
- **Auto-creating projects from places** — projects carry rates and clients a
  machine cannot know; nine projects would become thirty in a week.
- **Materialized allocation tables** — staleness machinery (and a rename
  invalidates everything) purchased to avoid an in-memory sweep over a few
  hundred rows.

## Phasing

1. **Resolve and reveal.** The resolution service, session attribution in the
   detail view with reasons and affordances, the row dot, recovery writing
   links. No schema change. This alone makes every mis-filed commit visible and
   fixable.
2. **Report truthfully.** The sweep, `?attribution=`, the Analytics switch and
   caption, CSV rows, the property tests.
3. **Places and polish.** Generalize `project_repos` → `project_places`
   (`kind: repo|dir`, `origin: manual|learned`, existing rows migrate as
   manual repos) so remoteless directories like `dataland` become linkable;
   surface places in the project editor; tint the day strip's run band with
   resolved project colours; retire the presentation-layer `workKey` grouping
   in the session detail (the strip keeps its own — lane packing is visual).

Phase 1 and 2 ship value on the existing schema; nothing in them is throwaway
for phase 3.

## Bookkeeping

`CLAUDE.md`'s *"attribution is decided by TIME"* section must be amended when
phase 1 lands: time decides **containment**; evidence decides **labeling** in
reports; the declaration is the fallback, never overwritten. Reports are living
views over the current project set — a rename re-labels history at the next
read, and the CSV export is the freezing mechanism for anyone who needs a
number to stand still.

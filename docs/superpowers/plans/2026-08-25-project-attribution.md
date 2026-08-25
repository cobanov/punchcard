# Project Attribution (Phases 1+2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reports stop billing every second of a session to its declared project; each session's wall-clock is partitioned across the projects its evidence proves were active, with quiet minutes following the declaration and every number labeled with how it was produced.

**Architecture:** A read-time derivation layer in `internal/service/attribution.go`: a four-rung resolution ladder maps each piece of evidence (commit / agent run) to a project, and a microsecond-precision sweep partitions a session's clipped duration across resolved projects (shared segments split evenly, deterministic integer remainders). No new tables, no stored rows rewritten; the only persistence change is that the recovery flow starts writing the project↔repo links it already knows. New endpoint `GET /v1/sessions/{id}/attribution`; new `?attribution=declared|evidence` parameter on the summary and CSV endpoints (API default `declared`; the web app sends `evidence`).

**Tech Stack:** Go 1.26 · huma/v2 · sqlc (no new queries) · testcontainers integration tests · React 19 + Tailwind v4 (embedded dist).

**Spec:** `docs/superpowers/specs/2026-08-25-project-attribution-design.md` (the design) and `docs/superpowers/specs/2026-08-25-project-attribution-problem.md` (the evidence). Read both before Task 1.

## Global Constraints

- `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock` before ANY `go test` / `make check`, or integration tests silently skip (CLAUDE.md trap). A sub-second `internal/http` pass is a failed run wearing green.
- `make check` is the only gate. `make openapi` after any route/param change or `openapi-check` fails the gate.
- Frontend changes require `make web` **and** a server restart to be visible; the binary serves the embedded dist. Never `make web && pkill`.
- Commits: author `Mert Cobanov <mertcobanov@gmail.com>`, explanatory English messages, **no Claude co-author line** (use `git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit`).
- Money is integer minor units; no float touches money. Apportionment must sum **exactly** to the clipped session duration in seconds.
- Amber in the UI means only running time and commit proof. Attribution UI uses `text-dim`/`text-faint`.
- The fifteen-minute constant is one idea with one number: reuse `clusterLeadIn` (`internal/service/unmatched.go`), do not define a second constant.
- `auth_sessions` vs `work_sessions` naming rule holds; "session" below always means `work_sessions`.

---

### Task 1: Resolution ladder + apportionment sweep (pure core)

**Files:**
- Create: `internal/service/attribution.go`
- Test: `internal/service/attribution_unit_test.go` (pure, no DB — keep the name distinct from any future integration file)

**Interfaces:**
- Consumes: `clusterLeadIn` (existing, `internal/service/unmatched.go`), `db.Project`, `db.ProjectRepo` (existing sqlc types).
- Produces (later tasks rely on these exact names):
  - `func placeKey(repo, cwd string) string`
  - `type attributionReason string` with constants `reasonLinked ("linked")`, `reasonName ("name")`, `reasonDeclared ("declared")`, `reasonNoProject ("no_project")`, `reasonAmbiguous ("ambiguous")`
  - `func buildResolver(projects []db.Project, links []db.ProjectRepo) *resolver`
  - `func (r *resolver) resolve(repo, cwd string) (uuid.UUID, attributionReason, string)`
  - `type evidenceSpan struct { project uuid.UUID; reason attributionReason; key, fullName string; from, to int64 }` (from/to are Unix **microseconds**)
  - `type Allocation struct { ProjectID uuid.UUID; Seconds int64; Evidenced bool; Reason attributionReason }`
  - `type UnresolvedPlace struct { Key, FullName string; Seconds int64; Ambiguous bool }`
  - `func apportion(declared uuid.UUID, from, to time.Time, spans []evidenceSpan) ([]Allocation, []UnresolvedPlace)`
  - `resolver` also exposes `projects map[uuid.UUID]db.Project` (Task 4 reads project metadata from it).

- [ ] **Step 1: Write the failing tests**

```go
package service

import (
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
)

func mkProject(name string) db.Project {
	id, _ := uuid.NewV7()
	return db.Project{ID: id, Name: name}
}

func mkLink(projectID uuid.UUID, fullName string) db.ProjectRepo {
	id, _ := uuid.NewV7()
	return db.ProjectRepo{ID: id, ProjectID: projectID, FullName: fullName}
}

func TestPlaceKeyTakesTheLastSegmentLowercased(t *testing.T) {
	cases := []struct{ repo, cwd, want string }{
		{"cobanov/HerdrChat", "", "herdrchat"},
		{"", "/Users/cobanov/Developer/herdrchat", "herdrchat"},
		{"", "/Users/cobanov/Workspace/RAS/META", "meta"},
		{"cobanov/punchcard", "/somewhere/else", "punchcard"}, // repo wins over cwd
		{"", "/", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := placeKey(c.repo, c.cwd); got != c.want {
			t.Errorf("placeKey(%q, %q) = %q, want %q", c.repo, c.cwd, got, c.want)
		}
	}
}

// The ladder, rung by rung. An exact owner/repo link beats a name match, a
// name match needs exactly one project, and two claimants resolve to nobody —
// a coin flip lands time on the wrong client's invoice.
func TestResolutionLadder(t *testing.T) {
	alpha := mkProject("alpha")
	beta := mkProject("beta")
	general := mkProject("General")
	linked := mkLink(beta.ID, "cobanov/alpha") // deliberately crossed: link beats name

	r := buildResolver([]db.Project{alpha, beta, general}, []db.ProjectRepo{linked})

	// Rung 1: exact link wins even though a project is NAMED alpha.
	if id, reason, _ := r.resolve("cobanov/alpha", ""); id != beta.ID || reason != reasonLinked {
		t.Fatalf("exact link: got %v %v, want beta/linked", id, reason)
	}
	// Rung 2: a remoteless directory of the linked repo's name follows the link.
	if id, reason, _ := r.resolve("", "/w/alpha"); id != beta.ID || reason != reasonLinked {
		t.Fatalf("key link: got %v %v, want beta/linked", id, reason)
	}
	// Rung 3: name match, case-insensitive.
	if id, reason, _ := r.resolve("cobanov/Beta", ""); id != beta.ID || reason != reasonName {
		t.Fatalf("name: got %v %v, want beta/name", id, reason)
	}
	// Rung 4: nobody claims it.
	if id, reason, key := r.resolve("cobanov/helva", ""); id != uuid.Nil || reason != reasonNoProject || key != "helva" {
		t.Fatalf("no project: got %v %v %q", id, reason, key)
	}
	// Ambiguity: two projects named the same → refuse.
	twin := mkProject("beta")
	r2 := buildResolver([]db.Project{beta, twin}, nil)
	if id, reason, _ := r2.resolve("cobanov/beta", ""); id != uuid.Nil || reason != reasonAmbiguous {
		t.Fatalf("ambiguous: got %v %v, want Nil/ambiguous", id, reason)
	}
}

func span(p uuid.UUID, reason attributionReason, key string, from, to time.Time) evidenceSpan {
	return evidenceSpan{project: p, reason: reason, key: key, from: from.UnixMicro(), to: to.UnixMicro()}
}

// One hour declared on alpha; beta evidence covers the middle 30 minutes.
func TestApportionSplitsQuietAndEvidencedMinutes(t *testing.T) {
	alpha, beta := mkProject("alpha"), mkProject("beta")
	s := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	e := s.Add(time.Hour)

	allocs, unres := apportion(alpha.ID, s, e,
		[]evidenceSpan{span(beta.ID, reasonName, "beta", s.Add(10*time.Minute), s.Add(40*time.Minute))})

	if len(unres) != 0 {
		t.Fatalf("unexpected unresolved: %+v", unres)
	}
	got := map[uuid.UUID]int64{}
	var sum int64
	for _, a := range allocs {
		got[a.ProjectID] += a.Seconds
		sum += a.Seconds
	}
	if sum != 3600 {
		t.Fatalf("allocations sum to %d, want exactly 3600", sum)
	}
	if got[beta.ID] != 1800 || got[alpha.ID] != 1800 {
		t.Fatalf("split = alpha %d / beta %d, want 1800/1800", got[alpha.ID], got[beta.ID])
	}
}

// Two projects active at once share the segment evenly.
func TestApportionSharesParallelSegmentsEvenly(t *testing.T) {
	p0, a, b := mkProject("p0"), mkProject("a"), mkProject("b")
	s := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	e := s.Add(20 * time.Minute)

	allocs, _ := apportion(p0.ID, s, e, []evidenceSpan{
		span(a.ID, reasonName, "a", s, e),
		span(b.ID, reasonName, "b", s, e),
	})
	got := map[uuid.UUID]int64{}
	for _, al := range allocs {
		got[al.ProjectID] += al.Seconds
	}
	if got[a.ID] != 600 || got[b.ID] != 600 || got[p0.ID] != 0 {
		t.Fatalf("shares = %v, want a=600 b=600 p0=0", got)
	}
}

// An unresolved place never enters the partition: its minutes follow the
// declaration, and it is reported by name for the create/link affordance.
func TestApportionKeepsUnresolvedOutOfThePartition(t *testing.T) {
	p0 := mkProject("p0")
	s := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	e := s.Add(30 * time.Minute)

	allocs, unres := apportion(p0.ID, s, e, []evidenceSpan{
		{project: uuid.Nil, reason: reasonNoProject, key: "helva", fullName: "cobanov/helva",
			from: s.Add(5 * time.Minute).UnixMicro(), to: s.Add(13 * time.Minute).UnixMicro()},
	})
	if len(allocs) != 1 || allocs[0].ProjectID != p0.ID || allocs[0].Seconds != 1800 || allocs[0].Evidenced {
		t.Fatalf("allocations = %+v, want all 1800s declared to p0", allocs)
	}
	if len(unres) != 1 || unres[0].Key != "helva" || unres[0].FullName != "cobanov/helva" || unres[0].Seconds != 480 {
		t.Fatalf("unresolved = %+v, want helva 480s", unres)
	}
}

// The three properties the design promises, over random inputs.
func TestApportionProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	projects := []uuid.UUID{}
	for i := 0; i < 4; i++ {
		p, _ := uuid.NewV7()
		projects = append(projects, p)
	}
	s := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)

	for i := 0; i < 200; i++ {
		durSec := int64(60 + rng.Intn(4*3600))
		e := s.Add(time.Duration(durSec) * time.Second)
		var spans []evidenceSpan
		for j := 0; j < rng.Intn(12); j++ {
			f := s.Add(time.Duration(rng.Int63n(durSec*1_000_000)) * time.Microsecond)
			to := f.Add(time.Duration(1+rng.Int63n(3600_000_000)) * time.Microsecond)
			spans = append(spans, span(projects[rng.Intn(3)+1], reasonName, "x", f, to))
		}
		a1, _ := apportion(projects[0], s, e, spans)
		a2, _ := apportion(projects[0], s, e, spans)

		var sum int64
		for _, a := range a1 {
			sum += a.Seconds
		}
		if sum != durSec {
			t.Fatalf("case %d: sum %d != duration %d", i, sum, durSec)
		}
		if len(a1) != len(a2) {
			t.Fatalf("case %d: nondeterministic length", i)
		}
		for k := range a1 {
			if a1[k] != a2[k] {
				t.Fatalf("case %d: nondeterministic at %d: %+v vs %+v", i, k, a1[k], a2[k])
			}
		}
	}

	// No evidence at all → one declared row covering everything.
	e := s.Add(90 * time.Minute)
	allocs, _ := apportion(projects[0], s, e, nil)
	if len(allocs) != 1 || allocs[0].Seconds != 5400 || allocs[0].Evidenced || allocs[0].Reason != reasonDeclared {
		t.Fatalf("no-evidence = %+v", allocs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/Developer/punchcard && go test ./internal/service/ -run 'TestPlaceKey|TestResolutionLadder|TestApportion' 2>&1 | head -20`
Expected: FAIL — `undefined: placeKey`, `undefined: buildResolver`, etc.

- [ ] **Step 3: Write `internal/service/attribution.go`**

```go
package service

// Attribution: which project each minute of a session belongs to.
//
// A session bundles two assertions — "I was working from A to B" (a time
// claim the timer genuinely knows) and "…on project P" (a label the user
// guessed once at Start). The bug this file exists to fix is treating the
// label as true for every second of the claim. Records keep the claim;
// everything here derives labels at read time from the evidence inside the
// session, falling back to the declaration for the minutes nothing vouches
// for. Nothing is stored: per-user data is small enough that freshness beats
// materialization, and a project rename re-labels history at the next read.

import (
	"bytes"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo/db"
)

type attributionReason string

const (
	reasonLinked    attributionReason = "linked"
	reasonName      attributionReason = "name"
	reasonDeclared  attributionReason = "declared"
	reasonNoProject attributionReason = "no_project"
	reasonAmbiguous attributionReason = "ambiguous"
)

// placeKey is the canonical identity of a place work happens: the lowercased
// last path segment of the repository full name, or of the working directory
// when there is no repository. It is the same rule the create-offer uses to
// name a project after a repo, which is exactly why name matching works with
// zero setup. Two owners sharing a repository name collide here — accepted
// deliberately; an explicit link (rung 1) breaks the tie.
func placeKey(repo, cwd string) string {
	source := repo
	if source == "" {
		source = cwd
	}
	source = strings.TrimRight(strings.TrimSpace(strings.ReplaceAll(source, "\\", "/")), "/")
	if source == "" {
		return ""
	}
	base := path.Base(source)
	if base == "." || base == "/" {
		return ""
	}
	return strings.ToLower(base)
}

// resolver answers "which project claims this place?" for one user. Built once
// per request from the current projects and links, so a rename or a new link
// takes effect on the very next read.
type resolver struct {
	byFullName map[string][]uuid.UUID
	byLinkKey  map[string][]uuid.UUID
	byName     map[string][]uuid.UUID
	projects   map[uuid.UUID]db.Project
}

func buildResolver(projects []db.Project, links []db.ProjectRepo) *resolver {
	r := &resolver{
		byFullName: map[string][]uuid.UUID{},
		byLinkKey:  map[string][]uuid.UUID{},
		byName:     map[string][]uuid.UUID{},
		projects:   map[uuid.UUID]db.Project{},
	}
	for _, p := range projects {
		r.projects[p.ID] = p
		if key := strings.ToLower(strings.TrimSpace(p.Name)); key != "" {
			r.byName[key] = appendID(r.byName[key], p.ID)
		}
	}
	for _, l := range links {
		full := strings.ToLower(strings.TrimSpace(l.FullName))
		if full == "" {
			continue
		}
		r.byFullName[full] = appendID(r.byFullName[full], l.ProjectID)
		if key := placeKey(l.FullName, ""); key != "" {
			r.byLinkKey[key] = appendID(r.byLinkKey[key], l.ProjectID)
		}
	}
	return r
}

func appendID(ids []uuid.UUID, id uuid.UUID) []uuid.UUID {
	for _, have := range ids {
		if have == id {
			return ids
		}
	}
	return append(ids, id)
}

// resolve runs the ladder. The zero uuid means "no project claims this place";
// the reason says whether that is because nobody does (no_project) or because
// more than one does (ambiguous) — the same refusal to guess that the cluster
// suggestion practices, for the same reason.
func (r *resolver) resolve(repo, cwd string) (uuid.UUID, attributionReason, string) {
	key := placeKey(repo, cwd)
	if repo != "" {
		if ids := r.byFullName[strings.ToLower(repo)]; len(ids) == 1 {
			return ids[0], reasonLinked, key
		} else if len(ids) > 1 {
			return uuid.Nil, reasonAmbiguous, key
		}
	}
	if key == "" {
		return uuid.Nil, reasonNoProject, ""
	}
	if ids := r.byLinkKey[key]; len(ids) == 1 {
		return ids[0], reasonLinked, key
	} else if len(ids) > 1 {
		return uuid.Nil, reasonAmbiguous, key
	}
	if ids := r.byName[key]; len(ids) == 1 {
		return ids[0], reasonName, key
	} else if len(ids) > 1 {
		return uuid.Nil, reasonAmbiguous, key
	}
	return uuid.Nil, reasonNoProject, key
}

// evidenceSpan is one piece of evidence as an interval, already resolved.
// from/to are Unix microseconds: sessions carry microsecond nudges (see
// StopOpenSessionForUser), and sweeping in seconds would truncate them into
// off-by-one totals.
type evidenceSpan struct {
	project  uuid.UUID // uuid.Nil = unresolved; display-only
	reason   attributionReason
	key      string
	fullName string
	from, to int64
}

// Allocation is one project's share of a session's clipped wall-clock.
// The declared project may appear twice: once for the minutes its own
// evidence earned (Evidenced) and once for the quiet fallback minutes.
type Allocation struct {
	ProjectID uuid.UUID
	Seconds   int64
	Evidenced bool
	Reason    attributionReason
}

// UnresolvedPlace is evidence whose place no project claims. Its minutes are
// activity (interval union), not an allocation — they already followed the
// declaration — and it exists so the UI can offer create/link by name.
type UnresolvedPlace struct {
	Key       string
	FullName  string
	Seconds   int64
	Ambiguous bool
}

// apportion partitions [from, to) across projects by evidence.
//
// The sweep walks segment boundaries; at each segment the set of active
// resolved projects splits it evenly, an empty set hands it to the declared
// project. Integer remainders — of the even split and of the micro→second
// conversion — are dealt out in project-id order, so the result is
// deterministic and sums exactly to the clipped duration in seconds.
func apportion(declared uuid.UUID, from, to time.Time, spans []evidenceSpan) ([]Allocation, []UnresolvedPlace) {
	lo, hi := from.UnixMicro(), to.UnixMicro()
	if hi <= lo {
		return nil, nil
	}

	var resolved []evidenceSpan
	reasons := map[uuid.UUID]attributionReason{}
	type uColl struct {
		fullName  string
		ambiguous bool
		spans     [][2]int64
	}
	unresolved := map[string]*uColl{}

	for _, s := range spans {
		f, t := max(s.from, lo), min(s.to, hi)
		if t <= f {
			continue
		}
		if s.project == uuid.Nil {
			if s.key == "" {
				continue
			}
			u := unresolved[s.key]
			if u == nil {
				u = &uColl{fullName: s.fullName, ambiguous: s.reason == reasonAmbiguous}
				unresolved[s.key] = u
			}
			if u.fullName == "" {
				u.fullName = s.fullName
			}
			u.spans = append(u.spans, [2]int64{f, t})
			continue
		}
		resolved = append(resolved, evidenceSpan{project: s.project, from: f, to: t})
		// The strongest reason observed represents the project: linked > name.
		if cur, ok := reasons[s.project]; !ok || (cur == reasonName && s.reason == reasonLinked) {
			reasons[s.project] = s.reason
		}
	}

	// Boundary sweep over the resolved spans.
	boundSet := map[int64]struct{}{lo: {}, hi: {}}
	for _, s := range resolved {
		boundSet[s.from] = struct{}{}
		boundSet[s.to] = struct{}{}
	}
	cuts := make([]int64, 0, len(boundSet))
	for b := range boundSet {
		cuts = append(cuts, b)
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })

	evidencedMicro := map[uuid.UUID]int64{}
	var declaredMicro int64
	for i := 0; i+1 < len(cuts); i++ {
		a, b := cuts[i], cuts[i+1]
		var active []uuid.UUID
		for _, s := range resolved {
			if s.from <= a && b <= s.to {
				active = appendID(active, s.project)
			}
		}
		length := b - a
		if len(active) == 0 {
			declaredMicro += length
			continue
		}
		sort.Slice(active, func(x, y int) bool {
			return bytes.Compare(active[x][:], active[y][:]) < 0
		})
		share := length / int64(len(active))
		rem := length % int64(len(active))
		for idx, id := range active {
			add := share
			if int64(idx) < rem {
				add++
			}
			evidencedMicro[id] += add
		}
	}

	// Micro → whole seconds by largest remainder, so per-session seconds sum
	// exactly to the rounded clipped duration.
	type part struct {
		id        uuid.UUID
		micro     int64
		evidenced bool
	}
	parts := make([]part, 0, len(evidencedMicro)+1)
	for id, m := range evidencedMicro {
		parts = append(parts, part{id: id, micro: m, evidenced: true})
	}
	parts = append(parts, part{id: declared, micro: declaredMicro, evidenced: false})
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].evidenced != parts[j].evidenced {
			return parts[i].evidenced
		}
		return bytes.Compare(parts[i].id[:], parts[j].id[:]) < 0
	})

	totalSec := (hi - lo + 500_000) / 1_000_000
	secs := make([]int64, len(parts))
	var floorSum int64
	for i, p := range parts {
		secs[i] = p.micro / 1_000_000
		floorSum += secs[i]
	}
	leftover := totalSec - floorSum
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(x, y int) bool {
		rx, ry := parts[order[x]].micro%1_000_000, parts[order[y]].micro%1_000_000
		if rx != ry {
			return rx > ry
		}
		return bytes.Compare(parts[order[x]].id[:], parts[order[y]].id[:]) < 0
	})
	for i := int64(0); i < leftover && int(i) < len(order); i++ {
		secs[order[i]]++
	}

	allocs := make([]Allocation, 0, len(parts))
	for i, p := range parts {
		if secs[i] == 0 {
			continue
		}
		reason := reasonDeclared
		if p.evidenced {
			reason = reasons[p.id]
		}
		allocs = append(allocs, Allocation{ProjectID: p.id, Seconds: secs[i], Evidenced: p.evidenced, Reason: reason})
	}
	// Evidenced rows first, biggest first; the declared fallback last.
	sort.SliceStable(allocs, func(i, j int) bool {
		if allocs[i].Evidenced != allocs[j].Evidenced {
			return allocs[i].Evidenced
		}
		return allocs[i].Seconds > allocs[j].Seconds
	})

	places := make([]UnresolvedPlace, 0, len(unresolved))
	for key, u := range unresolved {
		if sec := (unionMicros(u.spans) + 500_000) / 1_000_000; sec > 0 {
			places = append(places, UnresolvedPlace{Key: key, FullName: u.fullName, Seconds: sec, Ambiguous: u.ambiguous})
		}
	}
	sort.Slice(places, func(i, j int) bool {
		if places[i].Seconds != places[j].Seconds {
			return places[i].Seconds > places[j].Seconds
		}
		return places[i].Key < places[j].Key
	})
	return allocs, places
}

// unionMicros is the total covered length of possibly-overlapping intervals —
// activity for display, where double-counting an overlap would overstate it.
func unionMicros(spans [][2]int64) int64 {
	if len(spans) == 0 {
		return 0
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	total, curF, curT := int64(0), spans[0][0], spans[0][1]
	for _, s := range spans[1:] {
		if s[0] > curT {
			total += curT - curF
			curF, curT = s[0], s[1]
			continue
		}
		if s[1] > curT {
			curT = s[1]
		}
	}
	return total + (curT - curF)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/service/ -run 'TestPlaceKey|TestResolutionLadder|TestApportion' -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: all PASS. (These are pure tests; no DOCKER_HOST needed yet.)

- [ ] **Step 5: Commit**

```bash
git add internal/service/attribution.go internal/service/attribution_unit_test.go
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Add the attribution core: a resolution ladder and an exact sweep

The ladder maps a piece of evidence to a project: exact repo link, then a
link matching the place's last path segment, then a project named like the
place, then nobody — and any rung with two claimants resolves to nobody,
because a coin flip lands time on the wrong client's invoice.

The sweep partitions a session's clipped wall-clock across the projects
with active evidence, splitting shared segments evenly and handing quiet
segments to the declaration. It works in microseconds because session
boundaries carry deliberate microsecond nudges, and it hands out integer
remainders in project-id order so the same inputs always produce the same
seconds, summing exactly to the clipped duration. Property-tested over 200
random cases."
```

---

### Task 2: Session attribution — service + endpoint

**Files:**
- Modify: `internal/service/attribution.go` (append)
- Create: `internal/http/attribution_handlers.go`
- Modify: `internal/http/routes.go` (one line, next to `d.registerAgentRunRoutes(api)`)
- Test: `internal/http/attribution_test.go`

**Interfaces:**
- Consumes: Task 1's `buildResolver`, `apportion`, `evidenceSpan`; existing `d.GetSession`, `d.store.ListProjects(ctx, db.ListProjectsParams{OwnerID, IncludeArchived: true})`, `d.store.ListReposForUser(ctx, userID)`, `d.store.ListCommitsForSession(ctx, sessionID)`, `d.store.ListAgentRunsForSession(ctx, sessionID)`, `clusterLeadIn`.
- Produces:
  - `func (d *Domain) newResolver(ctx context.Context, userID uuid.UUID) (*resolver, error)`
  - `func (d *Domain) sessionSpans(ctx context.Context, r *resolver, sessionID uuid.UUID) ([]evidenceSpan, error)`
  - `func (d *Domain) SessionAttribution(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) ([]Allocation, []UnresolvedPlace, error)`
  - HTTP: `GET /v1/sessions/{id}/attribution` → `{"allocations":[{"project_id","name","seconds","evidenced","reason"}],"unresolved":[{"place","full_name","seconds","ambiguous"}]}` — both fields always arrays, never omitted (the omitempty lesson from the analytics crash).

- [ ] **Step 1: Write the failing integration test**

```go
package http

import (
	"net/http"
	"testing"
	"time"
)

type attributionBody struct {
	Allocations []struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Seconds   int64  `json:"seconds"`
		Evidenced bool   `json:"evidenced"`
		Reason    string `json:"reason"`
	} `json:"allocations"`
	Unresolved []struct {
		Place    string `json:"place"`
		FullName string `json:"full_name"`
		Seconds  int64  `json:"seconds"`
	} `json:"unresolved"`
}

// One declared hour on alpha; a 30-minute run in cobanov/beta and a 5-minute
// run in cobanov/gamma (no such project). Beta earns its half hour by name,
// gamma surfaces as unresolved, and alpha keeps the rest — with the total
// summing to exactly the hour.
func TestSessionAttributionSplitsByEvidence(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-split@example.com")
	alphaID := newProject(t, c, base, csrf, "alpha")
	_ = newProject(t, c, base, csrf, "beta")

	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": alphaID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)

	must(t, "beta run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "beta-run", start.Add(10*time.Minute), start.Add(40*time.Minute),
			map[string]any{"repo": "cobanov/beta"}), csrf), http.StatusAccepted)
	must(t, "gamma run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "gamma-run", start.Add(45*time.Minute), start.Add(50*time.Minute),
			map[string]any{"repo": "cobanov/gamma"}), csrf), http.StatusAccepted)

	code, raw = do(t, c, http.MethodGet, base+"/v1/sessions/"+ws.ID+"/attribution", nil, nil)
	must(t, "attribution", code, http.StatusOK)
	var body attributionBody
	unmarshal(t, raw, &body)

	var sum int64
	byName := map[string]int64{}
	reasons := map[string]string{}
	for _, a := range body.Allocations {
		sum += a.Seconds
		byName[a.Name] += a.Seconds
		if a.Evidenced {
			reasons[a.Name] = a.Reason
		}
	}
	if sum != 3600 {
		t.Fatalf("allocations sum to %d, want 3600: %+v", sum, body.Allocations)
	}
	if byName["beta"] != 1800 || reasons["beta"] != "name" {
		t.Fatalf("beta = %ds via %q, want 1800 via name", byName["beta"], reasons["beta"])
	}
	if byName["alpha"] != 1800 {
		t.Fatalf("alpha = %ds, want 1800 (quiet + gamma fallback)", byName["alpha"])
	}
	if len(body.Unresolved) != 1 || body.Unresolved[0].Place != "gamma" ||
		body.Unresolved[0].FullName != "cobanov/gamma" || body.Unresolved[0].Seconds != 300 {
		t.Fatalf("unresolved = %+v, want gamma 300s", body.Unresolved)
	}
}

// Another user's session is a 404, never a 403 (the ownership rule).
func TestSessionAttributionIsInvisibleAcrossAccounts(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	owner, _ := registerActor(t, base, "attr-owner@example.com")
	pid := newProject(t, owner, base, csrf, "mine")
	start := time.Now().UTC().Add(-2 * time.Hour)
	code, raw := do(t, owner, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": pid, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)

	other, _ := registerActor(t, base, "attr-other@example.com")
	code, _ = do(t, other, http.MethodGet, base+"/v1/sessions/"+ws.ID+"/attribution", nil, nil)
	must(t, "cross-account", code, http.StatusNotFound)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock && go test ./internal/http/ -run 'TestSessionAttribution' 2>&1 | tail -5`
Expected: FAIL (404 on the route — it does not exist yet). If the run takes under a second, DOCKER_HOST is not exported; stop and fix that first.

- [ ] **Step 3: Append the service methods to `internal/service/attribution.go`**

```go
// newResolver loads the user's current projects and links. Archived projects
// resolve like any other — archiving affects the timer's picker, not history —
// and deleted ones are already excluded by the queries.
func (d *Domain) newResolver(ctx context.Context, userID uuid.UUID) (*resolver, error) {
	projects, err := d.store.ListProjects(ctx, db.ListProjectsParams{OwnerID: userID, IncludeArchived: true})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	links, err := d.store.ListReposForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list repo links: %w", err)
	}
	return buildResolver(projects, links), nil
}

// sessionSpans turns a session's attached evidence into resolved spans.
// A run is a real interval; a commit is an instant that vouches for the
// fifteen minutes leading to it — the same clusterLeadIn the unmatched
// clustering uses, one idea with one number. Clipping happens in apportion.
func (d *Domain) sessionSpans(ctx context.Context, r *resolver, sessionID uuid.UUID) ([]evidenceSpan, error) {
	commits, err := d.store.ListCommitsForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}
	runs, err := d.store.ListAgentRunsForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agent runs: %w", err)
	}
	spans := make([]evidenceSpan, 0, len(commits)+len(runs))
	for _, c := range commits {
		pid, reason, key := r.resolve(c.RepoFullName, "")
		spans = append(spans, evidenceSpan{
			project: pid, reason: reason, key: key, fullName: c.RepoFullName,
			from: c.CommittedAt.Add(-clusterLeadIn).UnixMicro(), to: c.CommittedAt.UnixMicro(),
		})
	}
	for _, run := range runs {
		pid, reason, key := r.resolve(run.RepoFullName, run.Cwd)
		spans = append(spans, evidenceSpan{
			project: pid, reason: reason, key: key, fullName: run.RepoFullName,
			from: run.StartedAt.UnixMicro(), to: run.EndedAt.UnixMicro(),
		})
	}
	return spans, nil
}

// SessionAttribution is the per-session breakdown: how the session's
// wall-clock divides across projects, and which places nobody claims.
// A running session is read up to now — the breakdown is a view, not a record.
func (d *Domain) SessionAttribution(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) ([]Allocation, []UnresolvedPlace, error) {
	ws, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return nil, nil, err
	}
	end := time.Now().UTC()
	if ws.EndedAt != nil {
		end = *ws.EndedAt
	}
	r, err := d.newResolver(ctx, p.UserID)
	if err != nil {
		return nil, nil, err
	}
	spans, err := d.sessionSpans(ctx, r, ws.ID)
	if err != nil {
		return nil, nil, err
	}
	allocs, unres := apportion(ws.ProjectID, ws.StartedAt, end, spans)
	return allocs, unres, nil
}

// projectName is a display helper for handlers building DTOs from allocations.
func (r *resolver) projectName(id uuid.UUID) string {
	return r.projects[id].Name
}
```

Add `"context"`, `"fmt"`, and `"github.com/cobanov/punchcard/internal/auth"` to the file's imports.

- [ ] **Step 4: Create `internal/http/attribution_handlers.go`**

```go
package http

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// AllocationDTO is one project's share of a session's wall-clock.
type AllocationDTO struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Seconds   int64  `json:"seconds"`
	Evidenced bool   `json:"evidenced" doc:"true when evidence earned these seconds; false for the declared fallback."`
	Reason    string `json:"reason" enum:"linked,name,declared"`
}

// UnresolvedPlaceDTO is evidence whose place no project claims. Seconds here
// are activity, not an allocation — those minutes already followed the
// declaration — and the name exists so a client can offer create/link.
type UnresolvedPlaceDTO struct {
	Place     string `json:"place"`
	FullName  string `json:"full_name,omitempty" doc:"owner/repo when known; absent for remoteless directories."`
	Seconds   int64  `json:"seconds"`
	Ambiguous bool   `json:"ambiguous,omitempty" doc:"true when MORE than one project claims the place."`
}

func (d Deps) registerAttributionRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "sessions-attribution", Method: http.MethodGet, Path: "/v1/sessions/{id}/attribution",
		Summary: "How a session's time divides across projects, by its evidence",
		Tags:    []string{"sessions"}, Errors: []int{401, 404},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" format:"uuid"`
	}) (*struct {
		Body struct {
			Allocations []AllocationDTO      `json:"allocations"`
			Unresolved  []UnresolvedPlaceDTO `json:"unresolved"`
		}
	}, error) {
		p, err := requirePrincipal(ctx)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID)
		if err != nil {
			return nil, err
		}
		allocs, unres, err := d.Domain.SessionAttribution(ctx, p, id)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		r, err := d.Domain.NewResolverForNames(ctx, p.UserID)
		if err != nil {
			return nil, d.problem(ctx, err)
		}
		out := &struct {
			Body struct {
				Allocations []AllocationDTO      `json:"allocations"`
				Unresolved  []UnresolvedPlaceDTO `json:"unresolved"`
			}
		}{}
		out.Body.Allocations = make([]AllocationDTO, 0, len(allocs))
		for _, a := range allocs {
			out.Body.Allocations = append(out.Body.Allocations, AllocationDTO{
				ProjectID: a.ProjectID.String(), Name: r.projectName(a.ProjectID),
				Seconds: a.Seconds, Evidenced: a.Evidenced, Reason: string(a.Reason),
			})
		}
		out.Body.Unresolved = make([]UnresolvedPlaceDTO, 0, len(unres))
		for _, u := range unres {
			out.Body.Unresolved = append(out.Body.Unresolved, UnresolvedPlaceDTO{
				Place: u.Key, FullName: u.FullName, Seconds: u.Seconds, Ambiguous: u.Ambiguous,
			})
		}
		return out, nil
	})
}
```

This calls a resolver twice (once inside SessionAttribution, once for names). Avoid that: instead of `NewResolverForNames`, change `SessionAttribution` to also return names. **Do the simpler thing:** add to `attribution.go`:

```go
// SessionAttributionNamed is SessionAttribution plus project names, so the
// handler does not build a second resolver just to label rows.
type NamedAllocation struct {
	Allocation
	Name string
}

func (d *Domain) SessionAttributionNamed(ctx context.Context, p *auth.Principal, sessionID uuid.UUID) ([]NamedAllocation, []UnresolvedPlace, error) {
	ws, err := d.GetSession(ctx, p, sessionID)
	if err != nil {
		return nil, nil, err
	}
	end := time.Now().UTC()
	if ws.EndedAt != nil {
		end = *ws.EndedAt
	}
	r, err := d.newResolver(ctx, p.UserID)
	if err != nil {
		return nil, nil, err
	}
	spans, err := d.sessionSpans(ctx, r, ws.ID)
	if err != nil {
		return nil, nil, err
	}
	allocs, unres := apportion(ws.ProjectID, ws.StartedAt, end, spans)
	named := make([]NamedAllocation, 0, len(allocs))
	for _, a := range allocs {
		named = append(named, NamedAllocation{Allocation: a, Name: r.projectName(a.ProjectID)})
	}
	return named, unres, nil
}
```

…and in the handler use `d.Domain.SessionAttributionNamed(ctx, p, id)`, drop the second resolver block, and use `a.Name` directly. Keep `SessionAttribution` (unnamed) — Task 4 uses the resolver-reuse pattern instead. Delete the `NewResolverForNames` reference entirely; it must not appear in the final code.

- [ ] **Step 5: Register the route**

In `internal/http/routes.go`, after `d.registerAgentRunRoutes(api)` add:

```go
	d.registerAttributionRoutes(api)
```

- [ ] **Step 6: Regenerate OpenAPI, run the tests**

Run:
```bash
gofmt -w internal/ && go build ./... && make openapi
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
go test ./internal/http/ -run 'TestSessionAttribution' -v 2>&1 | grep -E "^(--- |ok|FAIL)"
go test ./internal/service/ -run 'TestApportion|TestResolution|TestPlaceKey' 2>&1 | tail -2
```
Expected: PASS everywhere.

- [ ] **Step 7: Commit**

```bash
git add internal/service/attribution.go internal/http/attribution_handlers.go internal/http/routes.go internal/http/attribution_test.go docs/openapi.json
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Expose per-session attribution

GET /v1/sessions/{id}/attribution answers the question the session row
could not: where did this hour actually go. Allocations partition the
wall-clock — evidenced rows first with the rung that earned them, the
declared fallback last — and unresolved places are listed by name with
their activity, so a client can offer to create or link the project the
evidence keeps mentioning. A running session reads up to now: the
breakdown is a view, not a record."
```

---

### Task 3: Recovery learns links

**Files:**
- Modify: `internal/service/unmatched.go` (end of `SessionFromCluster`)
- Test: append to `internal/http/attribution_test.go`

**Interfaces:**
- Consumes: existing `d.store.ListAgentRunsForSession`, `d.store.LinkProjectRepo(ctx, db.LinkProjectRepoParams{ID, ProjectID, FullName})` (ON CONFLICT DO NOTHING — a conflict surfaces as `isNoRows`), the `commits` slice already in scope in `SessionFromCluster`.
- Produces: recovery writes a `project_repos` row when the recovered window's evidence names exactly one repository.

- [ ] **Step 1: Write the failing test (append to `internal/http/attribution_test.go`)**

```go
// Recording a cluster into a project teaches the link — that is how the link
// table stops being sparse without anyone doing setup. But only when the
// window names exactly one repository: recovering a mixed morning into a
// catch-all project must not teach the catch-all to claim every repo in it.
func TestRecoveryLearnsTheLinkOnlyWhenUnambiguous(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-learn@example.com")
	shopID := newProject(t, c, base, csrf, "webshop")
	junkID := newProject(t, c, base, csrf, "junk")

	// One orphan run in one repo → recover into webshop → link learned.
	a := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Minute)
	must(t, "run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-1", a, a.Add(20*time.Minute),
			map[string]any{"repo": "cobanov/shop-backend"}), csrf), http.StatusAccepted)
	must(t, "recover", st(t, c, http.MethodPost, base+"/v1/github/unmatched/recover",
		map[string]any{"project_id": shopID, "from": a.Format(time.RFC3339),
			"to": a.Add(25 * time.Minute).Format(time.RFC3339), "note": "shop work"}, csrf),
		http.StatusCreated)

	code, raw := do(t, c, http.MethodGet, base+"/v1/projects/"+shopID+"/repos", nil, nil)
	must(t, "repos", code, http.StatusOK)
	if !strings.Contains(string(raw), "cobanov/shop-backend") {
		t.Fatalf("recovery did not learn the link: %s", raw)
	}

	// Two repos in the window → recover into junk → nothing learned.
	b := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	must(t, "run1", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-2a", b, b.Add(10*time.Minute),
			map[string]any{"repo": "cobanov/one"}), csrf), http.StatusAccepted)
	must(t, "run2", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "learn-2b", b.Add(2*time.Minute), b.Add(12*time.Minute),
			map[string]any{"repo": "cobanov/two"}), csrf), http.StatusAccepted)
	must(t, "recover mixed", st(t, c, http.MethodPost, base+"/v1/github/unmatched/recover",
		map[string]any{"project_id": junkID, "from": b.Format(time.RFC3339),
			"to": b.Add(15 * time.Minute).Format(time.RFC3339), "note": "mixed"}, csrf),
		http.StatusCreated)

	code, raw = do(t, c, http.MethodGet, base+"/v1/projects/"+junkID+"/repos", nil, nil)
	must(t, "junk repos", code, http.StatusOK)
	if strings.Contains(string(raw), "cobanov/one") || strings.Contains(string(raw), "cobanov/two") {
		t.Fatalf("a mixed recovery taught links it should not have: %s", raw)
	}
}
```

Add `"strings"` to the test file's imports if absent.

- [ ] **Step 2: Run to verify failure**

Run: `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock && go test ./internal/http/ -run 'TestRecoveryLearns' 2>&1 | tail -5`
Expected: FAIL at "recovery did not learn the link".

- [ ] **Step 3: Implement in `internal/service/unmatched.go`**

In `SessionFromCluster`, replace the tail:

```go
	// StopSession already reconciled; this covers the case where the recovered
	// window swallowed runs that a neighbouring session used to hold.
	if err := d.ReconcileAgentRuns(ctx, p.UserID); err != nil {
		return stopped, err
	}
	return stopped, nil
```

with:

```go
	// StopSession already reconciled; this covers the case where the recovered
	// window swallowed runs that a neighbouring session used to hold.
	if err := d.ReconcileAgentRuns(ctx, p.UserID); err != nil {
		return stopped, err
	}
	d.learnClusterLink(ctx, in.ProjectID, stopped.ID, commits)
	return stopped, nil
```

and append to the same file:

```go
// learnClusterLink links the recovered window's repository to the chosen
// project — but only when the window names exactly one. Recording is the
// moment the user explicitly connects evidence to a project, which is how the
// link table grows without a setup step; the one-repo guard exists because
// recovering a mixed morning into a catch-all must not teach the catch-all to
// claim every repository in it. Best-effort: a failed lesson never fails the
// recovery that taught it.
func (d *Domain) learnClusterLink(ctx context.Context, projectID, sessionID uuid.UUID, commits []db.Commit) {
	repos := map[string]bool{}
	for _, c := range commits {
		if c.RepoFullName != "" {
			repos[c.RepoFullName] = true
		}
	}
	runs, err := d.store.ListAgentRunsForSession(ctx, sessionID)
	if err != nil {
		d.log.WarnContext(ctx, "could not list runs for link learning", "error", err.Error())
		return
	}
	for _, r := range runs {
		if r.RepoFullName != "" {
			repos[r.RepoFullName] = true
		}
	}
	if len(repos) != 1 {
		return
	}
	var full string
	for name := range repos {
		full = name
	}
	id, err := uuid.NewV7()
	if err != nil {
		return
	}
	if _, err := d.store.LinkProjectRepo(ctx, db.LinkProjectRepoParams{
		ID: id, ProjectID: projectID, FullName: full,
	}); err != nil && !isNoRows(err) { // conflict = already linked, which is fine
		d.log.WarnContext(ctx, "could not learn project link", "repo", full, "error", err.Error())
	}
}
```

Check the actual logger field name on `Domain` (`d.log` — confirm against `internal/service/domain.go`; if it is named differently, e.g. `d.logger`, use that name in both places).

- [ ] **Step 4: Run tests, then commit**

```bash
gofmt -w internal/service/ && go build ./...
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
go test ./internal/http/ -run 'TestRecoveryLearns|TestSessionAttribution' 2>&1 | tail -3
git add internal/service/unmatched.go internal/http/attribution_test.go
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Recovery teaches the project its repository

Recording a cluster into a project is the one moment the user explicitly
connects evidence to a project, so the link is written then — that is how
the link table stops being three-of-nine sparse without a setup step. Only
when the window names exactly one repository: recovering a mixed morning
into a catch-all must not teach the catch-all to claim every repo in it.
Best-effort, and a conflict just means the lesson was already known."
```

---

### Task 4: Evidenced summary + `?attribution=` on reports

**Files:**
- Modify: `internal/service/attribution.go` (append `SummaryByProjectEvidenced`)
- Modify: `internal/http/report_handlers.go` (parameter + dispatch)
- Test: append to `internal/http/attribution_test.go`

**Interfaces:**
- Consumes: `d.ListSessions(ctx, p, from, to, nil)` (call exactly as `ExportCSV` does), Task 1/2 core, existing `ProjectTotal` + `amountCents` (`internal/service/reports.go`), `p.AllowsProject`.
- Produces: `func (d *Domain) SummaryByProjectEvidenced(ctx context.Context, p *auth.Principal, from, to time.Time) ([]ProjectTotal, error)`; HTTP query param `attribution` enum `declared,evidence` default `declared` on `reports-summary`.

- [ ] **Step 1: Write the failing test (append to `internal/http/attribution_test.go`)**

```go
// The two report modes agree on the grand total — the sweep only moves
// seconds between projects — and evidence mode bills moved minutes at the
// TARGET project's rate, which is the point of moving them.
func TestEvidencedSummaryRedistributesWithoutChangingTheTotal(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-report@example.com")
	alphaID := newProject(t, c, base, csrf, "alpha")
	betaID := newProject(t, c, base, csrf, "beta")
	// beta bills at 100¢ a second; alpha has no rate.
	must(t, "rate", st(t, c, http.MethodPatch, base+"/v1/projects/"+betaID,
		map[string]any{"hourly_rate_cents": 360000}, csrf), http.StatusOK)

	start := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Minute)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": alphaID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)
	must(t, "run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "rep-1", start.Add(10*time.Minute), start.Add(40*time.Minute),
			map[string]any{"repo": "cobanov/beta"}), csrf), http.StatusAccepted)

	q := "?from=" + start.Add(-5*time.Minute).Format(time.RFC3339) +
		"&to=" + end.Add(5*time.Minute).Format(time.RFC3339) + "&group_by=project"

	type summary struct {
		Projects []struct {
			Name        string `json:"name"`
			Seconds     int64  `json:"seconds"`
			AmountCents *int64 `json:"amount_cents"`
		} `json:"projects"`
	}
	fetch := func(mode string) summary {
		code, raw := do(t, c, http.MethodGet, base+"/v1/reports/summary"+q+"&attribution="+mode, nil, nil)
		must(t, "summary "+mode, code, http.StatusOK)
		var s summary
		unmarshal(t, raw, &s)
		return s
	}

	declared, evidence := fetch("declared"), fetch("evidence")
	total := func(s summary) (sum int64) {
		for _, p := range s.Projects {
			sum += p.Seconds
		}
		return
	}
	if total(declared) != 3600 || total(evidence) != 3600 {
		t.Fatalf("grand totals declared=%d evidence=%d, want 3600 both", total(declared), total(evidence))
	}
	get := func(s summary, name string) (int64, *int64) {
		for _, p := range s.Projects {
			if p.Name == name {
				return p.Seconds, p.AmountCents
			}
		}
		return 0, nil
	}
	dAlpha, _ := get(declared, "alpha")
	if dAlpha != 3600 {
		t.Fatalf("declared alpha = %d, want 3600", dAlpha)
	}
	eAlpha, _ := get(evidence, "alpha")
	eBeta, amount := get(evidence, "beta")
	if eAlpha != 1800 || eBeta != 1800 {
		t.Fatalf("evidence split alpha=%d beta=%d, want 1800/1800", eAlpha, eBeta)
	}
	if amount == nil || *amount != 180000 {
		t.Fatalf("beta amount = %v, want 180000 (its own rate over its evidenced seconds)", amount)
	}
	// Default is declared: an absent parameter must not change anyone's numbers.
	code, raw = do(t, c, http.MethodGet, base+"/v1/reports/summary"+q, nil, nil)
	must(t, "default", code, http.StatusOK)
	var def summary
	unmarshal(t, raw, &def)
	defAlpha, _ := get(def, "alpha")
	if defAlpha != 3600 {
		t.Fatalf("default mode alpha = %d, want the declared 3600", defAlpha)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock && go test ./internal/http/ -run 'TestEvidencedSummary' 2>&1 | tail -5`
Expected: FAIL — huma rejects the unknown `attribution` parameter (422) or the evidence assertions fail.

- [ ] **Step 3: Append to `internal/service/attribution.go`**

```go
// SummaryByProjectEvidenced is SummaryByProject with the sweep deciding where
// each second goes. Same range clipping, same totals — the sweep only moves
// seconds between projects — but a minute the evidence places on beta bills
// at beta's rate, which is the point of moving it.
func (d *Domain) SummaryByProjectEvidenced(ctx context.Context, p *auth.Principal, from, to time.Time) ([]ProjectTotal, error) {
	sessions, err := d.ListSessions(ctx, p, from, to, nil)
	if err != nil {
		return nil, err
	}
	r, err := d.newResolver(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	secs := map[uuid.UUID]int64{}
	for _, ws := range sessions {
		if ws.EndedAt == nil { // parity with the SQL summary: running sessions have no duration yet
			continue
		}
		s, e := ws.StartedAt, *ws.EndedAt
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if !e.After(s) {
			continue
		}
		spans, serr := d.sessionSpans(ctx, r, ws.ID)
		if serr != nil {
			return nil, serr
		}
		allocs, _ := apportion(ws.ProjectID, s, e, spans)
		for _, a := range allocs {
			secs[a.ProjectID] += a.Seconds
		}
	}

	out := make([]ProjectTotal, 0, len(secs))
	for id, seconds := range secs {
		if !p.AllowsProject(id) {
			continue
		}
		proj, ok := r.projects[id]
		if !ok {
			continue
		}
		color := ""
		if proj.Color != nil {
			color = *proj.Color
		}
		out = append(out, ProjectTotal{
			ProjectID: id, Name: proj.Name, Client: proj.Client, Color: color,
			Seconds: seconds, Currency: proj.Currency, Billable: proj.Billable,
			AmountCents: amountCents(seconds, proj.HourlyRateCents, proj.Billable),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
```

- [ ] **Step 4: Add the parameter in `internal/http/report_handlers.go`**

In the `reports-summary` input struct, after the `GroupBy` field, add:

```go
		Attribution string `query:"attribution" enum:"declared,evidence" default:"declared" doc:"How project totals are attributed. 'declared' bills every second to the session's own project. 'evidence' partitions each session across the projects its evidence shows active — quiet minutes still follow the declaration. Day totals are identical in both modes."`
```

Replace:

```go
		totals, terr := d.Domain.SummaryByProject(ctx, p, from, to)
		if terr != nil {
			return nil, d.problem(ctx, terr)
		}
```

with:

```go
		var totals []service.ProjectTotal
		var terr error
		if in.Attribution == "evidence" {
			totals, terr = d.Domain.SummaryByProjectEvidenced(ctx, p, from, to)
		} else {
			totals, terr = d.Domain.SummaryByProject(ctx, p, from, to)
		}
		if terr != nil {
			return nil, d.problem(ctx, terr)
		}
```

Add `"github.com/cobanov/punchcard/internal/service"` to the file's imports if not already present.

- [ ] **Step 5: Regenerate, test, commit**

```bash
gofmt -w internal/ && go build ./... && make openapi
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
go test ./internal/http/ -run 'TestEvidencedSummary|TestSessionAttribution|TestRecoveryLearns|TestEmptyRange' 2>&1 | tail -3
git add internal/service/attribution.go internal/http/report_handlers.go internal/http/attribution_test.go docs/openapi.json
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Report by evidence, with declared as the untouched default

?attribution=evidence partitions each session across the projects its
evidence shows active — quiet minutes still follow the declaration — and
bills moved minutes at the target project's own rate, which is the point
of moving them. The grand total is identical in both modes by
construction: the sweep only moves seconds, never makes them. The API
default stays declared so nobody's numbers change until they opt in."
```

---

### Task 5: CSV export follows the mode

**Files:**
- Modify: `internal/service/reports.go` (`ExportCSV` signature + evidence branch)
- Modify: `internal/http/report_handlers.go` (`reports-export-csv` param + call)
- Test: append to `internal/http/attribution_test.go`

**Interfaces:**
- Consumes: Task 4's service pieces (`newResolver`, `sessionSpans`, `apportion`).
- Produces: `ExportCSV(ctx, p, from, to time.Time, attribution string, w io.Writer)`. CSV gains one trailing column `basis` in both modes (`declared` on every row in declared mode). Evidence mode emits one row per session × allocation; `seconds`/`amount_cents` are the apportioned values at the row project's rate; `commits` stays the session's total commit count on each of its rows (documented in the header comment).

- [ ] **Step 1: Write the failing test (append to `internal/http/attribution_test.go`)**

```go
// Evidence-mode CSV: one row per session × project, apportioned seconds,
// and a basis column so a spreadsheet can tell a declared row from an
// evidenced one.
func TestCSVFollowsTheAttributionMode(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "attr-csv@example.com")
	alphaID := newProject(t, c, base, csrf, "alpha")
	_ = newProject(t, c, base, csrf, "beta")

	start := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Minute)
	end := start.Add(time.Hour)
	code, raw := do(t, c, http.MethodPost, base+"/v1/sessions",
		map[string]any{"project_id": alphaID, "started_at": start.Format(time.RFC3339)}, csrf)
	must(t, "start", code, http.StatusCreated)
	var ws struct {
		ID string `json:"id"`
	}
	unmarshal(t, raw, &ws)
	must(t, "stop", st(t, c, http.MethodPost, base+"/v1/sessions/"+ws.ID+"/stop",
		map[string]any{"at": end.Format(time.RFC3339)}, csrf), http.StatusOK)
	must(t, "run", st(t, c, http.MethodPost, base+"/v1/agent-runs",
		runBody("claude-code", "csv-1", start.Add(10*time.Minute), start.Add(40*time.Minute),
			map[string]any{"repo": "cobanov/beta"}), csrf), http.StatusAccepted)

	q := "?from=" + start.Add(-time.Minute).Format(time.RFC3339) +
		"&to=" + end.Add(time.Minute).Format(time.RFC3339)

	code, raw = do(t, c, http.MethodGet, base+"/v1/reports/export.csv"+q+"&attribution=evidence", nil, nil)
	must(t, "csv", code, http.StatusOK)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if !strings.HasSuffix(lines[0], ",basis") {
		t.Fatalf("header missing basis column: %s", lines[0])
	}
	if len(lines) != 3 { // header + alpha row + beta row
		t.Fatalf("want 2 data rows, got %d: %v", len(lines)-1, lines)
	}
	var alphaSecs, betaSecs int64
	for _, line := range lines[1:] {
		f := strings.Split(line, ",")
		// columns: session_id,project,client,note,started_at,ended_at,seconds,...
		n, _ := strconv.ParseInt(f[6], 10, 64)
		switch f[1] {
		case "alpha":
			alphaSecs = n
		case "beta":
			betaSecs = n
		}
	}
	if alphaSecs != 1800 || betaSecs != 1800 {
		t.Fatalf("csv split alpha=%d beta=%d, want 1800/1800", alphaSecs, betaSecs)
	}
}
```

Add `"strconv"` to the test file's imports.

- [ ] **Step 2: Run to verify failure**

Run: `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock && go test ./internal/http/ -run 'TestCSVFollows' 2>&1 | tail -5`
Expected: FAIL (unknown parameter or missing basis column).

- [ ] **Step 3: Modify `ExportCSV` in `internal/service/reports.go`**

Change the signature to `func (d *Domain) ExportCSV(ctx context.Context, p *auth.Principal, from, to time.Time, attribution string, w io.Writer) error`. Change the header write to append `"basis"`:

```go
	if err := cw.Write([]string{
		"session_id", "project", "client", "note", "started_at", "ended_at",
		"seconds", "amount_cents", "currency", "commits", "source", "basis",
	}); err != nil {
		return err
	}
```

Then wrap the existing per-session loop body. Build a resolver once before the loop when needed:

```go
	var res *resolver
	if attribution == "evidence" {
		if res, err = d.newResolver(ctx, p.UserID); err != nil {
			return err
		}
	}
```

Inside the loop, after the `ws.EndedAt == nil` skip, branch:

```go
		if attribution != "evidence" {
			proj := byID[ws.ProjectID]
			seconds := int64(ws.EndedAt.Sub(ws.StartedAt).Seconds())
			amount := ""
			if a := amountCents(seconds, proj.HourlyRateCents, proj.Billable); a != nil {
				amount = strconv.FormatInt(*a, 10)
			}
			if err := cw.Write([]string{
				ws.ID.String(), proj.Name, proj.Client, ws.Note,
				ws.StartedAt.In(loc).Format(time.RFC3339),
				ws.EndedAt.In(loc).Format(time.RFC3339),
				strconv.FormatInt(seconds, 10), amount, proj.Currency,
				strconv.FormatInt(counts[ws.ID], 10), ws.Source, "declared",
			}); err != nil {
				return err
			}
			continue
		}

		// Evidence mode: one row per allocation, apportioned seconds billed at
		// the row project's own rate. The commits column stays the session's
		// total on each of its rows — it counts evidence, not time.
		spans, serr := d.sessionSpans(ctx, res, ws.ID)
		if serr != nil {
			return serr
		}
		allocs, _ := apportion(ws.ProjectID, ws.StartedAt, *ws.EndedAt, spans)
		for _, a := range allocs {
			proj, ok := res.projects[a.ProjectID]
			if !ok {
				continue
			}
			amount := ""
			if amt := amountCents(a.Seconds, proj.HourlyRateCents, proj.Billable); amt != nil {
				amount = strconv.FormatInt(*amt, 10)
			}
			basis := "declared"
			if a.Evidenced {
				basis = "evidence"
			}
			if err := cw.Write([]string{
				ws.ID.String(), proj.Name, proj.Client, ws.Note,
				ws.StartedAt.In(loc).Format(time.RFC3339),
				ws.EndedAt.In(loc).Format(time.RFC3339),
				strconv.FormatInt(a.Seconds, 10), amount, proj.Currency,
				strconv.FormatInt(counts[ws.ID], 10), ws.Source, basis,
			}); err != nil {
				return err
			}
		}
```

(The old unconditional row-writing block is fully replaced by this branch; nothing of it remains outside.)

- [ ] **Step 4: Update the CSV handler in `internal/http/report_handlers.go`**

Add to the `reports-export-csv` input struct:

```go
		Attribution string `query:"attribution" enum:"declared,evidence" default:"declared"`
```

and change the call to `d.Domain.ExportCSV(hctx.Context(), p, from, to, in.Attribution, hctx.BodyWriter())`.

- [ ] **Step 5: Regenerate, run all report tests, commit**

```bash
gofmt -w internal/ && go build ./... && make openapi
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
go test ./internal/http/ 2>&1 | tail -3
git add internal/service/reports.go internal/http/report_handlers.go internal/http/attribution_test.go docs/openapi.json
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "CSV export follows the attribution mode

Evidence mode emits one row per session and project with the apportioned
seconds billed at the row project's own rate, and both modes gain a basis
column so a spreadsheet can tell a declared row from an evidenced one.
The export is the freezing mechanism for a report that is otherwise a
living view, so it has to be able to say everything the view can."
```

---

### Task 6: Web — session detail attribution block + row drift dot

**Files:**
- Modify: `web/src/lib/api.ts` (types + `sessionAttribution`)
- Modify: `web/src/components/SessionList.tsx` (replace `AgentRunList` with `AttributionBlock`; row dot)
- Modify: `web/src/App.tsx` (fetch attribution per session alongside commits; pass down)

**Interfaces:**
- Consumes: `GET /v1/sessions/{id}/attribution` (Task 2), existing `api.createProject`, `api.linkRepo`, `total()` from `lib/format`.
- Produces: `api.sessionAttribution(id): Promise<SessionAttributionT>`; `SessionList` accepts `attribution: Record<string, SessionAttributionT>` and `onChanged: () => void`.

- [ ] **Step 1: Add types and the fetch to `web/src/lib/api.ts`** (next to `agentRuns`)

```ts
export interface SessionAllocation {
  project_id: string;
  name: string;
  seconds: number;
  evidenced: boolean;
  reason: "linked" | "name" | "declared";
}

export interface UnresolvedPlace {
  place: string;
  full_name?: string;
  seconds: number;
  ambiguous?: boolean;
}

export interface SessionAttributionT {
  allocations: SessionAllocation[];
  unresolved: UnresolvedPlace[];
}
```

```ts
  /** How a session's time divides across projects, by its evidence. */
  sessionAttribution: (sessionID: string) =>
    call<SessionAttributionT>(`/v1/sessions/${sessionID}/attribution`).then((r) => ({
      allocations: r.allocations ?? [],
      unresolved: r.unresolved ?? [],
    })),
```

- [ ] **Step 2: Fetch per session in `web/src/App.tsx`**

Find the day-load block that fetches commits per session (`.map(async (s) => [s.id, await api.commits(s.id).catch(() => [])] as const)`). Add a parallel state and fetch with the identical shape:

```ts
const [attribution, setAttribution] = useState<Record<string, SessionAttributionT>>({});
```

```ts
      const attrEntries = await Promise.all(
        (day.sessions ?? [])
          .filter((s) => !s.running)
          .map(async (s) =>
            [s.id, await api.sessionAttribution(s.id).catch(() => ({ allocations: [], unresolved: [] }))] as const,
          ),
      );
      setAttribution(Object.fromEntries(attrEntries));
```

(Adapt the exact iteration variable to match the surrounding commit-fetch code — mirror it precisely.) Import the `SessionAttributionT` type. Pass `attribution={attribution}` and `onChanged={reloadDay}` to `<SessionList …>`, where `reloadDay` is whatever function the surrounding code uses to refresh the day's data (the same one `act()` ends up calling — read the file and reuse it; do not invent a second reload path).

- [ ] **Step 3: Replace `AgentRunList` in `web/src/components/SessionList.tsx`**

Delete the `AgentRunList` component and its call site entirely. In its place (same position in the expanded row), render `<AttributionBlock session={session} attribution={attribution[session.id]} onChanged={onChanged} />`. Thread the two new props through `SessionList`'s and the row's prop types. Add the component:

```tsx
/**
 * How this session's hour actually divides, and the places nobody claims.
 *
 * Replaces the per-repo agent-run summary: same information, upgraded from
 * string-grouping to real resolution, with the reason each project earned its
 * minutes. Quiet and dim on purpose — a commit is proof, a derived minute is
 * a reading of proof — and never amber.
 */
function AttributionBlock({
  session,
  attribution,
  onChanged,
}: {
  session: Session;
  attribution?: SessionAttributionT;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  if (!attribution) return null;
  const { allocations, unresolved } = attribution;
  const interesting =
    allocations.some((a) => a.evidenced) || unresolved.length > 0;
  if (!interesting) return null;

  const reasonLabel = (a: SessionAllocation) =>
    a.reason === "linked" ? "linked" : a.reason === "name" ? "name match" : "declared, quiet";

  const createFrom = async (u: UnresolvedPlace) => {
    setBusy(true);
    try {
      const p = await api.createProject({ name: u.place });
      if (u.full_name) await api.linkRepo(p.id, u.full_name).catch(() => {});
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="pt-1.5">
      <p className="eyebrow mb-1">Time by evidence</p>
      <ul className="space-y-1">
        {allocations.map((a) => (
          <li key={`${a.project_id}-${a.evidenced}`} className="flex items-baseline gap-2.5 t-caption text-faint">
            <span className={a.project_id === session.project_id ? "text-dim" : "font-medium text-dim"}>
              {a.name}
            </span>
            <span className="tnum shrink-0 text-dim">{total(a.seconds)}</span>
            <span className="shrink-0">{reasonLabel(a)}</span>
          </li>
        ))}
        {unresolved.map((u) => (
          <li key={u.place} className="flex flex-wrap items-baseline gap-2.5 t-caption text-faint">
            <span className="font-mono">{u.place}</span>
            <span className="tnum shrink-0">{total(u.seconds)}</span>
            <span className="shrink-0">{u.ambiguous ? "two projects claim this" : "no project"}</span>
            {!u.ambiguous && (
              <button onClick={() => void createFrom(u)} disabled={busy} className="btn-bare">
                Create “{u.place}”
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
```

Update imports: `SessionAttributionT`, `SessionAllocation`, `UnresolvedPlace` from `../lib/api`. Remove the now-unused `AgentRun` import if nothing else uses it.

- [ ] **Step 4: The row drift dot**

In the session row (inside the `tbl-row` button, right after the project-name span), add:

```tsx
              {drifts(attribution[session.id], session.project_id) && (
                <span
                  className="c-project -ml-2 shrink-0 t-caption text-faint"
                  title="Evidence points to other projects — open the row"
                  aria-hidden
                >
                  •
                </span>
              )}
```

Wait — two children cannot share the `c-project` grid area cleanly; instead put the dot INSIDE the existing project-name span, after the text:

```tsx
              <span className="c-project truncate font-medium">
                {projectName(session.project_id)}
                {drifts(attribution[session.id], session.project_id) && (
                  <span className="ml-1.5 t-caption text-faint" title="Evidence points to other projects">•</span>
                )}
              </span>
```

Use this inner-span version; do not add a second grid child. And the helper at file scope:

```ts
/** True when any evidenced minute resolved away from the declared project. */
function drifts(a: SessionAttributionT | undefined, declaredProjectID: string): boolean {
  return !!a?.allocations.some((x) => x.evidenced && x.project_id !== declaredProjectID);
}
```

- [ ] **Step 5: Typecheck, build, commit**

```bash
cd web && npx tsc -b --noEmit && cd ..
make web
git add web/ internal/http/webui/dist/
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Show where a session's hour actually went

The expanded row's per-repo agent summary becomes a real attribution
block: each project's share of the wall-clock with the reason it earned
it, and the places nobody claims listed by name with a one-press create.
A faint dot on the row marks sessions whose evidence points away from
their declaration — informational, not a task queue; ignoring it is a
valid resolution. Never amber: amber keeps meaning running time and
commit proof."
```

---

### Task 7: Web — Analytics attribution switch

**Files:**
- Modify: `web/src/lib/api.ts` (`summary` gains a mode argument)
- Modify: `web/src/components/Analytics.tsx` (mode state, switch UI, caption, CSV link)

**Interfaces:**
- Consumes: `?attribution=` (Task 4/5).
- Produces: `api.summary(from, to, attribution?: "declared" | "evidence")`.

- [ ] **Step 1: Extend `api.summary`**

```ts
  /** Per-project totals. The list is normalised to an array here so no screen
   *  further in has to defend against a range with nothing in it. */
  summary: (from: Date, to: Date, attribution: "declared" | "evidence" = "declared") =>
    call<{ projects?: ProjectTotal[]; timezone: string }>(
      `/v1/reports/summary?${range(from, to)}&group_by=project&attribution=${attribution}`,
    ).then((r) => ({ ...r, projects: r.projects ?? [] })),
```

- [ ] **Step 2: Mode state in `Analytics.tsx`**

In the `Analytics` component add state and thread it through `load`:

```ts
  // "evidence" is the default here even though the API defaults to
  // "declared": the API keeps old clients' numbers stable, the app shows the
  // truthful split. The switch exists for anyone who wants the timer's story.
  const [mode, setMode] = useState<"evidence" | "declared">("evidence");
```

Change `load = useCallback(async (r: Range) => {…}` to `load = useCallback(async (r: Range, m: "evidence" | "declared") => {…}` and inside it pass `m` to both `api.summary(r.from, r.to, m)` and `api.summary(prevFrom, r.from, m)` (the `summaryDays` call is untouched — day totals are mode-invariant). Update the `useEffect` that calls `load` to depend on `mode` and pass it, and every other `load(range)` call site to `load(range, mode)`.

- [ ] **Step 3: The switch and the caption**

In the header row that renders `<RangePicker …/>`, wrap it so the switch sits at the far end:

```tsx
      <div className="flex flex-wrap items-center justify-between gap-2">
        <RangePicker timezone={timezone} range={range} onChange={setRange} />
        <div className="flex gap-0.5 rounded-lg border border-line bg-card p-0.5" role="group" aria-label="Attribution">
          {(["evidence", "declared"] as const).map((m) => (
            <button
              key={m}
              onClick={() => setMode(m)}
              aria-pressed={mode === m}
              className={
                mode === m
                  ? "rounded-md bg-raise px-2.5 py-1 text-text"
                  : "rounded-md px-2.5 py-1 text-dim transition-colors hover:text-text"
              }
            >
              {m === "evidence" ? "By evidence" : "As declared"}
            </button>
          ))}
        </div>
      </div>
```

Under the `Breakdown` component (or in the footer line next to "days cut in"), add the method caption:

```tsx
        <span className="t-caption text-faint">
          {mode === "evidence"
            ? "each minute goes to the project whose evidence was active; shared minutes split evenly; quiet minutes follow the timer"
            : "every minute follows the timer's declared project"}
        </span>
```

Append `&attribution=${mode}` to the Download CSV link's href.

- [ ] **Step 4: Typecheck, build, commit**

```bash
cd web && npx tsc -b --noEmit && cd ..
make web
git add web/ internal/http/webui/dist/
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Analytics reports by evidence, with the declared view a switch away

The web app defaults to the truthful split while the API default stays
declared, so nothing changes for a client that has not asked. The one-line
caption says exactly how each mode counts, because a number whose method
is a secret is the thing this whole feature exists to end. Day totals
carry no switch — the sweep only moves seconds between projects, so days
are identical in both modes."
```

---

### Task 8: Docs, full gate, deploy

**Files:**
- Modify: `CLAUDE.md` (attribution section)
- Modify: `docs/superpowers/specs/2026-08-25-project-attribution-design.md` (status line)

- [ ] **Step 1: Amend `CLAUDE.md`**

Find the invariants section discussing attribution/time (search for "Attribution is decided by TIME" or the "Linking a repository to a project is optional" heading) and add beneath it:

```markdown
### Time decides containment; evidence decides labeling

`SessionCovering` and the run reconciler decide WHICH SESSION holds a piece of
evidence, by time alone — unchanged, and still resting on the one-open-session
index. Which PROJECT a report bills a minute to is a separate, derived answer:
`internal/service/attribution.go` resolves each piece of evidence through a
ladder (exact repo link → link by last path segment → project of the same
name → nobody) and partitions the session's wall-clock accordingly at read
time. The declaration is the fallback for quiet and unclaimed minutes and is
never overwritten. `?attribution=declared` is the API default; the web app
sends `evidence`.

Do not "fix" a mis-labeled report by rewriting session rows. Link the place or
rename the project — reports are living views over the current project set,
and the CSV export is the freezing mechanism. And keep the sweep exact: it
works in microseconds because session boundaries carry deliberate microsecond
nudges, and its allocations must sum to the clipped duration to the second.
```

- [ ] **Step 2: Flip the design doc status**

In `docs/superpowers/specs/2026-08-25-project-attribution-design.md` change the `**Status:**` line to:

```markdown
**Status:** approved by Cobanov 2026-08-25; phases 1–2 implemented (see
`docs/superpowers/plans/2026-08-25-project-attribution.md`). Phase 3 (places,
strip tinting) not yet started.
```

- [ ] **Step 3: Full gate**

```bash
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
make check 2>&1 | tail -4
```
Expected: `all checks passed`, with `internal/http` taking ~30s+ and `internal/service` ~90s+ (sub-second means the DB tests silently skipped — fix DOCKER_HOST and rerun).

- [ ] **Step 4: Commit docs, deploy, verify**

```bash
git add CLAUDE.md docs/
git -c user.name="Mert Cobanov" -c user.email="mertcobanov@gmail.com" commit -m "Record the attribution rules where the next session will look

CLAUDE.md now separates the two questions the old trap conflated: time
decides which session holds evidence, evidence decides which project a
report bills a minute to, and the declaration is the fallback that is
never overwritten."
./scratchpad/deploy.sh v1.17.0 2>&1 | tail -2
curl -s https://punchcard.cobanov.run/app | grep -o 'assets/app-[^"]*\.js'
```

Verify the served hash matches the `make web` output from Task 7. Then verify live behaviour end to end (the user's real data is the acceptance fixture): fetch one mixed session's `/v1/sessions/{id}/attribution` and `/v1/reports/summary?...&attribution=evidence` with the browser session, and confirm the 08:57 "General" session reports punchcard/herdrchat splits per the design's validation section. Push: `git push origin main`.

---

## Self-review notes (already applied)

- **Spec coverage:** ladder ✓ (T1), sweep + properties ✓ (T1), session endpoint + reasons + unresolved ✓ (T2), recovery learning with the one-repo guard ✓ (T3), `?attribution=` + target-rate billing + equal-totals ✓ (T4), CSV rows + basis ✓ (T5), detail block + row dot + create-from-place ✓ (T6), Analytics switch + caption + CSV link ✓ (T7), CLAUDE.md amendment + design status ✓ (T8). Phase 3 (places table, dir links, strip tinting) is deliberately out — the design marks it a separate phase.
- **Known judgment call carried from the design:** ambiguous places surface with no resolve-in-place control beyond what linking/renaming already offers; the block's copy says "two projects claim this" and the fix is the project editor. Acceptable for phase 1–2.
- **Type consistency:** `attribution` param string everywhere; `SessionAttributionT` naming consistent between api.ts and components; `ExportCSV` new signature has exactly one call site (T5 updates it).
- **Placeholder scan:** clean — every step carries its code; the one intentionally-deleted symbol (`NewResolverForNames`) is explicitly marked "must not appear in the final code".

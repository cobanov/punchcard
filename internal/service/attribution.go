package service

// Attribution: which project each minute of a session belongs to.
//
// A session bundles two assertions — "I was working from A to B" (a time claim
// the timer genuinely knows) and "…on project P" (a label the user guessed once
// at Start). The bug this file exists to fix is treating the label as true for
// every second of the claim. Records keep the claim; everything here derives
// labels at read time from the evidence inside the session, falling back to the
// declaration for the minutes nothing vouches for. Nothing is stored: per-user
// data is small enough that freshness beats materialization, and a project
// rename re-labels history at the next read.

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
		ids := r.byFullName[strings.ToLower(strings.TrimSpace(repo))]
		if len(ids) == 1 {
			return ids[0], reasonLinked, key
		}
		if len(ids) > 1 {
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

// projectName is a display helper for handlers building DTOs from allocations.
func (r *resolver) projectName(id uuid.UUID) string { return r.projects[id].Name }

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
// The declared project may appear twice: once for the minutes its own evidence
// earned (Evidenced) and once for the quiet fallback minutes.
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
		if b <= lo || a >= hi {
			continue
		}
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

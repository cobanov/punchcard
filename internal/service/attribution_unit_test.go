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

	if id, reason, _ := r.resolve("cobanov/alpha", ""); id != beta.ID || reason != reasonLinked {
		t.Fatalf("exact link: got %v %v, want beta/linked", id, reason)
	}
	if id, reason, _ := r.resolve("", "/w/alpha"); id != beta.ID || reason != reasonLinked {
		t.Fatalf("key link: got %v %v, want beta/linked", id, reason)
	}
	if id, reason, _ := r.resolve("cobanov/Beta", ""); id != beta.ID || reason != reasonName {
		t.Fatalf("name: got %v %v, want beta/name", id, reason)
	}
	if id, reason, key := r.resolve("cobanov/helva", ""); id != uuid.Nil || reason != reasonNoProject || key != "helva" {
		t.Fatalf("no project: got %v %v %q", id, reason, key)
	}
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

	e := s.Add(90 * time.Minute)
	allocs, _ := apportion(projects[0], s, e, nil)
	if len(allocs) != 1 || allocs[0].Seconds != 5400 || allocs[0].Evidenced || allocs[0].Reason != reasonDeclared {
		t.Fatalf("no-evidence = %+v", allocs)
	}
}

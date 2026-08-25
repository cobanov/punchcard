package cli

import (
	"fmt"
	"sort"
	"strings"
)

// resolveProject finds the project a user meant from a partial name.
//
// Typing the whole name every time is the friction a CLI exists to remove, so a
// prefix is enough — but only when it is unambiguous, and an exact name always
// wins. Without that last rule, having both "punchcard" and "punchcard-cli"
// would make the shorter one unreachable.
func resolveProject(projects []Project, query string) (Project, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return Project{}, fmt.Errorf("which project? try: punchcard projects")
	}

	var prefix []Project
	for _, p := range projects {
		name := strings.ToLower(p.Name)
		if name == q {
			return p, nil
		}
		if strings.HasPrefix(name, q) {
			prefix = append(prefix, p)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], nil
	case 0:
		// Fall back to a substring match before giving up: "sarsiv" should find
		// capsarsiv, because that is how people remember names.
		var contains []Project
		for _, p := range projects {
			if strings.Contains(strings.ToLower(p.Name), q) {
				contains = append(contains, p)
			}
		}
		if len(contains) == 1 {
			return contains[0], nil
		}
		if len(contains) > 1 {
			return Project{}, ambiguous(q, contains)
		}
		return Project{}, fmt.Errorf("no project matches %q — try: punchcard projects", query)
	default:
		return Project{}, ambiguous(q, prefix)
	}
}

func ambiguous(query string, candidates []Project) error {
	names := make([]string, 0, len(candidates))
	for _, p := range candidates {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return fmt.Errorf("%q matches %s — be more specific", query, strings.Join(names, ", "))
}

// formatDuration renders elapsed time as a wall clock: this is what a running
// timer shows, and it has to tick visibly. Hours are not wrapped at 24 — a
// timer left running over a weekend should say so, loudly, rather than
// pretending to be a short session.
func formatDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// formatTotal renders a total the way a person says it out loud: "6s 12d"
// (6 saat 12 dakika). Nobody bills in seconds, so they are dropped.
func formatTotal(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%ds %dd", h, m)
	case h > 0:
		return fmt.Sprintf("%ds", h)
	default:
		return fmt.Sprintf("%dd", m)
	}
}

// truncate shortens a string for column output, keeping whole runes.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

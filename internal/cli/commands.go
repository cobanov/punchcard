package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// App carries what every command needs: where to write, and where the config
// lives. Both are fields rather than globals so the commands can be driven from
// a test.
type App struct {
	Out        io.Writer
	Err        io.Writer
	ConfigPath string
	BaseURL    string // overrides the stored one when set (PUNCHCARD_URL)
	JSON       bool
}

// printf and friends write to the app's streams.
//
// The errors are ignored on purpose and this is the one place that says so: a
// failed write to stdout means the pipe closed (`punchcard today | head`), and
// there is nothing useful to do about it — certainly not print a second message
// down the same broken pipe.
func (a *App) printf(format string, args ...any) { _, _ = fmt.Fprintf(a.Out, format, args...) }
func (a *App) println(args ...any)               { _, _ = fmt.Fprintln(a.Out, args...) }
func (a *App) warnf(format string, args ...any)  { _, _ = fmt.Fprintf(a.Err, format, args...) }
func (a *App) warnln(args ...any)                { _, _ = fmt.Fprintln(a.Err, args...) }

// client builds an authenticated client from the stored config.
func (a *App) client() (*Client, Config, error) {
	cfg, err := LoadConfig(a.ConfigPath)
	if err != nil {
		return nil, Config{}, err
	}
	base := cfg.BaseURL
	if a.BaseURL != "" {
		base = a.BaseURL
	}
	return New(base, cfg.Token), cfg, nil
}

// Login signs in and stores the token.
func (a *App) Login(baseURL string) error {
	if baseURL == "" {
		baseURL = a.BaseURL
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	a.printf("Opening your browser to sign in to %s…\n", baseURL)

	token, err := Login(baseURL)
	if err != nil {
		return err
	}
	cfg := Config{BaseURL: baseURL, Token: token}

	// Record who this is, so `status` can say it without another round trip.
	if st, gerr := New(baseURL, token).GitHub(); gerr == nil && st.Connected {
		cfg.Login = st.Login
	}
	if err := SaveConfig(a.ConfigPath, cfg); err != nil {
		return err
	}
	a.printf("Signed in. Token stored in %s\n", a.ConfigPath)
	return nil
}

// Logout forgets the stored token.
func (a *App) Logout() error {
	if err := ClearConfig(a.ConfigPath); err != nil {
		return err
	}
	a.println("Signed out.")
	return nil
}

// Projects lists projects.
func (a *App) Projects() error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	projects, err := c.Projects(false)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(projects)
	}
	if len(projects) == 0 {
		a.println("No projects yet.")
		return nil
	}
	width := 0
	for _, p := range projects {
		if len(p.Name) > width {
			width = len(p.Name)
		}
	}
	for _, p := range projects {
		rate := ""
		if p.Billable && p.HourlyRateCents != nil {
			rate = fmt.Sprintf("%s %s/h", money(*p.HourlyRateCents), p.Currency)
		}
		a.printf("%-*s  %-20s %s\n", width, p.Name, truncate(p.Client, 20), rate)
	}
	return nil
}

// Start opens a timer.
func (a *App) Start(projectQuery, note string) error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	projects, err := c.Projects(false)
	if err != nil {
		return err
	}
	project, err := resolveProject(projects, projectQuery)
	if err != nil {
		return err
	}
	ws, err := c.Start(project.ID, note)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(ws)
	}
	a.printf("▶ %s · %s\n", project.Name, orDash(ws.Note))
	a.printf("  started %s\n", ws.StartedAt.Local().Format("15:04"))
	return nil
}

// Stop closes the running timer.
func (a *App) Stop() error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	current, err := c.Current()
	if errors.Is(err, ErrNoRunningSession) {
		a.println("No timer running.")
		return nil
	}
	if err != nil {
		return err
	}
	ws, err := c.Stop(current.ID)
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(ws)
	}
	name := a.projectName(c, ws.ProjectID)
	a.printf("■ %s · %s\n", name, orDash(ws.Note))
	a.printf("  %s  (%s–%s)\n", formatTotal(ws.Seconds),
		ws.StartedAt.Local().Format("15:04"), ws.EndedAt.Local().Format("15:04"))
	// The scan runs in the background, so saying it was queued is the honest
	// thing: the commits are not there yet and the user should not refresh
	// looking for them.
	a.println("  commits: queued — punchcard today will show them shortly")
	return nil
}

// Status reports what is running.
func (a *App) Status() error {
	c, cfg, err := a.client()
	if err != nil {
		return err
	}
	ws, err := c.Current()
	if errors.Is(err, ErrNoRunningSession) {
		if a.JSON {
			return a.writeJSON(map[string]any{"running": false})
		}
		a.println("No timer running.")
		a.warnAboutGitHub(c)
		return nil
	}
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(ws)
	}
	name := a.projectName(c, ws.ProjectID)
	elapsed := int64(time.Since(ws.StartedAt).Seconds())
	a.printf("%s · %s\n", name, orDash(ws.Note))
	a.printf("%s  (since %s)\n", formatDuration(elapsed), ws.StartedAt.Local().Format("15:04"))
	if cfg.Login != "" {
		a.printf("github: %s\n", cfg.Login)
	}
	a.warnAboutGitHub(c)
	return nil
}

// Today prints the day's records with their commit counts.
func (a *App) Today(days int) error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))
	sessions, err := c.Sessions(from, now.Add(time.Minute))
	if err != nil {
		return err
	}
	if a.JSON {
		return a.writeJSON(sessions)
	}
	if len(sessions) == 0 {
		a.println("Nothing recorded.")
		return nil
	}

	names := a.projectNames(c)
	var total int64
	// The API returns newest first; a day reads forwards.
	for i := len(sessions) - 1; i >= 0; i-- {
		ws := sessions[i]
		span := ws.StartedAt.Local().Format("15:04") + "–"
		if ws.EndedAt != nil {
			span += ws.EndedAt.Local().Format("15:04")
			total += ws.Seconds
		} else {
			span += "…"
		}
		dur := formatTotal(ws.Seconds)
		if ws.Running {
			dur = "running"
		}
		a.printf("%-12s %-14s %-32s %8s\n",
			span, truncate(names[ws.ProjectID], 14), truncate(orDash(ws.Note), 32), dur)

		if commits, cerr := c.Commits(ws.ID); cerr == nil && len(commits) > 0 {
			repos := map[string]bool{}
			for _, cm := range commits {
				repos[cm.Repo] = true
			}
			a.printf("%-12s └ %d commit · %s\n", "", len(commits), strings.Join(keys(repos), ", "))
		}
	}
	a.printf("\n%-12s %-14s %-32s %8s\n", "", "", "total", formatTotal(total))
	return nil
}

// warnAboutGitHub says why commits are not arriving, if they are not.
//
// The scan fails silently by design — a background job cannot interrupt anyone —
// so the CLI is where the user finds out. Without this the integration looks
// like it simply does not work.
func (a *App) warnAboutGitHub(c *Client) {
	st, err := c.GitHub()
	if err != nil {
		return
	}
	switch {
	case !st.Connected:
		a.warnln("github: not connected — commits will not be attached")
	case st.LastError != "":
		a.warnf("github: %s\n", st.LastError)
	}
}

func (a *App) projectNames(c *Client) map[string]string {
	names := map[string]string{}
	projects, err := c.Projects(true)
	if err != nil {
		return names
	}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	return names
}

func (a *App) projectName(c *Client, id string) string {
	if name, ok := a.projectNames(c)[id]; ok {
		return name
	}
	return id
}

func (a *App) writeJSON(v any) error {
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// money renders integer minor units as a decimal string. It formats; it never
// computes — every amount arrives already worked out in integers.
func money(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

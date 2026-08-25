package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/auth"
	"github.com/cobanov/punchcard/internal/events"
	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// defaultProjectName is the project every new account starts with. Without it a
// fresh account cannot start a timer at all — the first thing a user would have
// to do is a piece of setup, before the product has shown them anything.
const defaultProjectName = "General"

// validColors mirrors the CHECK constraint on projects.color. Colours are names
// rather than hex values so a client can resolve them against its own palette;
// see 00002_domain.sql.
var validColors = map[string]bool{
	"red": true, "amber": true, "green": true, "teal": true,
	"blue": true, "violet": true, "pink": true, "slate": true,
}

// repoFullName matches "owner/name" and nothing else — no URLs, no bare names,
// no nesting. Same shape as the CHECK in the migration, kept here too so the
// caller gets a 422 with a reason instead of a constraint violation.
var repoFullName = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// currencyCode is three letters, upper-cased before it reaches the database.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// CreateProjectInput is the payload for creating a project. HourlyRateCents is
// a pointer because "no rate" and "a rate of zero" are different states: the
// first means the project is not costed, the second means it is costed at zero.
type CreateProjectInput struct {
	Name            string
	Client          string
	Color           string
	HourlyRateCents *int64
	Currency        string
	Billable        bool
}

// UpdateProjectInput carries only the fields a caller sent. Every field is a
// pointer so an absent field is left alone rather than overwritten with a zero
// value; ClearColor and ClearRate express "set this back to nothing", which a
// nil pointer cannot.
type UpdateProjectInput struct {
	Name            *string
	Client          *string
	Color           *string
	ClearColor      bool
	HourlyRateCents *int64
	ClearRate       bool
	Currency        *string
	Billable        *bool
}

// CreateProject creates a project owned by the caller.
func (d *Domain) CreateProject(ctx context.Context, p *auth.Principal, in CreateProjectInput) (db.Project, error) {
	if !p.CanWrite() {
		return db.Project{}, ErrInsufficientScope
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 200 {
		return db.Project{}, NewError(422, "validation_failed", "name must be between 1 and 200 characters")
	}
	if in.Color != "" && !validColors[in.Color] {
		return db.Project{}, NewError(422, "validation_failed", "color must be one of red, amber, green, teal, blue, violet, pink, slate")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "TRY"
	}
	if !currencyCode.MatchString(currency) {
		return db.Project{}, NewError(422, "validation_failed", "currency must be a three-letter code")
	}
	if in.HourlyRateCents != nil && *in.HourlyRateCents < 0 {
		return db.Project{}, NewError(422, "validation_failed", "hourly_rate_cents cannot be negative")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.Project{}, fmt.Errorf("new uuid: %w", err)
	}
	var project db.Project
	err = d.store.WithTx(ctx, func(q *db.Queries) error {
		proj, e := q.CreateProject(ctx, db.CreateProjectParams{
			ID: id, OwnerID: p.UserID, Name: name, Client: strings.TrimSpace(in.Client),
			Color: nullableString(in.Color), HourlyRateCents: in.HourlyRateCents,
			Currency: currency, Billable: in.Billable,
		})
		if e != nil {
			return e
		}
		project = proj
		return events.Write(ctx, q, events.TypeProjectCreated, &proj.ID, actorOf(p), projectResource(proj), nil)
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			return db.Project{}, NewError(409, "project_name_taken", "a project with that name already exists")
		}
		return db.Project{}, fmt.Errorf("create project: %w", err)
	}
	return project, nil
}

// createDefaultProjectTx gives a new account something to book time against.
func createDefaultProjectTx(ctx context.Context, q *db.Queries, ownerID uuid.UUID) (db.Project, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return db.Project{}, err
	}
	return q.CreateProject(ctx, db.CreateProjectParams{
		ID: id, OwnerID: ownerID, Name: defaultProjectName, Client: "",
		Currency: "TRY", Billable: true,
	})
}

// ListProjects returns the caller's projects, archived ones only on request.
func (d *Domain) ListProjects(ctx context.Context, p *auth.Principal, includeArchived bool) ([]db.Project, error) {
	rows, err := d.store.ListProjects(ctx, db.ListProjectsParams{
		OwnerID: p.UserID, IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	if !p.TokenScopedToProjects() {
		return rows, nil
	}
	out := rows[:0]
	for _, r := range rows {
		if p.AllowsProject(r.ID) {
			out = append(out, r)
		}
	}
	return out, nil
}

// GetProject returns one of the caller's projects, or ErrNotFound.
func (d *Domain) GetProject(ctx context.Context, p *auth.Principal, projectID uuid.UUID) (db.Project, error) {
	if !p.AllowsProject(projectID) {
		return db.Project{}, ErrNotFound
	}
	proj, err := d.store.GetProjectForUser(ctx, db.GetProjectForUserParams{ID: projectID, OwnerID: p.UserID})
	if err != nil {
		return db.Project{}, mapNotFound(err)
	}
	return proj, nil
}

// UpdateProject applies a partial update.
func (d *Domain) UpdateProject(ctx context.Context, p *auth.Principal, projectID uuid.UUID, in UpdateProjectInput) (db.Project, error) {
	if err := d.authorizeProject(ctx, p, projectID, true); err != nil {
		return db.Project{}, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len([]rune(n)) > 200 {
			return db.Project{}, NewError(422, "validation_failed", "name must be between 1 and 200 characters")
		}
		in.Name = &n
	}
	if in.Color != nil && *in.Color != "" && !validColors[*in.Color] {
		return db.Project{}, NewError(422, "validation_failed", "color must be one of red, amber, green, teal, blue, violet, pink, slate")
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !currencyCode.MatchString(c) {
			return db.Project{}, NewError(422, "validation_failed", "currency must be a three-letter code")
		}
		in.Currency = &c
	}
	if in.HourlyRateCents != nil && *in.HourlyRateCents < 0 {
		return db.Project{}, NewError(422, "validation_failed", "hourly_rate_cents cannot be negative")
	}

	var project db.Project
	err := d.store.WithTx(ctx, func(q *db.Queries) error {
		proj, e := q.UpdateProject(ctx, db.UpdateProjectParams{
			ID: projectID, OwnerID: p.UserID,
			Name: in.Name, Client: in.Client, Color: in.Color, ClearColor: in.ClearColor,
			HourlyRateCents: in.HourlyRateCents, ClearRate: in.ClearRate,
			Currency: in.Currency, Billable: in.Billable,
		})
		if e != nil {
			return e
		}
		project = proj
		return events.Write(ctx, q, events.TypeProjectUpdated, &proj.ID, actorOf(p), projectResource(proj), nil)
	})
	if err != nil {
		if repo.IsUniqueViolation(err) {
			return db.Project{}, NewError(409, "project_name_taken", "a project with that name already exists")
		}
		return db.Project{}, mapNotFound(err)
	}
	return project, nil
}

// DeleteProject archives a project that has recorded sessions and deletes one
// that does not.
//
// The distinction is not a nicety. work_sessions references projects with ON
// DELETE RESTRICT, because deleting a project out from under a month of
// recorded time would silently rewrite every report that covered it. A project
// nobody has booked against is just a mistyped row, and deleting it is what the
// user meant.
func (d *Domain) DeleteProject(ctx context.Context, p *auth.Principal, projectID uuid.UUID) (archived bool, err error) {
	if err := d.authorizeProject(ctx, p, projectID, true); err != nil {
		return false, err
	}
	n, err := d.store.CountSessionsForProject(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("count sessions: %w", err)
	}
	if n > 0 {
		proj, err := d.store.ArchiveProject(ctx, db.ArchiveProjectParams{ID: projectID, OwnerID: p.UserID})
		if err != nil && repo.IsNotFound(err) {
			// Already archived: the caller's intent is satisfied.
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("archive project: %w", err)
		}
		if err := d.emit(ctx, events.TypeProjectArchived, &proj.ID, p, projectResource(proj)); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := d.store.SoftDeleteProject(ctx, db.SoftDeleteProjectParams{ID: projectID, OwnerID: p.UserID}); err != nil {
		return false, fmt.Errorf("delete project: %w", err)
	}
	if err := d.emit(ctx, events.TypeProjectDeleted, &projectID, p, map[string]any{"id": projectID}); err != nil {
		return false, err
	}
	return false, nil
}

// LinkRepo attaches a GitHub repository to a project.
func (d *Domain) LinkRepo(ctx context.Context, p *auth.Principal, projectID uuid.UUID, fullName string) (db.ProjectRepo, error) {
	if err := d.authorizeProject(ctx, p, projectID, true); err != nil {
		return db.ProjectRepo{}, err
	}
	fullName = strings.TrimSpace(fullName)
	if !repoFullName.MatchString(fullName) {
		return db.ProjectRepo{}, NewError(422, "validation_failed", `repository must be in "owner/name" form`)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.ProjectRepo{}, fmt.Errorf("new uuid: %w", err)
	}
	row, err := d.store.LinkProjectRepo(ctx, db.LinkProjectRepoParams{
		ID: id, ProjectID: projectID, FullName: fullName,
	})
	if err != nil {
		if repo.IsNotFound(err) {
			// ON CONFLICT DO NOTHING: already linked, which is the desired state.
			repos, lerr := d.store.ListProjectRepos(ctx, projectID)
			if lerr != nil {
				return db.ProjectRepo{}, fmt.Errorf("list repos: %w", lerr)
			}
			for _, r := range repos {
				if r.FullName == fullName {
					return r, nil
				}
			}
		}
		return db.ProjectRepo{}, fmt.Errorf("link repo: %w", err)
	}
	return row, nil
}

// UnlinkRepo detaches a repository from a project.
func (d *Domain) UnlinkRepo(ctx context.Context, p *auth.Principal, projectID, repoID uuid.UUID) error {
	if err := d.authorizeProject(ctx, p, projectID, true); err != nil {
		return err
	}
	n, err := d.store.DeleteProjectRepo(ctx, db.DeleteProjectRepoParams{ID: repoID, OwnerID: p.UserID})
	if err != nil {
		return fmt.Errorf("unlink repo: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProjectRepos returns the repositories linked to one project.
func (d *Domain) ListProjectRepos(ctx context.Context, p *auth.Principal, projectID uuid.UUID) ([]db.ProjectRepo, error) {
	if err := d.authorizeProject(ctx, p, projectID, false); err != nil {
		return nil, err
	}
	rows, err := d.store.ListProjectRepos(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	return rows, nil
}

// emit writes a single event outside any caller-supplied transaction.
func (d *Domain) emit(ctx context.Context, eventType string, projectID *uuid.UUID, p *auth.Principal, resource any) error {
	return d.store.WithTx(ctx, func(q *db.Queries) error {
		return events.Write(ctx, q, eventType, projectID, actorOf(p), resource, nil)
	})
}

func projectResource(p db.Project) map[string]any {
	return map[string]any{
		"id": p.ID, "name": p.Name, "client": p.Client, "color": p.Color,
		"hourly_rate_cents": p.HourlyRateCents, "currency": p.Currency,
		"billable": p.Billable, "archived_at": p.ArchivedAt,
	}
}

func projectIDParams(projectID, ownerID uuid.UUID) db.GetProjectForUserParams {
	return db.GetProjectForUserParams{ID: projectID, OwnerID: ownerID}
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func mapNotFound(err error) error {
	if repo.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

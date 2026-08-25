package http

import (
	"net/http"
	"testing"
)

// TestProjectIsolation is the tenant-isolation gate for the project plane: no
// caller may read, change or delete another account's project, and every
// refusal must look like absence rather than denial.
func TestProjectIsolation(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()

	alice, _ := registerActor(t, base, "alice@example.com")
	bob, _ := registerActor(t, base, "bob@example.com")

	_, body := do(t, alice, http.MethodPost, base+"/v1/projects",
		map[string]any{"name": "capsarsiv", "currency": "TRY"}, csrf)
	var created struct {
		ID string `json:"id"`
	}
	unmarshal(t, body, &created)
	if created.ID == "" {
		t.Fatalf("no project id in response: %s", body)
	}

	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"read", http.MethodGet, "/v1/projects/" + created.ID, nil},
		{"update", http.MethodPatch, "/v1/projects/" + created.ID, map[string]any{"name": "hijacked"}},
		{"delete", http.MethodDelete, "/v1/projects/" + created.ID, nil},
		{"link repo", http.MethodPost, "/v1/projects/" + created.ID + "/repos", map[string]any{"full_name": "bob/x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, rb := do(t, bob, tc.method, base+tc.path, tc.body, csrf)
			if code != http.StatusNotFound {
				t.Fatalf("%s as another user = %d, want 404 (a 403 would confirm the id exists): %s", tc.name, code, rb)
			}
		})
	}
}

// A new account must be able to start work immediately, which means it needs a
// project without doing setup first.
func TestNewAccountHasADefaultProject(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	c, _ := registerActor(t, base, "fresh@example.com")

	_, body := do(t, c, http.MethodGet, base+"/v1/projects", nil, nil)
	var out struct {
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	unmarshal(t, body, &out)
	if len(out.Projects) != 1 || out.Projects[0].Name != "General" {
		t.Fatalf("want one default project named General, got %+v", out.Projects)
	}
}

// The rate is a nullable integer end to end. A project with no rate must not
// serialize as 0, which a client would render as free work.
func TestUnpricedProjectSerializesRateAsNull(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "rates@example.com")

	_, body := do(t, c, http.MethodPost, base+"/v1/projects",
		map[string]any{"name": "unpriced", "currency": "TRY"}, csrf)
	var out struct {
		HourlyRateCents *int64 `json:"hourly_rate_cents"`
	}
	unmarshal(t, body, &out)
	if out.HourlyRateCents != nil {
		t.Fatalf("hourly_rate_cents = %v, want null", *out.HourlyRateCents)
	}
}

// The timezone drives every report's day boundaries, so it has to be reachable
// from a client. It was not: the column existed and nothing could read or write
// it, which left every account on UTC and every report cutting days in the
// wrong place for anyone outside Greenwich.
func TestTimezoneRoundTripsThroughTheAccount(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	csrf := testCSRF()
	c, _ := registerActor(t, base, "tz@example.com")

	_, body := do(t, c, http.MethodGet, base+"/v1/me", nil, nil)
	var me struct {
		Timezone string `json:"timezone"`
	}
	unmarshal(t, body, &me)
	if me.Timezone != "UTC" {
		t.Fatalf("a new account starts at %q, want UTC", me.Timezone)
	}

	must(t, "set timezone", st(t, c, http.MethodPatch, base+"/v1/me",
		map[string]any{"timezone": "Europe/Istanbul"}, csrf), http.StatusOK)

	_, body = do(t, c, http.MethodGet, base+"/v1/me", nil, nil)
	unmarshal(t, body, &me)
	if me.Timezone != "Europe/Istanbul" {
		t.Fatalf("timezone = %q after setting it", me.Timezone)
	}

	// And the reports have to actually use it, or the setting is decoration.
	_, body = do(t, c, http.MethodGet, base+"/v1/reports/summary", nil, nil)
	var summary struct {
		Timezone string `json:"timezone"`
	}
	unmarshal(t, body, &summary)
	if summary.Timezone != "Europe/Istanbul" {
		t.Fatalf("reports still bucket in %q", summary.Timezone)
	}
}

// A zone this server cannot load must be refused, not accepted and silently
// resolved to UTC at read time — the user would never learn why their days
// looked wrong.
func TestUnknownTimezoneIsRefused(t *testing.T) {
	srv, _ := newAuthTestServer(t)
	base := srv.URL
	c, _ := registerActor(t, base, "badtz@example.com")

	code := st(t, c, http.MethodPatch, base+"/v1/me",
		map[string]any{"timezone": "Mars/Olympus_Mons"}, testCSRF())
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", code)
	}
}

// export_test.go exists only so janitor_test.go can reach this package's
// unexported sweep from outside it. janitor_test.go cannot be `package repo`
// itself: it needs internal/testutil for Postgres provisioning, and testutil
// imports repo (for NewPool/NewMigrator, so it can hand back an
// already-migrated pool) — so a repo-internal test file that also imported
// testutil would close the loop, repo -> testutil -> repo, which the
// compiler refuses with "import cycle not allowed in test". Every other
// package's tests lean on testutil freely; repo's own cannot, because
// testutil is built on top of repo.
//
// This file sidesteps that by staying in package repo but importing nothing
// beyond context, so it carries none of testutil's dependency back into repo
// and the cycle never forms. That is the standard shape of a Go
// export_test.go: the smallest unexported surface a black-box test needs,
// exported only inside the test binary — it is excluded from every non-test
// build the same as any other _test.go file.
package repo

import "context"

// Sweep runs one retention pass synchronously. Its only caller is
// TestSweepPurgesOldActivity (janitor_test.go), which needs the real sweep —
// not a direct call to one job's own query — to prove that job actually got
// added to sweep's list rather than merely working in isolation.
func (j *Janitor) Sweep(ctx context.Context) {
	j.sweep(ctx)
}

# punchcard

Time tracking for developers, with the commits attached.

Start a timer against a project, say what you are doing, stop it. When you stop,
punchcard fetches the commits you pushed during that stretch and attaches them to
the record. So a day's log is not "3 hours, refactor" — it is "3 hours, refactor,
7 commits, these files".

And it works in reverse: commits made while no timer was running are surfaced
back to you as a suggested record, which is how a forgotten timer gets recovered.
The evidence was on GitHub all along.

Free to use — run the hosted instance or self-host the identical single binary.

## Status

**v1 is the backend.** A working, documented API: accounts, projects, timers,
reports and GitHub commit matching, with interactive documentation at `/docs`
and a one-page explanation at `/`. There is no web interface yet; it gets its
own design and its own release.

## Quick start (self-host)

```bash
cp .env.example deploy/.env    # set POSTGRES_PASSWORD, APP_SECRET, DOMAIN
docker compose -f deploy/docker-compose.yml up --build -d
curl -fsS http://localhost/healthz     # {"status":"ok"}
```

Three secrets decide what works:

| Variable | Without it |
|---|---|
| `APP_SECRET` | the service refuses to start |
| `GITHUB_CLIENT_ID` / `_SECRET` | no GitHub sign-in and no commit matching |
| `GITHUB_TOKEN_KEY` | commit matching stays off — tokens are never stored unencrypted |

Generate the last one with `openssl rand -base64 32`.

## Local development

```bash
export DOCKER_HOST=unix:///var/run/docker.sock   # or your Docker socket
make check                                        # the full gate
make run                                          # serve on :8080
```

`make check` runs `go vet`, golangci-lint, gosec, govulncheck, an OpenAPI drift
check, and the race-enabled test suite against a real Postgres started by
testcontainers. **It needs `DOCKER_HOST` exported**; without it the integration
tests skip and the run goes green having tested nothing.

## How it fits together

```
cmd/punchcard        the single binary: serve, migrate
db/migrations        the schema — the only source of truth (sqlc reads it)
db/queries           hand-written SQL; `make sqlc` generates the Go
internal/repo        every database access; nothing else imports pgx
internal/service     the domain: projects, sessions, reports, GitHub
internal/github      the GitHub REST client and the branch-walking scanner
internal/http        transport: huma operations, middleware, SSE
```

Two rules the schema enforces rather than the code:

- **One open session per user.** A partial unique index. Overlapping sessions
  would make a commit belong to two records at once, and every report after that
  would be wrong.
- **One session per commit.** The other half of the same guarantee.

## Origin

punchcard's chassis — identity, the event outbox, webhooks, SSE, observability
and the self-host packaging — is derived from
[helva](https://github.com/cobanov/helva-todo), an agent-native todo service by
the same author. The domain is new; the parts that have nothing to do with
todos or timers were not written twice.

## Licence

MIT.

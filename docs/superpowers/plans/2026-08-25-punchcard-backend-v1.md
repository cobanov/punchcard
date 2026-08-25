# punchcard backend v1 — uygulama planı

> **Ajanlar için:** Bu planı görev görev uygulamak için
> `superpowers:executing-plans` (veya `subagent-driven-development`) kullanın.
> Adımlar `- [ ]` kutucuklarıyla izlenir.

**Hedef:** Geliştirici odaklı zaman takibi API'si — proje, timer, rapor ve
oturumlara otomatik iliştirilen GitHub commit'leri — canlıda `punchcard.cobanov.run`.

**Yaklaşım:** `~/Developer/helva-todo` ağacı mekanik olarak kopyalanır, domain
budanır, migration'lar sıfırdan yazılır, üstüne proje–oturum–commit modeli
kurulur. Kimlik, olay akışı, webhook, gözlemlenebilirlik ve dağıtım katmanları
yeniden yazılmaz.

**Yığın:** Go 1.26 · huma/v2 · chi/v5 · pgx/v5 · sqlc · goose · Postgres 17 ·
testcontainers · Docker Compose.

**Spec:** `docs/superpowers/specs/2026-08-25-punchcard-design.md`

**Durum (2026-08-25):** Görev 1–17 tamamlandı; Görev 18 kısmen — servis
`https://punchcard.cobanov.run` adresinde canlı, uçtan uca doğrulandı (kayıt,
proje, timer, düzeltme, rapor, CSV, SSE). Kalan tek adım GitHub OAuth App'in
oluşturulup gerçek bir hesapla commit eşlemesinin canlıda görülmesi — sahte
sunucuya karşı geçen testler gerçek API'nin davranışını kanıtlamaz.

## Genel kısıtlar

Her görevin gereksinimleri bunları kapsar:

- Modül yolu `github.com/cobanov/punchcard`, binary `cmd/punchcard`.
- Frontend yok. `web/` ağacı, `go:embed` dist, Playwright, Tauri, eklenti —
  hiçbiri taşınmaz.
- Giriş oturumları tablosu **`auth_sessions`**, çalışma oturumları
  **`work_sessions`**. `sessions` adı hiçbir yerde geçmez.
- Tüm zamanlar `timestamptz`, UTC saklanır. Gösterim `users.timezone`'a göre.
- Para `bigint` kuruş. Float ile para hesabı yasak.
- Şema tek kaynak: `db/migrations`. sqlc şemayı oradan okur; elle SQL yazılmaz,
  sorgular `db/queries/*.sql` içine yazılıp `make sqlc` ile üretilir.
- Kapı `make check`. **Koşmadan önce zorunlu:**
  `export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock`
  Bu değişken yoksa entegrasyon testleri sessizce atlanır ve koşu hiçbir şeyi
  test etmemiş halde yeşil görünür. Çıktıda `ok … internal/http` ve
  `ok … internal/service` satırlarını gör.
- Her görev kendi testiyle biter ve kendi commit'ini atar. Commit mesajlarına
  Claude co-author satırı **eklenmez**.
- GitHub Actions kullanılmaz; helva'da olduğu gibi kapı yereldir.

---

## Dosya yapısı

Aşama 1 sonunda ağaç şöyle olur (helva'dan farklı olan satırlar işaretli):

```
cmd/punchcard/main.go              ← helva'dan, yeniden adlandırıldı
db/
  migrations/00001_init.sql        ← YENİ, sıfırdan yazıldı
  migrations/00002_domain.sql      ← YENİ
  migrations/00003_github.sql      ← YENİ
  queries/*.sql                    ← auth'lar taşındı, domain'ler yeni
  embed.go
internal/
  auth/          config/           email/          events/
  observability/ oauth/            ratelimit/      audit/
  repo/          service/          http/           webhooks/
  github/                          ← YENİ paket
  testutil/
deploy/
  docker-compose.yml  Caddyfile  .env.example
docs/
  openapi.json
Makefile  README.md  LICENSE  CLAUDE.md
```

**Yeni paketlerin sorumlulukları**

| Dosya | Sorumluluk |
|---|---|
| `internal/service/projects.go` | Proje CRUD, arşiv, sahiplik kontrolü |
| `internal/service/sessions.go` | Timer yaşam döngüsü, tek açık oturum kuralı |
| `internal/service/sessions_edit.go` | Saat düzeltme, bölme, silme |
| `internal/service/reports.go` | Toplama, tutar hesabı, CSV |
| `internal/service/github_link.go` | Bağlantı, token şifreleme, tarama tetikleme |
| `internal/service/github_scan.go` | Tarama algoritması, commit→oturum bağlama |
| `internal/github/client.go` | GitHub REST istemcisi (repos, branches, commits) |
| `internal/github/scan.go` | Dal döngüsü, tekilleştirme, sayfalama |
| `internal/http/project_handlers.go` | `/v1/projects*` |
| `internal/http/session_handlers.go` | `/v1/sessions*` |
| `internal/http/report_handlers.go` | `/v1/reports*` |
| `internal/http/github_handlers.go` | `/v1/github*` |

---

# Aşama 1 — İskeleti kur (Görev 1–5)

Sonunda: kimlik doğrulaması çalışan, domain'siz, `make check` yeşil bir servis.

### Görev 1: Ağacı kopyala ve modülü yeniden adlandır

**Dosyalar:**
- Oluştur: `~/Developer/punchcard/` altında helva ağacının budanmış kopyası
- Değiştir: `go.mod`, `cmd/punchcard/main.go`

- [ ] **Adım 1: Kopyala, taşınmayacakları dışarıda bırak**

```bash
cd ~/Developer/punchcard
rsync -a --exclude '.git' --exclude 'web' --exclude 'node_modules' \
  --exclude 'scratchpad' --exclude 'docs/openapi.json' \
  --exclude '.github' --exclude 'Packaging' --exclude 'Tools' \
  ~/Developer/helva-todo/ ./
rm -rf internal/gemini internal/repo/db skills plugin audit/ CHANGELOG.md HANDOFF.md
mv cmd/helva cmd/punchcard
```

- [ ] **Adım 2: Modül yolunu ve isimleri değiştir**

```bash
grep -rl 'cobanov/helva-todo' --include='*.go' --include='*.mod' --include='*.yaml' . \
  | xargs sed -i '' 's|github.com/cobanov/helva-todo|github.com/cobanov/punchcard|g'
sed -i '' 's|^module .*|module github.com/cobanov/punchcard|' go.mod
grep -rl '\bhelva\b' --include='Makefile' --include='*.yml' --include='Dockerfile' . \
  | xargs sed -i '' 's|helva|punchcard|g'
```

- [ ] **Adım 3: Derlemeyi dene, kalan referansları gör**

```bash
go build ./... 2>&1 | head -40
```

Beklenen: `internal/repo/db` silindiği ve domain kodu hâlâ durduğu için çok
sayıda hata. Bu normal — Görev 2 ve 3 bunları kapatır. Hataların **hepsinin**
lists/tasks/position/activity/gemini/db kaynaklı olduğunu doğrula; başka bir
şey kırıldıysa Adım 2'deki sed fazla eşleşmiş demektir.

- [ ] **Adım 4: Commit**

```bash
git add -A
git commit -m "Port the helva chassis into punchcard

Mechanical copy of the helva-todo tree minus the web client, the embedded
dist, the Gemini chat and the generated sqlc package. Module path, binary
name and Docker/Makefile references renamed. Does not build yet: the domain
layer and the generated DB package land in the next two commits."
```

---

### Görev 2: Domain kodunu buda

**Dosyalar:**
- Sil: `internal/service/{lists,tasks,tasks_bulk,members,invites,position,activity,changes,domain,native_code}*.go`
- Sil: `internal/http/{list_handlers,task_handlers,task_bulk_handler,activity_handler,chat_handler,changes_handler,dto_domain,move_test,domain_test,task_scope_test,export_test}*.go`
- Sil: `internal/repo/` içindeki domain store dosyaları
- Değiştir: `internal/http/routes.go`, `internal/service/doc.go`

**Arayüzler:**
- Üretir: `service.Service` yapısı yalnızca kimlik/hesap/webhook alanlarıyla
  kalır; domain metotları Görev 6'dan itibaren eklenir.

- [ ] **Adım 1: Domain dosyalarını sil**

```bash
cd ~/Developer/punchcard
rm -f internal/service/{lists,tasks,tasks_bulk,members,invites,position,activity,changes,domain,native_code}*.go
rm -f internal/service/{position_test,position_fixture_test,activity_write_test,invites_test,changes_test,tasks_patch_test,native_code_test}.go
rm -f internal/http/{list_handlers,task_handlers,task_bulk_handler,activity_handler,chat_handler,changes_handler,dto_domain}.go
rm -f internal/http/{move_test,domain_test,task_scope_test,export_test,activity_test,activity_at_test,activity_origin_test,mcp_test}.go
rm -rf internal/mcp internal/activity
rm -f db/position_fixtures.json
```

- [ ] **Adım 2: `routes.go` içinde silinen handler'ların kayıtlarını kaldır**

`internal/http/routes.go` dosyasında yalnızca şunlar kalmalı: `/healthz`,
`/metrics`, auth uçları, `/v1/me`, `/v1/account*`, `/v1/tokens*`,
`/v1/webhooks*`, `/v1/events` (SSE), OpenAPI dokümantasyonu.

- [ ] **Adım 3: Makefile'dan düşen hedefleri çıkar**

`web`, `web-unit`, `ios-core`, `position-parity`, `version-parity` hedeflerini
sil. `check` hedefi şuna iner:

```make
check: vet lint sec vuln openapi-check test build ## Run the full local gate
```

- [ ] **Adım 4: Derle — kalan hataların tamamı `internal/repo/db` kaynaklı olmalı**

```bash
go build ./... 2>&1 | grep -v 'internal/repo/db' | head -20
```

Beklenen: boş çıktı (yalnızca üretilmemiş db paketi hataları kalır).

- [ ] **Adım 5: Commit**

```bash
git add -A
git commit -m "Drop the todo domain from the ported chassis

Removes lists, tasks, members, invites, ordering, the activity log, the
change feed and the MCP surface, along with their handlers and tests. The
identity, webhook, SSE and observability layers stay. Still does not build:
the generated DB package is regenerated in the next commit."
```

---

### Görev 3: Migration'ları sıfırdan yaz

helva'nın 13 migration'ı taşınmaz. Yeni ürünün verisi yok, dolayısıyla şema
üç temiz dosyada kurulur ve `auth_sessions` adı en baştan doğru olur.

**Dosyalar:**
- Oluştur: `db/migrations/00001_init.sql`, `00002_domain.sql`, `00003_github.sql`
- Sil: helva'dan gelen tüm `db/migrations/*.sql`
- Değiştir: `db/queries/*.sql`

- [ ] **Adım 1: Eski migration'ları sil, 00001'i yaz**

`00001_init.sql` şunları içerir — içeriği helva'nın `00001_init.sql`,
`00002_identity.sql`, `00004_events_webhooks.sql`, `00005_oauth.sql`,
`00006_display_name.sql`, `00007_avatar.sql`, `00008_totp.sql`,
`00010_apple_oauth.sql`, `00011_token_kind.sql` dosyalarının birleşimidir; şu üç
değişiklikle:

1. `sessions` tablosu **`auth_sessions`** adıyla oluşturulur (indeks ve yabancı
   anahtar adları da `auth_sessions_*`)
2. `users` tablosuna kolon eklenir:
   ```sql
   timezone text NOT NULL DEFAULT 'UTC'
   ```
3. `00009_offline_sync.sql` ve `00013_activity.sql` içerikleri **alınmaz**

- [ ] **Adım 2: 00002_domain.sql — proje, oturum, commit**

```sql
-- +goose Up

CREATE TABLE projects (
    id                uuid PRIMARY KEY,
    owner_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    client            text NOT NULL DEFAULT '' CHECK (char_length(client) <= 200),
    color             text NOT NULL DEFAULT '' CHECK (char_length(color) <= 32),
    hourly_rate_cents bigint CHECK (hourly_rate_cents IS NULL OR hourly_rate_cents >= 0),
    currency          char(3) NOT NULL DEFAULT 'TRY',
    billable          boolean NOT NULL DEFAULT true,
    archived_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);
CREATE UNIQUE INDEX idx_projects_owner_name ON projects (owner_id, lower(name))
    WHERE deleted_at IS NULL;
CREATE INDEX idx_projects_owner_active ON projects (owner_id)
    WHERE deleted_at IS NULL AND archived_at IS NULL;

CREATE TABLE project_repos (
    id          uuid PRIMARY KEY,
    project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider    text NOT NULL DEFAULT 'github' CHECK (provider IN ('github')),
    full_name   text NOT NULL CHECK (full_name ~ '^[^/]+/[^/]+$'),
    branches    jsonb NOT NULL DEFAULT '[]',
    branches_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_project_repos_unique ON project_repos (project_id, provider, full_name);
CREATE INDEX idx_project_repos_full_name ON project_repos (full_name);

CREATE TABLE work_sessions (
    id            uuid PRIMARY KEY,
    project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    note          text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    started_at    timestamptz NOT NULL,
    ended_at      timestamptz,
    source        text NOT NULL DEFAULT 'web'
                  CHECK (source IN ('web','cli','extension','mobile','auto')),
    sync_state    text NOT NULL DEFAULT 'pending'
                  CHECK (sync_state IN ('pending','ok','error','skipped')),
    sync_attempts smallint NOT NULL DEFAULT 0,
    sync_next_at  timestamptz,
    sync_error    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    CONSTRAINT work_sessions_range CHECK (ended_at IS NULL OR ended_at > started_at)
);
-- Aynı anda tek açık oturum. Uygulama katmanına bırakılmaz.
CREATE UNIQUE INDEX one_open_session_per_user ON work_sessions (user_id)
    WHERE ended_at IS NULL AND deleted_at IS NULL;
CREATE INDEX idx_work_sessions_user_started ON work_sessions (user_id, started_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_work_sessions_project ON work_sessions (project_id, started_at);
CREATE INDEX idx_work_sessions_sync ON work_sessions (sync_next_at)
    WHERE sync_state = 'pending';

-- +goose Down
DROP TABLE work_sessions;
DROP TABLE project_repos;
DROP TABLE projects;
```

- [ ] **Adım 3: 00003_github.sql**

```sql
-- +goose Up

CREATE TABLE commits (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    repo_full_name text NOT NULL,
    sha            text NOT NULL CHECK (char_length(sha) BETWEEN 7 AND 64),
    message        text NOT NULL DEFAULT '',
    committed_at   timestamptz NOT NULL,
    url            text NOT NULL DEFAULT '',
    additions      integer,
    deletions      integer,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_commits_unique ON commits (user_id, repo_full_name, sha);
CREATE INDEX idx_commits_user_time ON commits (user_id, committed_at DESC);

CREATE TABLE session_commits (
    session_id uuid NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    commit_id  uuid NOT NULL REFERENCES commits(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, commit_id)
);
-- Bir commit en fazla bir oturuma bağlanır.
CREATE UNIQUE INDEX idx_session_commits_commit ON session_commits (commit_id);

CREATE TABLE github_connections (
    user_id          uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    github_login     text NOT NULL,
    access_token_enc bytea NOT NULL,
    scopes           text NOT NULL DEFAULT '',
    connected_at     timestamptz NOT NULL DEFAULT now(),
    last_scan_at     timestamptz,
    last_error       text,
    revoked_at       timestamptz
);

CREATE TABLE github_emails (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email   citext NOT NULL,
    PRIMARY KEY (user_id, email)
);

-- +goose Down
DROP TABLE github_emails;
DROP TABLE github_connections;
DROP TABLE session_commits;
DROP TABLE commits;
```

- [ ] **Adım 4: Sorguları sadeleştir ve sqlc'yi çalıştır**

`db/queries/` içinde yalnızca şunlar kalır: `users.sql`, `auth_sessions.sql`
(eski `sessions.sql`, içindeki tablo adı değişmiş), `api_tokens.sql`,
`email_tokens.sql`, `audit.sql`, `events.sql`, `webhooks.sql`, `deliveries.sql`,
`idempotency.sql`, `health.sql`. Silinenler: `lists.sql`, `tasks.sql`,
`members.sql`, `invites.sql`, `activity.sql`.

```bash
make sqlc && go build ./...
```

Beklenen: ikisi de hatasız.

- [ ] **Adım 5: Commit**

```bash
git add -A
git commit -m "Rewrite the schema from scratch for punchcard

Three migrations replace helva's thirteen: identity (with login sessions
named auth_sessions and a users.timezone column), the project/work_session
domain, and the GitHub commit cache. The one-open-session-per-user rule and
the one-session-per-commit rule are partial unique indexes, not application
checks."
```

---

### Görev 4: Servis ayağa kalksın, kimlik uçtan uca çalışsın

**Dosyalar:**
- Değiştir: `internal/http/routes.go`, `internal/service/*.go` (derleme hataları)
- Test: `internal/http/auth_test.go` (helva'dan taşındı, sadeleştirildi)

**Arayüzler:**
- Üretir: `POST /v1/auth/register`, `POST /v1/auth/login`, `GET /v1/me`
  çalışır durumda; testler `testutil.Postgres` ile gerçek veritabanına koşar.

- [ ] **Adım 1: Testi koştur — önce kırmızı olduğunu gör**

```bash
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
go test ./internal/http/ -run TestAuth -v 2>&1 | tail -20
```

Beklenen: derleme hatası veya başarısızlık. **`SKIP` görürsen `DOCKER_HOST`
ayarlanmamıştır** — düzeltmeden devam etme.

- [ ] **Adım 2: Kalan derleme hatalarını kapat**

Silinen domain'e yapılan atıflar temizlenir; `service.New(...)` imzasından
domain bağımlılıkları çıkar.

- [ ] **Adım 3: Testi koştur — yeşil olmalı**

```bash
go test ./internal/http/ ./internal/service/ ./internal/repo/ -v 2>&1 | tail -20
```

Beklenen: `ok github.com/cobanov/punchcard/internal/http`

- [ ] **Adım 4: Sunucuyu elle ayağa kaldır ve doğrula**

```bash
docker run -d --rm --name pc-db -e POSTGRES_PASSWORD=dev -e POSTGRES_USER=punchcard \
  -e POSTGRES_DB=punchcard -p 55433:5432 postgres:17-alpine
DATABASE_URL="postgres://punchcard:dev@localhost:55433/punchcard?sslmode=disable" \
  APP_SECRET="dev-only-secret-at-least-32-chars-long" HTTP_ADDR=":8092" \
  PUBLIC_BASE_URL="http://localhost:8092" APP_ENV=development LOG_LEVEL=warn \
  go run ./cmd/punchcard serve &
sleep 3 && curl -fsS localhost:8092/healthz
```

Beklenen: `{"status":"ok"}`

- [ ] **Adım 5: Commit**

```bash
git add -A
git commit -m "Bring the ported service back up

Compiles, boots against a real Postgres and serves the identity surface:
register, login, sessions, API tokens, TOTP, webhooks and SSE. No domain
resources yet."
```

---

### Görev 5: Kapıyı yeşile al

**Dosyalar:**
- Değiştir: `Makefile`, `.golangci.yml`, `docs/openapi.json`, `README.md`,
  `CLAUDE.md`

- [ ] **Adım 1: OpenAPI şemasını yeniden üret**

```bash
make openapi && git diff --stat docs/openapi.json
```

- [ ] **Adım 2: `README.md` ve `CLAUDE.md`'yi punchcard'a göre yaz**

`README.md`: ne olduğu, self-host adımları, v1'in API-only olduğu.
`CLAUDE.md`: helva'nın koddan çıkarılamayan tuzakları — `DOCKER_HOST` tuzağı,
`make check` kapısı, `auth_sessions`/`work_sessions` ayrımı, GitHub tarayıcısının
dal tuzağı (Görev 13'te doldurulacak). helva'ya özgü olan her şey (embedded
dist, Tauri, position parity) **silinir**.

- [ ] **Adım 3: Tam kapıyı koştur**

```bash
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
make check
```

Beklenen: hepsi yeşil. `test` satırında `ok … internal/http` göründüğünü
doğrula; yalnızca `no test files` satırları varsa test koşmamıştır.

- [ ] **Adım 4: Commit**

```bash
git add -A
git commit -m "Turn the local gate green

make check runs vet, golangci-lint, gosec, govulncheck, an OpenAPI drift
check and the race-enabled test suite against a real Postgres. Adds the
punchcard README and the working notes that the code cannot carry."
```

---

# Aşama 2 — Domain (Görev 6–11)

Sonunda: `curl` ile proje açılabilen, timer başlatılıp durdurulabilen,
raporlanabilen bir API.

### Görev 6: Projeler

**Dosyalar:**
- Oluştur: `db/queries/projects.sql`, `internal/service/projects.go`,
  `internal/http/project_handlers.go`
- Test: `internal/service/projects_test.go`, `internal/http/project_test.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) CreateProject(ctx context.Context, userID uuid.UUID, in CreateProjectInput) (Project, error)
  func (s *Service) ListProjects(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]Project, error)
  func (s *Service) GetProject(ctx context.Context, userID, projectID uuid.UUID) (Project, error)
  func (s *Service) UpdateProject(ctx context.Context, userID, projectID uuid.UUID, in UpdateProjectInput) (Project, error)
  func (s *Service) ArchiveProject(ctx context.Context, userID, projectID uuid.UUID) error
  ```
  `CreateProjectInput{Name, Client, Color string; HourlyRateCents *int64; Currency string; Billable bool}`

- [ ] **Adım 1: Başarısız testi yaz**

```go
func TestCreateProjectRejectsDuplicateName(t *testing.T) {
    s, userID := newServiceWithUser(t)
    _, err := s.CreateProject(ctx, userID, CreateProjectInput{Name: "capsarsiv", Currency: "TRY"})
    require.NoError(t, err)

    _, err = s.CreateProject(ctx, userID, CreateProjectInput{Name: "CAPSARSIV", Currency: "TRY"})
    require.ErrorIs(t, err, ErrConflict, "isim büyük/küçük harften bağımsız tekil olmalı")
}

func TestProjectIsScopedToOwner(t *testing.T) {
    s, alice := newServiceWithUser(t)
    _, bob := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, alice, CreateProjectInput{Name: "gizli", Currency: "TRY"})

    _, err := s.GetProject(ctx, bob, p.ID)
    require.ErrorIs(t, err, ErrNotFound, "başkasının projesi 404 döner, 403 değil")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula**

```bash
go test ./internal/service/ -run TestCreateProject -v
```
Beklenen: `undefined: CreateProjectInput`

- [ ] **Adım 3: Sorguları yaz ve üret**

`db/queries/projects.sql` içine `CreateProject`, `ListProjects`,
`GetProjectForUser`, `UpdateProject`, `ArchiveProject`, `SoftDeleteProject`
sorguları; ardından `make sqlc`.

- [ ] **Adım 4: Servisi ve handler'ları yaz**

Uçlar: `GET/POST /v1/projects`, `GET/PATCH/DELETE /v1/projects/{id}`.
`DELETE` kaydı olan projeyi arşivler (`ON DELETE RESTRICT` ihlalini yakalayıp
`ErrConflict` yerine arşive düşer), kaydı olmayanı siler.

- [ ] **Adım 5: Testleri koştur ve commit'le**

```bash
go test ./internal/service/ ./internal/http/ -run 'Project' -v
git add -A && git commit -m "Add projects

Per-owner projects with client, colour, hourly rate in integer minor units,
currency and a billable flag. Names are unique per owner case-insensitively;
another user's project reads as 404, never 403."
```

---

### Görev 7: Projeye repo bağlama

**Dosyalar:**
- Oluştur: `db/queries/project_repos.sql`
- Değiştir: `internal/service/projects.go`, `internal/http/project_handlers.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) LinkRepo(ctx context.Context, userID, projectID uuid.UUID, fullName string) (ProjectRepo, error)
  func (s *Service) UnlinkRepo(ctx context.Context, userID, projectID, repoID uuid.UUID) error
  func (s *Service) ReposForUser(ctx context.Context, userID uuid.UUID) ([]ProjectRepo, error)
  ```

- [ ] **Adım 1: Başarısız testi yaz**

```go
func TestLinkRepoValidatesFullName(t *testing.T) {
    s, userID := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "x", Currency: "TRY"})

    for _, bad := range []string{"capsarsiv", "a/b/c", "", "https://github.com/a/b"} {
        _, err := s.LinkRepo(ctx, userID, p.ID, bad)
        require.ErrorIs(t, err, ErrInvalid, "reddedilmeli: %q", bad)
    }
    r, err := s.LinkRepo(ctx, userID, p.ID, "cobanov/capsarsiv")
    require.NoError(t, err)
    require.Equal(t, "cobanov/capsarsiv", r.FullName)
}

func TestSameRepoCanBeLinkedToTwoProjects(t *testing.T) {
    s, userID := newServiceWithUser(t)
    a, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "a", Currency: "TRY"})
    b, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "b", Currency: "TRY"})
    _, err1 := s.LinkRepo(ctx, userID, a.ID, "cobanov/x")
    _, err2 := s.LinkRepo(ctx, userID, b.ID, "cobanov/x")
    require.NoError(t, err1)
    require.NoError(t, err2)
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run TestLinkRepo -v`

- [ ] **Adım 3: Uygula** — `POST /v1/projects/{id}/repos`,
  `DELETE /v1/projects/{id}/repos/{repo_id}`. Doğrulama regex'i migration'daki
  `CHECK` ile aynı: `^[^/]+/[^/]+$`.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Link GitHub repositories to projects

A project can carry several repositories and a repository can belong to
several projects: which session a commit lands in is decided by time, not by
repository."
```

---

### Görev 8: Timer — başlat, durdur, mevcut oturum

**Dosyalar:**
- Oluştur: `db/queries/work_sessions.sql`, `internal/service/sessions.go`,
  `internal/http/session_handlers.go`
- Test: `internal/service/sessions_test.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) StartSession(ctx context.Context, userID uuid.UUID, in StartSessionInput) (WorkSession, error)
  func (s *Service) StopSession(ctx context.Context, userID, sessionID uuid.UUID, at time.Time) (WorkSession, error)
  func (s *Service) CurrentSession(ctx context.Context, userID uuid.UUID) (WorkSession, error)
  func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID, from, to time.Time, projectID *uuid.UUID) ([]WorkSession, error)
  ```
  `StartSessionInput{ProjectID uuid.UUID; Note string; StartedAt *time.Time; Source string; StopCurrent bool}`

- [ ] **Adım 1: Başarısız testleri yaz — asıl kural burada**

```go
func TestStartingASecondSessionStopsTheFirst(t *testing.T) {
    s, userID := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "p", Currency: "TRY"})

    first, err := s.StartSession(ctx, userID, StartSessionInput{ProjectID: p.ID, Note: "bir"})
    require.NoError(t, err)

    second, err := s.StartSession(ctx, userID, StartSessionInput{
        ProjectID: p.ID, Note: "iki", StopCurrent: true,
    })
    require.NoError(t, err)

    reloaded, _ := s.GetSession(ctx, userID, first.ID)
    require.NotNil(t, reloaded.EndedAt, "ilk oturum kapanmış olmalı")
    require.Nil(t, second.EndedAt)

    cur, err := s.CurrentSession(ctx, userID)
    require.NoError(t, err)
    require.Equal(t, second.ID, cur.ID)
}

func TestSecondSessionWithoutStopCurrentConflicts(t *testing.T) {
    s, userID := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "p", Currency: "TRY"})
    _, _ = s.StartSession(ctx, userID, StartSessionInput{ProjectID: p.ID})

    _, err := s.StartSession(ctx, userID, StartSessionInput{ProjectID: p.ID, StopCurrent: false})
    require.ErrorIs(t, err, ErrConflict)
}

// Veritabanı kuralının uygulama katmanından bağımsız durduğunu kanıtlar.
func TestOpenSessionUniquenessIsEnforcedByTheDatabase(t *testing.T) {
    pool, userID, projectID := newPoolWithProject(t)
    insert := `INSERT INTO work_sessions (id, project_id, user_id, started_at)
               VALUES ($1, $2, $3, now())`
    _, err := pool.Exec(ctx, insert, uuid.New(), projectID, userID)
    require.NoError(t, err)
    _, err = pool.Exec(ctx, insert, uuid.New(), projectID, userID)
    require.Error(t, err, "ikinci açık oturumu indeks reddetmeli")
    require.Contains(t, err.Error(), "one_open_session_per_user")
}

func TestStopIsIdempotent(t *testing.T) {
    s, userID := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "p", Currency: "TRY"})
    ws, _ := s.StartSession(ctx, userID, StartSessionInput{ProjectID: p.ID})

    stopped, err := s.StopSession(ctx, userID, ws.ID, time.Now())
    require.NoError(t, err)
    again, err := s.StopSession(ctx, userID, ws.ID, time.Now().Add(time.Hour))
    require.NoError(t, err)
    require.Equal(t, stopped.EndedAt.Unix(), again.EndedAt.Unix(), "bitiş saati değişmemeli")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run Session -v`

- [ ] **Adım 3: Uygula**

Uçlar: `POST /v1/sessions` (`stop_current` varsayılan `true`),
`POST /v1/sessions/{id}/stop`, `GET /v1/sessions/current` (yoksa `204`),
`GET /v1/sessions?from&to&project_id`.

Durdurma tek işlem içinde yapılır: aynı transaction'da önceki kapatılır, yenisi
açılır. Aksi halde iki istemci aynı anda başlat derse kısmi indeks patlar ve
kullanıcı sebepsiz hata görür.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Add the timer

Start, stop and read the running session. A user can only ever have one open
session: starting a second one closes the first inside the same transaction,
and a partial unique index guarantees the rule even if a caller bypasses the
service layer. Stopping twice keeps the first end time."
```

---

### Görev 9: Kayıt düzeltme — saat, bölme, silme

**Dosyalar:**
- Oluştur: `internal/service/sessions_edit.go`
- Test: `internal/service/sessions_edit_test.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) UpdateSession(ctx context.Context, userID, sessionID uuid.UUID, in UpdateSessionInput) (WorkSession, error)
  func (s *Service) SplitSession(ctx context.Context, userID, sessionID uuid.UUID, at time.Time) (WorkSession, WorkSession, error)
  func (s *Service) DeleteSession(ctx context.Context, userID, sessionID uuid.UUID) error
  ```

- [ ] **Adım 1: Başarısız testleri yaz**

```go
func TestUpdateRejectsInvertedRange(t *testing.T) {
    s, userID, ws := newStoppedSession(t)
    end := ws.StartedAt.Add(-time.Minute)
    _, err := s.UpdateSession(ctx, userID, ws.ID, UpdateSessionInput{EndedAt: &end})
    require.ErrorIs(t, err, ErrInvalid)
}

func TestSplitProducesTwoAdjacentSessions(t *testing.T) {
    s, userID, ws := newStoppedSession(t) // 10:00–12:00
    at := ws.StartedAt.Add(time.Hour)     // 11:00

    left, right, err := s.SplitSession(ctx, userID, ws.ID, at)
    require.NoError(t, err)
    require.Equal(t, at.Unix(), left.EndedAt.Unix())
    require.Equal(t, at.Unix(), right.StartedAt.Unix())
    require.Equal(t, ws.EndedAt.Unix(), right.EndedAt.Unix())
}

func TestSplitOutsideRangeFails(t *testing.T) {
    s, userID, ws := newStoppedSession(t)
    _, _, err := s.SplitSession(ctx, userID, ws.ID, ws.EndedAt.Add(time.Hour))
    require.ErrorIs(t, err, ErrInvalid)
}

// Bölünen oturumun commit'leri zamanına göre doğru parçaya taşınır.
func TestSplitReassignsCommitsByTime(t *testing.T) {
    s, userID, ws := newStoppedSessionWithCommits(t) // 10:15 ve 11:30'da birer commit
    at := ws.StartedAt.Add(time.Hour)

    left, right, err := s.SplitSession(ctx, userID, ws.ID, at)
    require.NoError(t, err)
    require.Len(t, mustCommits(t, s, left.ID), 1)
    require.Len(t, mustCommits(t, s, right.ID), 1)
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run 'Split|Update' -v`

- [ ] **Adım 3: Uygula** — `PATCH /v1/sessions/{id}`, `POST /v1/sessions/{id}/split`,
  `DELETE /v1/sessions/{id}` (yumuşak silme). Bölme tek transaction.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Let recorded sessions be corrected

Times can be edited, a session can be split at a point inside its range, and
a session can be soft-deleted. Splitting moves each attached commit to
whichever half its commit time falls in."
```

---

### Görev 10: Raporlar

**Dosyalar:**
- Oluştur: `internal/service/reports.go`, `internal/http/report_handlers.go`
- Test: `internal/service/reports_test.go`

**Arayüzler:**
- Üretir:
  ```go
  type ProjectTotal struct {
      ProjectID uuid.UUID
      Name      string
      Seconds   int64
      AmountCents *int64 // billable değilse veya ücret yoksa nil
      Currency  string
  }
  func (s *Service) SummaryByProject(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]ProjectTotal, error)
  func (s *Service) SummaryByDay(ctx context.Context, userID uuid.UUID, from, to time.Time, loc *time.Location) ([]DayTotal, error)
  func (s *Service) ExportCSV(ctx context.Context, userID uuid.UUID, from, to time.Time, w io.Writer) error
  ```

- [ ] **Adım 1: Başarısız testleri yaz — zaman dilimi burada kritik**

```go
func TestAmountUsesIntegerArithmetic(t *testing.T) {
    // 90 dk, saati 333.33 TL → 33333 kuruş × 1.5 = 49999.5 → 49999 (aşağı yuvarlanır)
    s, userID := newServiceWithUser(t)
    rate := int64(33333)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{
        Name: "p", Currency: "TRY", Billable: true, HourlyRateCents: &rate,
    })
    seedSession(t, s, userID, p.ID, "10:00", "11:30")

    totals, err := s.SummaryByProject(ctx, userID, dayStart, dayEnd)
    require.NoError(t, err)
    require.Equal(t, int64(49999), *totals[0].AmountCents)
}

func TestNonBillableProjectHasNoAmount(t *testing.T) {
    s, userID := newServiceWithUser(t)
    rate := int64(10000)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{
        Name: "p", Currency: "TRY", Billable: false, HourlyRateCents: &rate,
    })
    seedSession(t, s, userID, p.ID, "10:00", "11:00")

    totals, _ := s.SummaryByProject(ctx, userID, dayStart, dayEnd)
    require.Nil(t, totals[0].AmountCents)
}

// Gün sınırı kullanıcının diliminde çizilir, UTC'de değil.
func TestDayBucketsFollowTheUserTimezone(t *testing.T) {
    s, userID := newServiceWithUser(t)
    p, _ := s.CreateProject(ctx, userID, CreateProjectInput{Name: "p", Currency: "TRY"})
    // 2026-03-01 22:30 UTC = 2026-03-02 01:30 Istanbul
    seedSessionAt(t, s, userID, p.ID, "2026-03-01T22:30:00Z", "2026-03-01T23:30:00Z")

    ist, _ := time.LoadLocation("Europe/Istanbul")
    days, err := s.SummaryByDay(ctx, userID, from, to, ist)
    require.NoError(t, err)
    require.Equal(t, "2026-03-02", days[0].Date, "Istanbul'da ertesi güne düşer")

    days, _ = s.SummaryByDay(ctx, userID, from, to, time.UTC)
    require.Equal(t, "2026-03-01", days[0].Date)
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run Summary -v`

- [ ] **Adım 3: Uygula**

Tutar: `amount = seconds * rate_cents / 3600`, tam sayı bölmesi. Gün öbeklemesi
SQL'de `date_trunc('day', started_at AT TIME ZONE $tz)` ile. Gece yarısını aşan
oturum bölünmez, günlere `generate_series` ile paylaştırılır.

Uçlar: `GET /v1/reports/summary?from&to&group_by=project|day`,
`GET /v1/reports/export.csv?from&to`.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Add reporting

Totals per project and per day for a date range, plus CSV export. Amounts are
integer minor units throughout. Day boundaries are drawn in the user's
timezone, so a session that crosses midnight in UTC lands on the right local
day."
```

---

### Görev 11: Olay yayınları

**Dosyalar:**
- Değiştir: `internal/service/sessions.go`, `sessions_edit.go`, `projects.go`
- Test: `internal/http/sse_test.go`

**Arayüzler:**
- Üretir: `session.started`, `session.stopped`, `session.updated`,
  `session.deleted`, `project.created`, `project.updated` olayları; hepsi
  taşınan `events` katmanından geçer ve hem SSE'ye hem webhook'lara düşer.

- [ ] **Adım 1: Başarısız testi yaz**

```go
func TestStartingASessionEmitsAnEvent(t *testing.T) {
    srv, token := newServerWithUser(t)
    stream := subscribeSSE(t, srv, token) // /v1/events

    p := mustCreateProject(t, srv, token, "p")
    mustStartSession(t, srv, token, p.ID, "bir şeyler")

    ev := stream.Next(t, 3*time.Second)
    require.Equal(t, "session.started", ev.Type)
    require.Equal(t, p.ID.String(), ev.Data["project_id"])
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/http/ -run SSE -v`

- [ ] **Adım 3: Uygula** — yayınlar servis metotlarının içinde, yazma ile aynı
  transaction'da `events` tablosuna yazılır (helva'daki desen).

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Emit domain events for sessions and projects

Every write publishes on the existing event feed, so SSE subscribers and
webhooks see timer changes the moment they land. Events are written in the
same transaction as the change they describe."
```

---

# Aşama 3 — GitHub (Görev 12–16)

Sonunda: ürünü ayıran özellik çalışır durumda.

### Görev 12: GitHub bağlantısı ve token şifreleme

**Dosyalar:**
- Oluştur: `internal/service/github_link.go`, `internal/http/github_handlers.go`,
  `db/queries/github.sql`
- Değiştir: `internal/config/config.go` (`GITHUB_TOKEN_KEY`)
- Test: `internal/service/github_link_test.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) ConnectGitHub(ctx context.Context, userID uuid.UUID, login, token, scopes string) error
  func (s *Service) GitHubStatus(ctx context.Context, userID uuid.UUID) (GitHubStatus, error)
  func (s *Service) DisconnectGitHub(ctx context.Context, userID uuid.UUID) error
  func (s *Service) githubToken(ctx context.Context, userID uuid.UUID) (string, error) // iç kullanım
  ```

- [ ] **Adım 1: Başarısız testleri yaz**

```go
func TestTokenIsNotStoredInPlaintext(t *testing.T) {
    s, userID, pool := newServiceWithPool(t)
    require.NoError(t, s.ConnectGitHub(ctx, userID, "cobanov", "ghp_secret_value", "repo"))

    var raw []byte
    require.NoError(t, pool.QueryRow(ctx,
        `SELECT access_token_enc FROM github_connections WHERE user_id = $1`, userID).Scan(&raw))
    require.NotContains(t, string(raw), "ghp_secret_value")

    got, err := s.githubToken(ctx, userID)
    require.NoError(t, err)
    require.Equal(t, "ghp_secret_value", got)
}

func TestStatusNeverLeaksTheToken(t *testing.T) {
    s, userID, _ := newServiceWithPool(t)
    _ = s.ConnectGitHub(ctx, userID, "cobanov", "ghp_secret_value", "repo")
    st, _ := s.GitHubStatus(ctx, userID)
    body, _ := json.Marshal(st)
    require.NotContains(t, string(body), "ghp_secret_value")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run GitHub -v`

- [ ] **Adım 3: Uygula**

AES-256-GCM, anahtar `GITHUB_TOKEN_KEY` (32 bayt, base64). Her şifrelemede yeni
nonce, nonce şifre metninin başına yazılır. Anahtar yoksa servis açılışta hata
verip durur — sessizce düz metin saklamaz.

Uçlar: `GET /v1/github/status`, `POST /v1/github/connect` (OAuth akışı
`internal/oauth`'tan, `repo` scope eklenerek), `DELETE /v1/github/connection`.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Connect a GitHub account

Stores the OAuth access token encrypted with AES-256-GCM under a key the
service refuses to start without. The status endpoint reports the login, the
scopes and the last error, never the token."
```

---

### Görev 13: GitHub istemcisi ve dal tuzağı

**Dosyalar:**
- Oluştur: `internal/github/client.go`, `internal/github/scan.go`
- Test: `internal/github/scan_test.go` (sahte HTTP sunucusu, gerçek API'ye
  çıkılmaz)

**Arayüzler:**
- Üretir:
  ```go
  type Client struct{ ... }
  func New(httpClient *http.Client, baseURL, token string) *Client
  func (c *Client) Repo(ctx context.Context, fullName string) (Repo, error)          // PushedAt taşır
  func (c *Client) Branches(ctx context.Context, fullName string) ([]string, error)
  func (c *Client) Commits(ctx context.Context, fullName, branch, author string, since, until time.Time) ([]Commit, error)
  type Commit struct{ SHA, Message, URL string; CommittedAt time.Time }
  ```

- [ ] **Adım 1: Başarısız testi yaz — spec'teki 1. tuzak**

```go
// Varsayılan dal tuzağı: parametresiz commit listesi yalnızca ana dalı tarar.
// Feature branch'teki commit bulunamazsa entegrasyon sessizce boş döner.
func TestScanFindsCommitsOnNonDefaultBranches(t *testing.T) {
    srv := fakeGitHub(t, fakeRepo{
        FullName: "cobanov/x",
        PushedAt: at("2026-03-01T12:00:00Z"),
        Branches: map[string][]fakeCommit{
            "main":            {{SHA: "aaa", At: at("2026-03-01T09:00:00Z")}},
            "feature/refactor": {{SHA: "bbb", At: at("2026-03-01T10:30:00Z")}},
        },
    })
    c := New(srv.Client(), srv.URL, "token")

    got, err := ScanRepo(ctx, c, "cobanov/x", "cobanov",
        at("2026-03-01T08:00:00Z"), at("2026-03-01T12:00:00Z"))
    require.NoError(t, err)
    require.ElementsMatch(t, []string{"aaa", "bbb"}, shas(got))
}

func TestScanDeduplicatesCommitsSeenOnTwoBranches(t *testing.T) {
    srv := fakeGitHub(t, fakeRepo{
        FullName: "cobanov/x", PushedAt: at("2026-03-01T12:00:00Z"),
        Branches: map[string][]fakeCommit{
            "main":    {{SHA: "aaa", At: at("2026-03-01T09:00:00Z")}},
            "release": {{SHA: "aaa", At: at("2026-03-01T09:00:00Z")}},
        },
    })
    got, _ := ScanRepo(ctx, New(srv.Client(), srv.URL, "t"), "cobanov/x", "cobanov", from, to)
    require.Len(t, got, 1)
}

func TestScanSkipsRepoPushedBeforeTheWindow(t *testing.T) {
    srv := fakeGitHub(t, fakeRepo{
        FullName: "cobanov/x", PushedAt: at("2026-02-01T00:00:00Z"),
        Branches: map[string][]fakeCommit{"main": {{SHA: "aaa", At: at("2026-02-01T00:00:00Z")}}},
    })
    got, err := ScanRepo(ctx, New(srv.Client(), srv.URL, "t"), "cobanov/x", "cobanov",
        at("2026-03-01T08:00:00Z"), at("2026-03-01T12:00:00Z"))
    require.NoError(t, err)
    require.Empty(t, got)
    require.Equal(t, 1, srv.Calls(), "yalnızca repo bilgisi istenmeli, dal/commit değil")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/github/ -v`

- [ ] **Adım 3: Uygula**

`ScanRepo` sırası: `Repo()` → `pushed_at` pencereden önceyse boş dön →
`Branches()` → her dal için `Commits(sha=branch, since, until, author)` →
sha'ya göre tekilleştir. Sayfalama `per_page=100` ve `Link` başlığıyla.
`403` + `x-ratelimit-remaining: 0` ayrı bir hata tipine (`ErrRateLimited`)
çevrilir; çağıran onu geçici hata sayar.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Add the GitHub scanner

Walks every branch rather than the default one: GitHub's commit listing only
covers the default branch, which would make the whole feature return nothing
for anyone working on a feature branch — and return it silently. Skips
repositories whose pushed_at predates the window, and dedupes commits seen on
several branches."
```

---

### Görev 14: Tarama işi ve durum makinesi

**Dosyalar:**
- Oluştur: `internal/service/github_scan.go`
- Değiştir: `internal/repo/janitor.go`
- Test: `internal/service/github_scan_test.go`

**Arayüzler:**
- Üretir:
  ```go
  func (s *Service) ScanWindow(ctx context.Context, userID uuid.UUID, from, to time.Time) (ScanResult, error)
  func (s *Service) RunPendingScans(ctx context.Context, now time.Time) error // janitor çağırır
  ```

- [ ] **Adım 1: Başarısız testleri yaz — spec'teki 2. tuzak**

```go
// Geç push tuzağı: oturum kapanırken push edilmemiş commit sonradan iliştirilmeli.
func TestRescanAttachesLatePushedCommits(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    ws := mustStoppedSession(t, s, userID, p.ID, "10:00", "12:00")

    _, err := s.ScanWindow(ctx, userID, ws.StartedAt, *ws.EndedAt)
    require.NoError(t, err)
    require.Empty(t, mustCommits(t, s, ws.ID), "henüz push yok")

    gh.AddCommit("cobanov/x", "main", "aaa", at("11:15"))
    _, err = s.ScanWindow(ctx, userID, ws.StartedAt, *ws.EndedAt)
    require.NoError(t, err)
    require.Len(t, mustCommits(t, s, ws.ID), 1)
}

func TestScanIsIdempotent(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    ws := mustStoppedSession(t, s, userID, p.ID, "10:00", "12:00")
    gh.AddCommit("cobanov/x", "main", "aaa", at("11:15"))

    for i := 0; i < 3; i++ {
        _, err := s.ScanWindow(ctx, userID, ws.StartedAt, *ws.EndedAt)
        require.NoError(t, err)
    }
    require.Len(t, mustCommits(t, s, ws.ID), 1)
}

func TestFailedScanBacksOffAndEventuallyErrors(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    gh.FailWith(http.StatusInternalServerError)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    ws := mustStoppedSession(t, s, userID, p.ID, "10:00", "12:00")

    now := ws.EndedAt.Add(time.Minute)
    for i := 0; i < 5; i++ {
        require.NoError(t, s.RunPendingScans(ctx, now))
        now = now.Add(24 * time.Hour) // her denemenin zamanı gelmiş olsun
    }
    reloaded, _ := s.GetSession(ctx, userID, ws.ID)
    require.Equal(t, "error", reloaded.SyncState)
    require.NotEmpty(t, reloaded.SyncError)
}

func TestRevokedTokenStopsScanningAndIsReported(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    gh.FailWith(http.StatusUnauthorized)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    _ = mustStoppedSession(t, s, userID, p.ID, "10:00", "12:00")

    require.NoError(t, s.RunPendingScans(ctx, time.Now()))
    st, _ := s.GitHubStatus(ctx, userID)
    require.NotEmpty(t, st.LastError, "kullanıcı bağlantının koptuğunu görmeli")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run Scan -v`

- [ ] **Adım 3: Uygula**

- `StopSession` başarıyla dönünce oturum `sync_state='pending'`,
  `sync_next_at=now()` ile işaretlenir.
- Janitor `sync_next_at <= now()` olan pending oturumları toplar, kullanıcı
  başına pencereleri birleştirir ve `ScanWindow` çağırır.
- Başarısızlıkta `sync_attempts++`, `sync_next_at` sırayla
  1dk → 5dk → 30dk → 2sa → 12sa ötelenir; 5. denemeden sonra `sync_state='error'`.
- `401`/`403 (revoked)` alınırsa tarama durur, `github_connections.last_error`
  yazılır, o kullanıcının pending oturumları `skipped` yapılır.
- Saatlik iş: son 7 günün kapalı oturumlarını `pending`e geri alır (geç push).

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Scan GitHub for the commits behind a session

Stopping a session queues a scan; a janitor runs it with exponential backoff
and re-queues the last seven days every hour, so commits pushed hours after
they were written still find their session. A revoked token stops the scan
and surfaces on the connection status instead of silently returning nothing."
```

---

### Görev 15: Commit'leri oturuma bağla, eşleşmeyenleri öbekle

**Dosyalar:**
- Değiştir: `internal/service/github_scan.go`
- Oluştur: `internal/service/unmatched.go`
- Test: `internal/service/unmatched_test.go`

**Arayüzler:**
- Üretir:
  ```go
  type CommitCluster struct {
      From, To time.Time
      RepoFullName string
      Commits []Commit
      SuggestedProjectID *uuid.UUID
      SuggestedNote string
  }
  func (s *Service) UnmatchedClusters(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]CommitCluster, error)
  func (s *Service) SessionFromCluster(ctx context.Context, userID uuid.UUID, in ClusterToSessionInput) (WorkSession, error)
  ```

- [ ] **Adım 1: Başarısız testleri yaz**

```go
func TestCommitOutsideAnySessionStaysUnmatched(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    ws := mustStoppedSession(t, s, userID, p.ID, "10:00", "12:00")
    gh.AddCommit("cobanov/x", "main", "in", at("11:00"))
    gh.AddCommit("cobanov/x", "main", "out", at("14:00"))

    _, err := s.ScanWindow(ctx, userID, at("08:00"), at("18:00"))
    require.NoError(t, err)

    require.Len(t, mustCommits(t, s, ws.ID), 1)
    clusters, err := s.UnmatchedClusters(ctx, userID, at("08:00"), at("18:00"))
    require.NoError(t, err)
    require.Len(t, clusters, 1)
    require.Equal(t, "out", clusters[0].Commits[0].SHA)
}

func TestClustersSplitOnGapsLongerThanThirtyMinutes(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    _ = mustProjectWithRepo(t, s, userID, "cobanov/x")
    for _, ts := range []string{"14:00", "14:20", "15:30", "15:40"} {
        gh.AddCommit("cobanov/x", "main", "c"+ts, at(ts))
    }
    _, _ = s.ScanWindow(ctx, userID, at("08:00"), at("18:00"))

    clusters, _ := s.UnmatchedClusters(ctx, userID, at("08:00"), at("18:00"))
    require.Len(t, clusters, 2)
    require.Len(t, clusters[0].Commits, 2)
    require.Len(t, clusters[1].Commits, 2)
}

func TestSessionFromClusterAttachesItsCommits(t *testing.T) {
    s, userID, gh := newServiceWithFakeGitHub(t)
    p := mustProjectWithRepo(t, s, userID, "cobanov/x")
    gh.AddCommit("cobanov/x", "main", "aaa", at("14:00"))
    _, _ = s.ScanWindow(ctx, userID, at("08:00"), at("18:00"))
    cl, _ := s.UnmatchedClusters(ctx, userID, at("08:00"), at("18:00"))

    ws, err := s.SessionFromCluster(ctx, userID, ClusterToSessionInput{
        ProjectID: p.ID, From: cl[0].From, To: cl[0].To, Note: "kurtarıldı",
    })
    require.NoError(t, err)
    require.Equal(t, "auto", ws.Source)
    require.Len(t, mustCommits(t, s, ws.ID), 1)

    left, _ := s.UnmatchedClusters(ctx, userID, at("08:00"), at("18:00"))
    require.Empty(t, left, "artık eşleşmiş olmalı")
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/service/ -run 'Unmatched|Cluster' -v`

- [ ] **Adım 3: Uygula**

Bağlama: her commit için `committed_at` hangi kapalı oturumun aralığına
düşüyorsa `session_commits`'e yazılır (`ON CONFLICT DO NOTHING`).
Öbekleme: bağsız commit'ler zamana göre sıralanır, 30 dakikadan uzun boşlukta
yeni öbek açılır. Önerilen başlangıç ilk commit − 15 dk; önerilen proje repo tek
projeye bağlıysa o proje; önerilen not commit mesajlarının ilk satırlarından.

Uçlar: `GET /v1/github/unmatched?from&to`, `POST /v1/sessions` içinde
`source='auto'`.

- [ ] **Adım 4: Yeşil olduğunu doğrula ve commit'le**

```bash
git add -A && git commit -m "Attach commits to sessions and surface the rest

A commit lands in whichever session covers its commit time. Commits covered
by no session are grouped into clusters on 30-minute gaps and offered back as
suggested records, which is how a forgotten timer gets recovered: the
evidence was on GitHub all along."
```

---

### Görev 16: Elle yeniden tarama ve OpenAPI

**Dosyalar:**
- Değiştir: `internal/http/session_handlers.go`, `internal/http/github_handlers.go`
- Değiştir: `docs/openapi.json`

- [ ] **Adım 1: Başarısız testi yaz**

```go
func TestRescanEndpointRequeuesTheSession(t *testing.T) {
    srv, token, ws := newServerWithStoppedSession(t)
    markSyncState(t, ws.ID, "error")

    resp := post(t, srv, token, "/v1/sessions/"+ws.ID.String()+"/rescan", nil)
    require.Equal(t, http.StatusAccepted, resp.Code)
    require.Equal(t, "pending", reloadSession(t, ws.ID).SyncState)
}
```

- [ ] **Adım 2: Kırmızı olduğunu doğrula** — `go test ./internal/http/ -run Rescan -v`

- [ ] **Adım 3: Uygula ve şemayı üret**

```bash
make openapi
```

- [ ] **Adım 4: Tam kapı ve commit**

```bash
export DOCKER_HOST=unix:///Users/cobanov/.orbstack/run/docker.sock
make check
git add -A && git commit -m "Allow a scan to be retried by hand

Adds POST /v1/sessions/{id}/rescan for a session whose scan ended in error,
and regenerates the OpenAPI document for the full v1 surface."
```

---

# Aşama 4 — Canlıya alma (Görev 17–18)

### Görev 17: Self-host paketi

**Dosyalar:**
- Değiştir: `deploy/docker-compose.yml`, `deploy/Caddyfile`, `Dockerfile`
- Oluştur: `deploy/.env.example`, `deploy/docker-compose.homelab.yml`

- [ ] **Adım 1: Dockerfile'dan Node aşamasını çıkar**

v1'de gömülü frontend yok; imaj tek aşamalı Go derlemesi.

- [ ] **Adım 2: `.env.example` yaz**

```
POSTGRES_PASSWORD=
DOMAIN=punchcard.cobanov.run
APP_SECRET=              # en az 32 karakter
GITHUB_CLIENT_ID=
GITHUB_CLIENT_SECRET=
GITHUB_TOKEN_KEY=        # base64, 32 bayt: openssl rand -base64 32
PUBLIC_BASE_URL=https://punchcard.cobanov.run
```

- [ ] **Adım 3: Yerelde uçtan uca doğrula**

```bash
cd deploy && cp .env.example .env && $EDITOR .env
docker compose up --build -d
curl -fsS http://localhost/healthz
curl -fsS http://localhost/docs | head -5
```

- [ ] **Adım 4: Commit**

```bash
git add -A && git commit -m "Package punchcard for self-hosting

Single-stage Go image, compose file with Postgres and Caddy, and an env
example that names every secret the service refuses to start without."
```

---

### Görev 18: ct104'e deploy ve canlı doğrulama

**Dosyalar:**
- Oluştur: `scratchpad/deploy.sh` (gitignored)

- [ ] **Adım 1: GitHub OAuth App'i oluştur**

`https://github.com/settings/developers` → yeni OAuth App.
Callback: `https://punchcard.cobanov.run/v1/auth/oauth/github/callback`.
Client id/secret `deploy/.env`'e yazılır.

- [ ] **Adım 2: DNS ve tünel**

Cloudflare'de `punchcard.cobanov.run` kaydı; helva'nın (`todo.cobanov.run`)
tünel ve Caddy düzeni örnek alınır. **Kök alias kullan: `ct104-apps`.**

- [ ] **Adım 3: Deploy**

```bash
git archive HEAD | ssh ct104-apps 'mkdir -p /opt/punchcard && tar -x -C /opt/punchcard'
ssh ct104-apps 'cd /opt/punchcard && docker compose -f deploy/docker-compose.homelab.yml up --build -d'
```

- [ ] **Adım 4: Canlıda uçtan uca doğrula**

```bash
curl -fsS https://punchcard.cobanov.run/healthz
# kayıt ol → giriş yap → proje aç → timer başlat → durdur → raporu oku
```

Gerçek bir GitHub hesabıyla bağlan, gerçek bir repoya commit at, taramanın
commit'i iliştirdiğini gör. **Bu adım geçmeden v1 bitmiş sayılmaz** — sahte
sunucuya karşı geçen testler gerçek API'nin davranışını kanıtlamaz.

- [ ] **Adım 5: Sürümü işaretle ve haber ver**

```bash
git tag v1.0.0 && git commit --allow-empty -m "Release v1.0.0 — backend"
~/bin/ntfy-send.sh -t "punchcard v1 canlıda" "punchcard.cobanov.run ayakta; \
proje, timer, rapor ve GitHub commit eşleme uçtan uca doğrulandı."
```

---

## Öz-denetim

**Spec kapsamı.** Spec'in her bölümü bir göreve düşüyor: §3 mimari → Görev 1–5;
§4 veri modeli → Görev 3; §5 API → Görev 6–16; §6 arayüz → v1 dışı, uçlar bu
akışı karşılayacak şekilde tasarlandı; §7 GitHub → Görev 12–16; §8 hata
durumları → Görev 8 (çakışma), 9 (geçersiz aralık), 14 (kota, iptal), 10 (zaman
dilimi); §9 doğrulama → her görevin test adımları + Görev 5, 16 kapı; §10
dağıtım → Görev 17–18.

**Bilinçli boşluk.** Spec §8'deki "8 saati aşan timer bildirimi" v1 backend'inde
uygulanmıyor: bildirimi gösterecek istemci yok. Arayüz spec'ine taşındı.

**Tip tutarlılığı.** `WorkSession`, `Project`, `ProjectRepo`, `Commit`,
`CommitCluster` tipleri Görev 6–15 boyunca aynı adlarla kullanılıyor.
`ErrInvalid`, `ErrConflict`, `ErrNotFound` helva'nın `internal/service/errors.go`
dosyasından geliyor ve tüm görevlerde aynı anlamda.

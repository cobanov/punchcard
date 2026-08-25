# punchcard — tasarım belgesi

Tarih: 2026-08-25 · Durum: onaylandı, uygulamaya hazır

## 1. Ne yapıyoruz

Geliştiriciler için açık kaynak zaman takibi. Clockify'ın çözdüğü işi çözüyor
ama tek ekranda ve geliştiricinin kanıtına bağlı olarak: bir çalışma oturumu
bittiğinde o aralıkta attığın commit'ler kaydın altına kendiliğinden iliştirilir.

Ayırt edici özellik budur. Süre kaydı, proje yönetimi ve raporlama kısmı
Kimai/Solidtime gibi mevcut açık kaynak ürünlerde zaten var; commit eşlemesi
yok. Tasarımın tamamı bu özelliğin etrafında kurulur.

Ürün üyelik tabanlıdır: veri sunucuda durur, aynı hesap birden fazla istemciden
(v1'de web) erişilir ve senkron kalır.

## 2. Kapsam

### v1 içinde

- Üyelik: e-posta, GitHub/Google/Apple ile giriş, TOTP 2FA, API token'ları
- Projeler: ad, müşteri, renk, saatlik ücret, para birimi, faturalanabilirlik, arşiv
- Projeye GitHub reposu bağlama (çoğa çok)
- Canlı timer: başlat / durdur, sunucu tarafında tek doğruluk kaynağı
- Kayıtları elle düzeltme: başlangıç–bitiş saati değiştirme, silme, bölme
- Raporlar: tarih aralığı, proje kırılımı, süre ve tutar, gün gün grafik, CSV
- GitHub commit eşleme + geç push'lar için geriye dönük tarama
- Eşleşmemiş commit'lerden tek tıkla kayıt oluşturma
- SSE ile cihazlar arası anlık senkronizasyon
- Docker compose ile tek komut self-host

### v1 dışında

Her biri kendi spec'ini alacak:

1. Chrome eklentisi, CLI, mobil istemciler (API v1'de hazır olacak)
2. Fatura kesme, tahsilat takibi, ödeme durumu
3. Takım ve paylaşım — altyapı taşınır, arayüzde açılmaz
4. Boşta kalma tespiti (klavye/fare hareketsizliğinden otomatik kesme)
5. GitLab, Bitbucket, self-hosted git sağlayıcıları

## 3. Mimari

### 3.1 Kaynak: helva

Kod tabanı `~/Developer/helva-todo`'nun (github.com/cobanov/helva-todo)
iskeletinden türetilir. helva üretimde çalışan, API-first bir Go servisidir;
`internal/` ağacının yaklaşık yarısı domain'den bağımsızdır.

Yığın: Go 1.26 · huma/v2 (OpenAPI) · chi · pgx/v5 · goose (migration) ·
Postgres · React + Vite + Tailwind + radix-ui · testcontainers · Playwright.
Lisans MIT, helva ile aynı.

### 3.2 Neredeyse olduğu gibi taşınan

| Paket | İşlev |
|---|---|
| `internal/auth`, `internal/oauth`, `http/auth_*`, `http/csrf.go` | Kimlik, sosyal giriş, 2FA, token'lar |
| `internal/events`, `internal/webhooks`, `http/sse_handler.go` | Değişiklik akışı, dış entegrasyon, canlı güncelleme |
| `internal/config`, `observability`, `ratelimit`, `audit`, `email` | Servis altyapısı |
| `internal/http` çatısı (`server`, `routes`, `middleware`, `problem`, `respond`, `idempotency`) | HTTP katmanının kemikleri |
| `internal/repo`, `internal/service` desenleri | Katman ayrımı ve test düzeni |
| `db/` düzeni, `deploy/`, `Makefile`, e2e iskeleti | Migration akışı ve self-host paketi |

### 3.3 Yeniden yazılan

- `internal/repo` + `internal/service`: liste/görev mantığı yerine
  proje–oturum–commit
- `internal/http` handler'ları: `list_handlers.go` / `task_handlers.go` yerine
  `project_handlers.go` / `session_handlers.go` / `report_handlers.go` /
  `github_handlers.go`
- `web/src`: tüm ekranlar

### 3.4 Atılan

Sürükle-bırak (`dnd.ts`), sıralama (`position`), Gemini sohbeti
(`internal/gemini`, `chat_handler.go`), offline yazma kuyruğu (v1'de okuma
önbelleği yeterli, yazmalar çevrimiçi).

### 3.5 İsim çakışması

helva'da `sessions` tablosu **giriş oturumlarını** tutar. punchcard'da "oturum"
kullanıcının çalışma dilimidir. Kopyalama sırasında çözülür:

- Giriş oturumları → tablo `auth_sessions`
- Çalışma oturumları → tablo `work_sessions`
- API'de dışa dönük yol `/v1/sessions` çalışma oturumunu ifade eder; tek anlam
  görünür

Bu yeniden adlandırma migration 00002'de, kopyalanan koda ilk dokunuşta yapılır.
Sonraya bırakılırsa her dosyada iki anlam taşıyan bir kelime kalır.

## 4. Veri modeli

Tüm zamanlar `timestamptz`, UTC saklanır. `users` tablosuna `timezone text NOT
NULL DEFAULT 'UTC'` kolonu eklenir (helva'da yok) — rapor gün sınırları buna
göre çizilir.

### projects

```
id                uuid PK
owner_id          uuid → users(id) ON DELETE CASCADE
name              text CHECK (char_length BETWEEN 1 AND 200)
client            text NOT NULL DEFAULT ''
color             text NOT NULL DEFAULT ''
hourly_rate_cents bigint                      -- NULL = ücret takibi yok
currency          char(3) NOT NULL DEFAULT 'TRY'
billable          boolean NOT NULL DEFAULT true
archived_at       timestamptz
created_at / updated_at / deleted_at
```

- `UNIQUE (owner_id, lower(name)) WHERE deleted_at IS NULL`
- `INDEX (owner_id) WHERE deleted_at IS NULL AND archived_at IS NULL`

Ücret tam sayı kuruş olarak tutulur; kayan noktalı para hesabı yapılmaz.

### project_repos

```
id              uuid PK
project_id      uuid → projects(id) ON DELETE CASCADE
provider        text NOT NULL DEFAULT 'github'
full_name       text NOT NULL              -- "cobanov/capsarsiv"
branches        jsonb NOT NULL DEFAULT '[]' -- dal adı önbelleği
branches_at     timestamptz                 -- önbelleğin tazeliği
created_at
```

- `UNIQUE (project_id, provider, full_name)`
- `INDEX (full_name)`

Aynı repo birden fazla projeye bağlanabilir. Bir commit'in hangi projeye
düşeceğini repo değil, **zaman aralığı** belirler.

### work_sessions

```
id            uuid PK
project_id    uuid → projects(id) ON DELETE RESTRICT
user_id       uuid → users(id) ON DELETE CASCADE
note          text NOT NULL DEFAULT '' CHECK (char_length <= 500)
started_at    timestamptz NOT NULL
ended_at      timestamptz                  -- NULL = çalışıyor
source        text NOT NULL DEFAULT 'web'
                CHECK (source IN ('web','cli','extension','mobile','auto'))
sync_state    text NOT NULL DEFAULT 'pending'
                CHECK (sync_state IN ('pending','ok','error','skipped'))
sync_attempts smallint NOT NULL DEFAULT 0
sync_next_at  timestamptz
sync_error    text
created_at / updated_at / deleted_at
```

- `CHECK (ended_at IS NULL OR ended_at > started_at)`
- `CREATE UNIQUE INDEX one_open_session_per_user ON work_sessions (user_id)
   WHERE ended_at IS NULL AND deleted_at IS NULL` — **aynı anda tek açık oturum,
   veritabanı düzeyinde zorlanır**
- `INDEX (user_id, started_at DESC) WHERE deleted_at IS NULL`
- `INDEX (project_id, started_at)`
- `INDEX (sync_next_at) WHERE sync_state = 'pending'`

Süre saklanmaz, `ended_at - started_at` olarak hesaplanır. Tek doğruluk kaynağı
budur; istemcideki sayaç `started_at`'ten türetilen bir görüntüdür.

`project_id` üzerinde `ON DELETE RESTRICT`: kaydı olan proje silinemez,
arşivlenir.

### commits

Kullanıcının GitHub'dan çekilmiş commit'lerinin önbelleği. Oturumdan bağımsız
tutulur — "eşleşmemiş commit" özelliğini mümkün kılan şey budur.

```
id             uuid PK
user_id        uuid → users(id) ON DELETE CASCADE
repo_full_name text NOT NULL
sha            text NOT NULL
message        text NOT NULL DEFAULT ''
committed_at   timestamptz NOT NULL
url            text NOT NULL DEFAULT ''
additions      integer         -- NULL bırakılabilir, ek istek gerektirir
deletions      integer
created_at
```

- `UNIQUE (user_id, repo_full_name, sha)`
- `INDEX (user_id, committed_at DESC)`

### session_commits

```
session_id uuid → work_sessions(id) ON DELETE CASCADE
commit_id  uuid → commits(id) ON DELETE CASCADE
created_at
PRIMARY KEY (session_id, commit_id)
```

- `UNIQUE (commit_id)` — bir commit en fazla bir oturuma bağlanır

### github_connections

```
user_id          uuid PK → users(id) ON DELETE CASCADE
github_login     text NOT NULL
access_token_enc bytea NOT NULL   -- AES-256-GCM, anahtar GITHUB_TOKEN_KEY
scopes           text NOT NULL DEFAULT ''
connected_at     timestamptz NOT NULL DEFAULT now()
last_scan_at     timestamptz
last_error       text
revoked_at       timestamptz
```

### github_emails

Farklı makinede farklı `git config` kullanan kullanıcılar için ek yazar
eşleşmesi.

```
user_id uuid → users(id) ON DELETE CASCADE
email   citext NOT NULL
PRIMARY KEY (user_id, email)
```

## 5. API yüzeyi

Tümü `/v1` altında, huma ile OpenAPI şeması üretilir. Kimlik doğrulama
kopyalanan katmandan gelir (çerez tabanlı web oturumu veya `Authorization:
Bearer <api_token>`).

**Projeler**

```
GET    /v1/projects?archived=false
POST   /v1/projects
GET    /v1/projects/{id}
PATCH  /v1/projects/{id}
DELETE /v1/projects/{id}          -- arşivler; kaydı yoksa siler
POST   /v1/projects/{id}/repos
DELETE /v1/projects/{id}/repos/{repo_id}
```

**Oturumlar**

```
GET    /v1/sessions?from=&to=&project_id=
GET    /v1/sessions/current       -- açık oturum veya 204
POST   /v1/sessions               -- start (ended_at yok) veya tam kayıt
POST   /v1/sessions/{id}/stop
PATCH  /v1/sessions/{id}          -- not, proje, saatler
POST   /v1/sessions/{id}/split    -- verilen anda ikiye böler
DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/rescan   -- commit taramasını elle tetikler
```

`POST /v1/sessions` açık bir oturum varken çağrılırsa: varsayılan davranış
öncekini o anda kapatıp yenisini başlatmaktır (`?stop_current=true`, varsayılan
açık). Aksi halde `409` döner. Bu, "başlat" tuşuna basınca beklenen şeyin
olmasını sağlar.

**Raporlar**

```
GET /v1/reports/summary?from=&to=&group_by=project|day
GET /v1/reports/export.csv?from=&to=
```

**GitHub**

```
GET    /v1/github/status          -- bağlı mı, hangi login, son hata
POST   /v1/github/connect         -- OAuth akışını başlatır
DELETE /v1/github/connection      -- bağlantıyı koparır, token'ı siler
GET    /v1/github/repos?q=        -- repo seçici için
GET    /v1/github/unmatched?from=&to=
POST   /v1/github/emails
DELETE /v1/github/emails/{email}
```

**Canlı akış**

```
GET /v1/events                    -- SSE; session.started, session.stopped,
                                     session.updated, commits.attached
```

## 6. Arayüz

Tek sayfa uygulaması, dört sekme: **Bugün · Raporlar · Projeler · Ayarlar**.

### Bugün

```
┌────────────────────────────────────────────────────────────┐
│  ▶  Ne yapıyorsun?                    [ capsarsiv ▾ ]  ⏵   │
├────────────────────────────────────────────────────────────┤
│  Bugün                                              6s 12d │
│                                                            │
│  09:14–11:02   capsarsiv   feed sorgusu             1s 48d │
│                └ 3 commit · cobanov/capsarsiv              │
│  11:20–13:35   helva       webhook retry            2s 15d │
│  14:00–…       capsarsiv   yorum sistemi refactor  çalışıyor│
├────────────────────────────────────────────────────────────┤
│  Eşleşmemiş commit'ler                                     │
│  09:40–10:15   cobanov/capsarsiv   4 commit  [kayıt oluştur]│
└────────────────────────────────────────────────────────────┘
```

- İmleç açılışta not alanındadır. Yaz → Enter → timer döner.
- Proje seçici son kullanılan projeyi hatırlar; çoğu gün dokunulmaz.
- Ücret, müşteri, repo burada **sorulmaz**; projede bir kere girilmiştir.
- Oturum çalışırken üstteki şerit kumandaya döner (proje · not · canlı sayaç ·
  durdur); aynı oturum listede de "çalışıyor" olarak görünür, böylece günün
  akışı bütün halinde okunur.
- Saatlere çift tıklayınca satır içi düzenlenir.

### Raporlar

Tarih aralığı (bu hafta / bu ay / özel), proje kırılımı, satır başına süre ve
tutar (`süre × hourly_rate`), gün gün çubuk grafik, CSV indirme. Faturalanabilir
olmayan projeler tutar sütununda boş görünür.

### Projeler

Proje oluştur/düzenle: ad, müşteri, renk, saatlik ücret, para birimi,
faturalanabilirlik, arşivle. GitHub reposu bağlama burada yapılır.

### Ayarlar

Hesap, 2FA, API token'ları, zaman dilimi, GitHub bağlantısı ve ek e-postalar.

### Unutulan timer

Canlı timer'ın tek gerçek zaafı budur. v1'deki üç önlem:

1. Her kayıt elle düzeltilebilir, silinebilir, bölünebilir
2. Timer 8 saati aşarsa bildirim
3. Eşleşmemiş commit kutusu — kanıt GitHub'da zaten durduğu için unutulan
   çalışma geri kazanılabilir

Boşta kalma tespiti bilinçli olarak v1 dışıdır: doğru yapılması her istemci için
ayrı iş ve yukarıdaki üçü sorunu pratikte kapatıyor.

## 7. GitHub entegrasyonu

### 7.1 Bağlantı

GitHub **OAuth App** (GitHub App değil). helva'da OAuth sağlayıcısı zaten kurulu;
tek fark istenen izin: `repo` (özel repoların commit'lerini okumak için gerekli).
Token AES-256-GCM ile şifrelenip `github_connections.access_token_enc`'e yazılır.
Anahtar `GITHUB_TOKEN_KEY` ortam değişkeninden gelir.

GitHub App v2'de değerlendirilecek: repo bazlı izin ve daha yüksek kota verir,
ama self-host talimatını belirgin şekilde ağırlaştırır.

### 7.2 Tarama işi

Tarama **oturum başına değil, zaman penceresi başına** çalışır. Girdi: kullanıcı
+ `[from, to]` aralığı.

1. Kullanıcının projelerine bağlı tüm repolar toplanır
2. Her repo için `GET /repos/{o}/{r}` ile `pushed_at` bakılır; pencerenin
   öncesindeyse repo atlanır
3. Repo'nun dalları çekilir (`GET /repos/{o}/{r}/branches`, `branches` alanında
   önbelleklenir, tazeliği 1 saat)
4. Her dal için `GET /repos/{o}/{r}/commits?sha={branch}&since={from}&until={to}&author={login}`
5. Sonuçlar sha'ya göre tekilleştirilip `commits` tablosuna yazılır (upsert).
   `additions`/`deletions` v1'de doldurulmaz — commit başına ek istek gerektirir
   ve kotayı hızla yer; kolonlar ileride doldurulmak üzere NULL kalır
6. Her commit `committed_at`'i hangi `work_sessions` aralığına düşüyorsa oraya
   `session_commits` ile bağlanır; hiçbirine düşmüyorsa bağsız kalır — "eşleşmemiş"
   listesini besleyen budur
7. İlgili oturumların `sync_state`'i `ok` yapılır, SSE'den `commits.attached`
   yayınlanır

Tetikleyiciler:

- `POST /v1/sessions/{id}/stop` → o oturumun aralığı için tarama kuyruğa girer
- Saatlik janitor → son 7 günün penceresi için tarama; geç push edilmiş
  commit'leri geriye dönük iliştirir
- `POST /v1/sessions/{id}/rescan` → elle

Kuyruk için ayrı tablo yoktur: `work_sessions.sync_state` + `sync_next_at`
janitor tarafından taranır, başarısızlıkta `sync_attempts` artar ve
`sync_next_at` artan aralıkla ötelenir (1dk, 5dk, 30dk, 2sa, 12sa; 5 denemeden
sonra `error`).

### 7.3 Bilinen tuzaklar

**Varsayılan dal tuzağı.** `GET /repos/{o}/{r}/commits` parametresiz çağrıldığında
yalnızca varsayılan dalı tarar. Feature branch'te çalışan bir geliştirici için
entegrasyon sessizce boş döner — ve boş dönmek, hatalı dönmekten daha kötüdür
çünkü fark edilmez. Bu yüzden dal listesi üzerinden dönmek zorunludur (adım 3–4).

**Geç push tuzağı.** Push edilmemiş commit GitHub'da yoktur. Oturum kapanışındaki
ilk tarama onu bulamaz. Saatlik 7 günlük yeniden tarama bunu kapatır.

**Yazar eşleşmesi.** Birincil filtre GitHub kullanıcı adıdır (`author={login}`).
Farklı `git config` ile atılmış commit'ler kaçabilir; kullanıcı Ayarlar'dan ek
e-posta ekleyebilir, tarama bunlar için ayrıca `author={email}` dener.

**Çakışma yok.** Aynı anda tek açık oturum kuralı, oturum aralıklarının
çakışmamasını sağlar. Dolayısıyla bir commit en fazla bir oturuma düşer;
`session_commits` üzerindeki `UNIQUE (commit_id)` bunu ayrıca garanti eder.

### 7.4 Eşleşmemiş commit'ten kayıt

`GET /v1/github/unmatched` bağsız commit'leri zaman yakınlığına göre öbekler
(30 dakikadan kısa boşluklar aynı öbek). Her öbek için önerilen kayıt:

- `started_at` = ilk commit − 15 dk (varsayılan tampon)
- `ended_at` = son commit
- `project_id` = repo'nun bağlı olduğu proje (tek projeye bağlıysa)
- `note` = commit mesajlarından üretilen özet

Kullanıcı onaylar, `source='auto'` ile kayıt oluşur ve commit'ler bağlanır.

## 8. Hata durumları ve kenar durumlar

| Durum | Davranış |
|---|---|
| GitHub API hatası / kota | Kayıt etkilenmez; tarama artan aralıkla tekrar denenir, 5 denemeden sonra `sync_state='error'` ve kayıtta yeniden dene düğmesi |
| Token iptal / süre dolmuş | Tarama durur, `github_connections.last_error` yazılır, Ayarlar'da uyarı çıkar. Sessizce boş dönmez |
| GitHub bağlı değil | Commit özellikleri gizlenir; ürünün geri kalanı çalışır |
| Açık timer varken yeni başlatma | Öncekini kapatıp yenisini başlatır (varsayılan); `stop_current=false` ile 409 |
| Gece yarısını aşan oturum | Bölünmez. Raporda kullanıcının zaman dilimine göre günlere paylaştırılır |
| Zaman dilimi | UTC saklanır, `users.timezone` ile gösterilir; gün sınırları o dilimde |
| Saat geri/ileri alınması | `timestamptz` üzerinden hesaplandığı için süre doğru kalır |
| Negatif/sıfır süre | `CHECK (ended_at > started_at)`; düzenlemede istemci de engeller |
| Kaydı olan projeyi silme | `ON DELETE RESTRICT` — arşivlenir, silinmez |
| Çevrimdışı istemci | Okuma önbellekten; yazmalar bağlantı gelince gönderilir. Timer sunucuda olduğu için sekme kapansa da akmaya devam eder |

## 9. Doğrulama

- **Birim + entegrasyon:** helva'nın testcontainers düzeni taşınır; gerçek
  Postgres'e karşı koşar. `repo` ve `service` katmanları ayrı ayrı sınanır.
- **GitHub tarafı:** sahte bir API sunucusuna (`httptest`) karşı test edilir.
  Kota harcanmaz. Zorunlu senaryolar: feature branch'teki commit bulunuyor mu,
  geç push edilen commit yeniden taramada iliştiriliyor mu, aynı commit iki kez
  bağlanmıyor mu, token iptali doğru raporlanıyor mu.
- **Veritabanı kuralları:** açık oturum tekilliği ve `UNIQUE (commit_id)` doğrudan
  test edilir — bunlar uygulama katmanına bırakılmayan garantilerdir.
- **Tarayıcı:** Playwright ile başlat–durdur–düzelt akışı, rapor toplamları,
  eşleşmemiş commit'ten kayıt oluşturma.
- **Zaman dilimi:** rapor gün sınırları en az iki farklı zaman diliminde sınanır.

## 10. Dağıtım

helva'nın `deploy/` düzeni taşınır: tek Go binary (web derlemesi gömülü) +
Postgres, docker compose ile ayağa kalkar.

```bash
cp deploy/.env.example deploy/.env    # POSTGRES_PASSWORD, DOMAIN,
                                      # GITHUB_CLIENT_ID/SECRET, GITHUB_TOKEN_KEY
docker compose -f deploy/docker-compose.yml up --build -d
curl -fsS http://localhost/healthz
```

Barındırılan örnek: `punchcard.cobanov.run`. Lisans MIT.

## 11. Sonraki spec'ler

Sırayla, her biri ayrı tasarım–plan–uygulama döngüsü:

1. CLI istemcisi (`punchcard start`, `stop`, `status`) — API token ile
2. Chrome eklentisi ve Tauri masaüstü/mobil paketi
3. Faturalandırma: fatura kesme, tahsilat durumu, müşteriye rapor gönderme
4. Takım: proje paylaşımı, üye rolleri, ekip raporları
5. GitLab ve diğer sağlayıcılar

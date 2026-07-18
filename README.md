# Rumarasa Backend (Go)

REST API untuk landing page Rumarasa Nusantara (`../rumarasa`). Go + PostgreSQL,
tanpa dependency eksternal lain — gambar pun disimpan di Postgres.

## Menjalankan (dev)

```sh
# 1. Postgres via Docker (sekali saja; data persisten di volume rumarasa-pgdata)
docker start rumarasa-pg 2>/dev/null || docker run -d --name rumarasa-pg \
  -e POSTGRES_USER=rumarasa -e POSTGRES_PASSWORD=rumarasa -e POSTGRES_DB=rumarasa \
  -p 5433:5432 -v rumarasa-pgdata:/var/lib/postgresql/data postgres:16-alpine

# 2. Jalankan API (migrasi + seed otomatis saat startup)
set -a; source .env; set +a
go run ./cmd/api
```

Server: `http://localhost:8080`. Konfigurasi lihat `.env.example`.
Admin pertama dibuat otomatis dari `ADMIN_USERNAME` / `ADMIN_PASSWORD` saat tabel admin kosong.

## Konsep

- **Content blocks** — semua teks click-to-edit di website. Key-value:
  `site.tagline`, `site.phone`, `hero.description`, dst. Frontend ambil semuanya
  sekali (`GET /content`), admin edit per key (`PUT /content/{key}`).
- **Collections** — data berbentuk daftar: `dishes`, `promos`, `happenings`,
  `facilities`, `member-benefits`. CRUD seragam.
- **Images** — upload multipart (max 5 MB, JPEG/PNG/WebP/GIF), disimpan BYTEA,
  disajikan dengan `Cache-Control: immutable`. Simpan `url` hasil upload ke
  field `image_url` item collection.

## Endpoint

Semua `GET` publik; semua mutasi butuh `Authorization: Bearer <access_token>`.

| Method | Path | Keterangan |
|---|---|---|
| POST | `/api/v1/auth/login` | `{username, password}` → access token (15 mnt) + refresh cookie (30 hari, rotasi) |
| POST | `/api/v1/auth/refresh` | Tukar refresh cookie dengan access token baru |
| POST | `/api/v1/auth/logout` | Revoke refresh token |
| GET | `/api/v1/content` | Semua content blocks: `{"data": {key: value}}` |
| PUT | `/api/v1/content/{key}` | Upsert: `{"value": "..."}` |
| DELETE | `/api/v1/content/{key}` | Hapus key |
| GET | `/api/v1/{collection}` | List, urut `sort, id` |
| POST | `/api/v1/{collection}` | Buat item (JSON sesuai field collection) |
| PATCH | `/api/v1/{collection}/{id}` | Update parsial |
| DELETE | `/api/v1/{collection}/{id}` | Hapus item |
| POST | `/api/v1/images` | Multipart `file` → `{"id", "url"}` |
| GET | `/api/v1/images/{id}` | Gambar (publik, cacheable) |
| GET | `/api/v1/images` | Daftar metadata gambar (admin) |
| DELETE | `/api/v1/images/{id}` | Hapus gambar |
| GET | `/healthz` | Health check |

Field per collection: lihat `internal/store/store.go` (`Collections`).
Error selalu `{"error": {"code", "message"}}`; sukses selalu `{"data": ...}`.

## Integrasi frontend (Next.js)

```ts
// Ambil konten untuk render
const res = await fetch(`${API_URL}/api/v1/content`);
const { data } = await res.json(); // { "site.tagline": "...", ... }

// Inline edit (admin): klik teks → PUT
await fetch(`${API_URL}/api/v1/content/site.tagline`, {
  method: "PUT",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({ value: newText }),
});
```

Login/refresh dipanggil dengan `credentials: "include"` supaya refresh cookie
ikut terkirim. Origin frontend harus terdaftar di `CORS_ORIGINS`.

## Struktur

```
cmd/api/            main: config → db → migrate → bootstrap admin → serve
internal/config/    env parsing + validasi (fail fast)
internal/db/        pgx pool + migration runner (SQL embedded)
internal/db/migrations/
internal/store/     query layer (content, collections, images, auth)
internal/auth/      JWT HS256 + refresh token (sha256, rotasi tiap pakai)
internal/httpapi/   router (net/http 1.22+), middleware, handlers
```

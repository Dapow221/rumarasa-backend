# Rumarasa Backend (Go)

REST API for the Rumarasa Nusantara landing page (`../rumarasa`). Go +
PostgreSQL with no other external dependencies — even images are stored in
Postgres.

## Running (dev)

```sh
# 1. Postgres via Docker (once; data persists in the rumarasa-pgdata volume)
docker start rumarasa-pg 2>/dev/null || docker run -d --name rumarasa-pg \
  -e POSTGRES_USER=rumarasa -e POSTGRES_PASSWORD=rumarasa -e POSTGRES_DB=rumarasa \
  -p 5433:5432 -v rumarasa-pgdata:/var/lib/postgresql/data postgres:16-alpine

# 2. Run the API (migrations + seed run automatically at startup)
set -a; source .env; set +a
go run ./cmd/api
```

Server: `http://localhost:8080`. See `.env.example` for configuration.
The first admin account is bootstrapped from `ADMIN_USERNAME` / `ADMIN_PASSWORD`
when the admins table is empty.

## Concepts

- **Content blocks** — every click-to-edit string on the website. Key-value:
  `site.tagline`, `site.phone`, `hero.description`, etc. The frontend fetches
  them all at once (`GET /content`); admins edit per key (`PUT /content/{key}`).
- **Collections** — list-shaped data: `dishes`, `promos`, `happenings`,
  `facilities`, `member-benefits`. Uniform CRUD.
- **Images** — multipart upload (max 5 MB, JPEG/PNG/WebP/GIF), stored as BYTEA,
  served with `Cache-Control: immutable`. Store the returned `url` in an item's
  `image_url` field.

## Endpoints

All `GET`s are public; every mutation requires `Authorization: Bearer <access_token>`.

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/auth/login` | `{username, password}` → access token (15 min) + refresh cookie (30 days, rotated) |
| POST | `/api/v1/auth/refresh` | Exchange the refresh cookie for a new access token |
| POST | `/api/v1/auth/logout` | Revoke the refresh token |
| GET | `/api/v1/content` | All content blocks: `{"data": {key: value}}` |
| PUT | `/api/v1/content/{key}` | Upsert: `{"value": "..."}` |
| DELETE | `/api/v1/content/{key}` | Delete a key |
| GET | `/api/v1/{collection}` | List, ordered by `sort, id` |
| POST | `/api/v1/{collection}` | Create an item (JSON matching the collection's fields) |
| PATCH | `/api/v1/{collection}/{id}` | Partial update |
| DELETE | `/api/v1/{collection}/{id}` | Delete an item |
| POST | `/api/v1/images` | Multipart `file` → `{"id", "url"}` |
| GET | `/api/v1/images/{id}` | Image bytes (public, cacheable) |
| GET | `/api/v1/images` | Image metadata list (admin) |
| DELETE | `/api/v1/images/{id}` | Delete an image |
| GET | `/healthz` | Health check |

Per-collection fields: see `Collections` in `internal/store/store.go`.
Errors are always `{"error": {"code", "message"}}`; success is always `{"data": ...}`.

## Frontend integration (Next.js)

```ts
// Fetch content for rendering
const res = await fetch(`${API_URL}/api/v1/content`);
const { data } = await res.json(); // { "site.tagline": "...", ... }

// Inline edit (admin): click text → PUT
await fetch(`${API_URL}/api/v1/content/site.tagline`, {
  method: "PUT",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({ value: newText }),
});
```

Call login/refresh with `credentials: "include"` so the refresh cookie is sent.
The frontend origin must be listed in `CORS_ORIGINS`.

## Layout

```
cmd/api/            main: config → db → migrate → bootstrap admin → serve
internal/config/    env parsing + validation (fail fast)
internal/db/        pgx pool + migration runner (embedded SQL)
internal/db/migrations/
internal/store/     query layer (content, collections, images, auth)
internal/auth/      JWT HS256 + refresh tokens (sha256, rotated on every use)
internal/httpapi/   router (net/http 1.22+), middleware, handlers
```

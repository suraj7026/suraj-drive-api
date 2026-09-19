# SDrive API

Go HTTP API that powers the [SDrive WebUI](https://github.com/suraj7026/suraj-drive-webui). Built on:

- [`chi`](https://github.com/go-chi/chi) router
- PostgreSQL 16 metadata, users, drives, and revocable sessions
- Google OAuth2 + signed HTTP-only session cookies
- MinIO / S3-compatible object storage for file content
- Stable drive/item UUIDs with presigned upload/download URLs

The frontend lives in a separate repo: [`suraj-drive-webui`](https://github.com/suraj7026/suraj-drive-webui).

## Layout

```
backend/
├── cmd/server/main.go         # HTTP entrypoint, route wiring
├── config.yaml                # Default config (override via env)
├── Dockerfile                 # Multi-stage build
├── docker-compose.yml         # Runs backend image, mounts config.yaml
├── deploy/nginx/              # Reverse proxy configs
├── go.mod / go.sum
└── internal/
    ├── auth/                  # Google OAuth + JWT helpers
    ├── config/                # Config loading
    ├── handler/               # auth, files, folders, upload, download, search
    ├── middleware/            # CORS, auth
    ├── model/                 # Shared API types
    └── storage/               # MinIO client
```

## Local Setup

1. Install Go 1.25.13+ and PostgreSQL 16.
2. From the `backend/` directory, set Google OAuth, JWT, PostgreSQL, and MinIO credentials through environment variables.
3. Apply the database migrations, then run the server:

   ```bash
   cd backend
   DATABASE_URL='postgres://...' go run ./cmd/migrate up
   go run ./cmd/server
   ```

The server listens on `http://localhost:4001` by default and expects the frontend at `http://localhost:4000`.

## Docker

```bash
cd backend
docker compose up --build
```

This mounts `config.yaml` into the container and exposes port `4001`.

## Configuration

`backend/config.yaml` provides defaults; every value can be overridden by an environment variable using the `SECTION_KEY` upper-case naming convention (e.g. `SERVER_PORT`, `MINIO_ENDPOINT`, `JWT_SECRET`).

Key settings:

| Section  | Notes                                                                 |
| -------- | --------------------------------------------------------------------- |
| `server` | `port` (4001), `frontend_url` (used for CORS + OAuth redirect).       |
| `google` | OAuth client ID/secret, callback URL, optional `allowed_domain`.      |
| `jwt`    | Signing secret, session expiry in hours.                              |
| `database` | App URL, optional migration-owner URL, pool sizes, and health timeouts. |
| `minio`  | Endpoint, public endpoint, access/secret keys, bucket prefix, region. |

## API Surface

Public:

- `GET  /api/health`
- `GET  /api/ready`
- `GET  /api/auth/google/login`
- `GET  /api/auth/google/callback`

Authenticated (JWT cookie):

- `GET    /api/auth/me`
- `POST   /api/auth/logout`
- `GET    /api/files`
- `POST   /api/files/upload`
- `DELETE /api/files`
- `POST   /api/files/copy`
- `GET    /api/files/presign/download`
- `GET    /api/files/presign/upload`
- `POST   /api/files/upload/complete`
- `POST   /api/folders`
- `DELETE /api/folders`
- `GET    /api/search`
- `POST   /api/items/{itemID}/trash`
- `POST   /api/items/{itemID}/restore`
- `POST   /api/metadata/reconcile`

## Notes

- The backend must allow the frontend origin in CORS via `server.frontend_url` / `SERVER_FRONTEND_URL`.
- PostgreSQL is the source of truth for identity, hierarchy, search, trash, and sessions. MinIO stores blob bytes only.
- Existing per-user MinIO buckets are reconciled idempotently during the first database-backed login; objects are not moved or deleted.
- Use `go run ./cmd/provision-db-role` with `DATABASE_ADMIN_URL` and `DATABASE_APP_PASSWORD` to create/update the least-privilege `drive_app` role. Store the resulting app URL as `DATABASE_URL`; keep the migration-owner URL separate.

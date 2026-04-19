# SDrive Frontend

Next.js 16 frontend for the SDrive archive UI. It now connects to the Go backend for:

- Google login and session lookup
- Bucket browsing
- Search
- Folder creation
- File upload via presigned URLs
- File download via presigned URLs
- File copy and delete

## Local Setup

1. Start the Go backend from `backend/`.
2. Create `./.env.local` in the frontend root with:

```bash
NEXT_PUBLIC_API_URL=http://localhost:4001
```

3. Start the frontend:

```bash
npm install
npm run dev
```

The frontend runs on [http://localhost:4000](http://localhost:4000).

## Backend Storage Setup

The backend is configured to use a remote MinIO host by default:

- S3 API: `https://s3.sudarshanrajagopalan.one`
- Console: `https://console.sudarshanrajagopalan.one`

Relevant backend env vars:

```bash
MINIO_ENDPOINT=s3.sudarshanrajagopalan.one
MINIO_PUBLIC_ENDPOINT=s3.sudarshanrajagopalan.one
MINIO_USE_SSL=true
MINIO_REGION=us-east-1
MINIO_ACCESS_KEY=...
MINIO_SECRET_KEY=...
```

## Notes

- The backend must allow `http://localhost:4000` in its CORS config.
- The backend no longer depends on a local MinIO Docker service.
- Shared, recents, starred, and vault screens remain placeholder views until the Go backend exposes metadata or dedicated endpoints for them.
- Root archive routes resolve against the authenticated user bucket returned by `GET /api/auth/me`.

## Verification

```bash
npm run lint
npm run build
```

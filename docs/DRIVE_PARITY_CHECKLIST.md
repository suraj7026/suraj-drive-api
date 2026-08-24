# Google Drive Parity Checklist (AI Excluded)

Last updated: 2026-08-24

This checklist covers the Go API and the sibling Next.js WebUI. PostgreSQL is the metadata source of truth; MinIO stores blob bytes. Google Drive AI features are explicitly out of scope.

## Current foundation

- [x] PostgreSQL 16 TLS connectivity verified.
- [x] Reversible, embedded migrations with isolated up/down/reapply integration tests.
- [x] Multi-user accounts and verified Google OAuth identities.
- [x] Personal drives, root folders, memberships, and stable public UUIDs.
- [x] Revocable, expiring, hashed database sessions behind signed HTTP-only cookies.
- [x] Least-privilege `drive_app` role separated from the migration owner.
- [x] Database pool configuration and schema-aware readiness endpoint.
- [x] Idempotent legacy MinIO reconciliation without moving or deleting blobs.
- [x] PostgreSQL-backed folder listing and file search.
- [x] Metadata completion after server-side and presigned uploads.
- [x] Metadata registration for folder creation and copies.
- [x] Thirty-day soft trash for files and folder subtrees; blob deletion is deferred.
- [x] Stable drive IDs in frontend routes and stable item IDs in frontend state.
- [x] Frontend TypeScript, ESLint, and production build pass.
- [x] Frontend `npm audit` reports zero vulnerabilities.
- [x] Backend `go test`, `go vet`, and `govulncheck` pass with zero reachable vulnerabilities.

## P0: production-safe core drive

### Deployment and migration gate

- [ ] Add `DATABASE_URL` (app role) and `DATABASE_MIGRATION_URL` (migration owner) to GitHub Actions secrets.
- [ ] Rotate the database password disclosed during setup and reprovision both roles.
- [ ] Deploy migrations before the API container and verify migration version/readiness in production.
- [ ] Complete one real Google login against the deployed callback and confirm one user, identity, personal drive, root, membership, and session are created.
- [ ] Run reconciliation for every existing user bucket and compare object counts, total bytes, and sampled ETags.
- [ ] Keep the old MinIO-only image available for rollback until reconciliation is signed off.

### Upload reliability

- [ ] Replace one-shot presigned PUT with durable upload sessions and idempotency keys.
- [ ] Implement multipart/resumable upload for large files, pause/resume, cancellation, expiry, and orphan-part cleanup.
- [ ] Validate final object size, MIME type, ETag, and optional SHA-256 before marking a version ready.
- [ ] Reserve metadata before upload, then use an outbox/reconciler to repair blob/metadata partial failures.
- [ ] Enforce per-user quota before issuing upload URLs and again at completion.
- [ ] Add duplicate-name policy compatible with Drive instead of the current five-suffix limit.
- [ ] Add malware/quarantine hooks and safe-content response headers before public sharing.

### File and folder operations

- [ ] Add ID-based create, rename, move, copy, and details endpoints; retire key/prefix mutation parameters.
- [ ] Make all operations transactional at the metadata layer with idempotent request IDs.
- [ ] Add collision handling, cyclic-move tests, invalid-name tests, and concurrent mutation tests.
- [ ] Return opaque cursor pagination and deterministic sorting instead of offset pagination.
- [ ] Add batch operations and partial-failure reporting.
- [ ] Add download streaming/range support or verified presigned range behavior for large media.

### Open and preview

- [ ] Verify PDF, image, video, audio, text, Office, archive, HEIC/HEIF, RAW, and unknown-file behavior in browsers.
- [ ] Move HEIC conversion out of request memory into bounded background preview jobs.
- [ ] Add preview status, failure retry, maximum source size, timeouts, and cached derivatives.
- [ ] Ensure unsupported files always offer a reliable download action.
- [ ] Add content-disposition handling for safe inline viewing versus forced download.

### Trash and recovery

- [ ] Build a real Trash page using metadata queries.
- [ ] Add restore UI and conflict handling when the original parent was deleted or renamed.
- [ ] Add explicit permanent delete with reauthentication/confirmation for risky bulk actions.
- [ ] Run a scheduled purge after 30 days that deletes MinIO versions only after the database transaction is committed.
- [ ] Add purge retries, tombstone audit events, and proof that no active reference points to a deleted blob.

## P1: sharing and Drive workflows

### Permissions and sharing

- [ ] Implement item permissions for viewer, commenter, and editor roles.
- [ ] Implement inherited folder permissions with a documented effective-permission algorithm.
- [ ] Add share-by-email invitations, pending users, resend/revoke, and notification delivery.
- [ ] Add hashed public link tokens, expiry, download controls, revocation, and abuse rate limits.
- [ ] Implement “Shared with me” and enforce permissions in every item, version, preview, download, search, and activity query.
- [ ] Add shared drives with owner/manager/content-manager/contributor/commenter/viewer semantics.
- [ ] Add permission regression tests preventing cross-user and cross-drive access.

### Drive navigation

- [ ] Implement My Drive, Shared with me, Recent, Starred, Trash, and Storage views from live metadata.
- [ ] Add list/grid modes, sort options, filters, breadcrumbs, details panel, and persistent view preferences.
- [ ] Add keyboard selection, multi-select, drag-and-drop move, context menus, and undo snackbars.
- [ ] Add shortcuts with permission-aware target resolution and broken-target behavior.
- [ ] Add folder colors and user item state.

### Search

- [ ] Search files and folders, not only files.
- [ ] Add filters for type, owner, location, modified date, shared state, starred state, and trash state.
- [ ] Add quoted phrases, exclusions, and Drive-like query chips.
- [ ] Add extracted-text indexing for supported document formats with bounded background jobs.
- [ ] Measure relevance and latency with realistic multi-user datasets.

### Versions, activity, and comments

- [ ] Create immutable file versions on overwrite and allow download/restore/keep-forever.
- [ ] Add version retention and storage accounting.
- [ ] Record all mutations and security-sensitive reads in the activity ledger.
- [ ] Build activity UI with actor, time, action, and target.
- [ ] Implement comments, replies, resolve/reopen, edit/delete tombstones, and mentions.

## P2: platform quality

### Security

- [ ] Add request and login rate limiting, brute-force protection, and sharing abuse controls.
- [ ] Add CSP, HSTS, Referrer-Policy, Permissions-Policy, and hardened content-type headers at the proxy.
- [ ] Stop returning raw internal/database errors to clients; log structured request IDs instead.
- [ ] Validate trusted reverse-proxy hops before accepting forwarded client IPs.
- [ ] Add CSRF protection for cookie-authenticated mutations.
- [ ] Add session/device management, revoke-all, account suspension, and account deletion workflows.
- [ ] Add secret rotation runbooks and automated secret scanning.

### Reliability and observability

- [ ] Add graceful shutdown, write-header/idle timeouts, and bounded background workers.
- [ ] Add structured metrics and traces for auth, database, MinIO, uploads, previews, and reconciliation.
- [ ] Add SLOs and alerts for login, list, upload completion, download, and database readiness.
- [ ] Add PostgreSQL backups with point-in-time recovery and perform restore drills.
- [ ] Add MinIO replication/versioning policy and disaster-recovery drills.
- [ ] Add metadata/blob consistency scans and repair reports.

### Test coverage

- [ ] Unit-test path validation, pagination, config validation, cookies, CORS, and every repository permission rule.
- [ ] Add handler contract tests for all status codes and response schemas.
- [ ] Add real MinIO integration tests for upload, copy, presign, range download, preview, and reconciliation.
- [ ] Add Playwright flows for login, browse, upload, open, rename, move, share, trash, restore, and logout.
- [ ] Add concurrent/race tests and run `go test -race` in CI.
- [ ] Add migration tests from every supported previous schema version with production-scale fixtures.

### Accessibility and UX

- [ ] Meet WCAG 2.2 AA for keyboard access, focus management, contrast, labels, dialogs, menus, and live upload announcements.
- [ ] Add responsive mobile/tablet layouts and touch interactions.
- [ ] Add loading skeletons, empty states, offline/network errors, retry controls, and non-destructive optimistic updates.
- [ ] Localize UI copy, dates, sizes, and time zones.
- [ ] Replace remaining “archive/bucket/object” implementation language with user-facing Drive terminology.

## Explicitly out of scope

- Gemini, AI summaries, AI search, generated content, and other AI-assisted Drive features.
- Building full Google Docs, Sheets, and Slides editors. The parity target is file storage, organization, preview, sharing, comments, versions, and collaboration plumbing; editor files may be previewed or opened through configured external integrations.

## Release criteria

Do not call the product Google Drive-quality until:

1. Every P0 box is complete.
2. Sharing authorization has cross-user integration tests and no browser-visible storage identifiers.
3. Upload, download/open, trash/restore, and reconciliation pass end to end against deployed PostgreSQL and MinIO.
4. Security scans, accessibility checks, production build, backup restore, and rollback rehearsal all pass on the release commit.

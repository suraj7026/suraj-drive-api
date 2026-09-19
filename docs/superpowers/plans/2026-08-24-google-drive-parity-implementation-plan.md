# Non-AI Google Drive Parity Implementation Plan

Date: 2026-08-24
Backend: `/Users/sudarshanrajagopalan/Developer/suraj-drive-api`
WebUI: `/Users/sudarshanrajagopalan/Developer/suraj-drive-webui`
Architecture spec: `docs/superpowers/specs/2026-08-24-drive-metadata-database-design.md`
Requirements source: `pasted-text-1.txt` attached to the Codex task

## 1. Objective and completion boundary

Build a multi-user file storage and collaboration product with functional and quality parity to Google Drive, excluding AI features. PostgreSQL owns identity, hierarchy, versions, permissions, workflow state, search metadata, trash, and audit data. MinIO stores immutable blob versions and generated derivatives.

Completion requires every requirement in the attached audit to have implementation and runtime evidence. A green build alone is insufficient. The final gate includes deployed browser flows, physical storage reconciliation, backup restoration, cross-user authorization tests, accessibility testing, and failure recovery exercises.

Full Google Docs, Sheets, and Slides implementations are not bundled into the storage UI. Office and OpenDocument formats receive reliable previews; collaborative document editing is delivered through an editor integration boundary. AI-assisted search, summaries, generation, and Gemini features remain excluded.

## 2. Delivery rules

1. Preserve existing MinIO objects until backup and reconciliation gates pass.
2. Never derive authorization from a browser-provided bucket, key, owner, or role.
3. Use stable UUIDs in public APIs. Storage locations remain server-side.
4. Every cross-system mutation uses a durable state machine and idempotency key.
5. Every metadata mutation and security-sensitive read emits an activity event.
6. No phase ships with raw provider/database errors, placeholder production secrets, known reachable vulnerabilities, or failing quality gates.
7. Each phase is independently deployable and rollback-capable.
8. Compatibility endpoints are removed only after the WebUI and reconciliation gates no longer use them.

## 3. Current-state requirement audit

### Proven complete

- PostgreSQL schema, TLS connectivity, embedded reversible migrations, and isolated PostgreSQL integration tests.
- Durable users, Google identities, personal drives, roots, memberships, revocable sessions, and a least-privilege application role.
- Stable drive and item UUIDs in the current API/WebUI path.
- Idempotent MinIO metadata reconciliation without blob movement or deletion.
- PostgreSQL-backed listing and filename search.
- Basic metadata completion for one-shot uploads, folder creation, and file copies.
- Recoverable 30-day soft-trash state and restore repository operations.
- Configured login URL, corrected preview selection, React lint defects, stable frontend build, and patched dependency chains.
- Backend unit/integration/race/vulnerability checks and frontend type/lint/build/audit checks.
- Live schema migrations through v10, including discovery state, permission workflow foundations, and notification outbox.
- Bounded HEIC preview jobs, version-scoped artifacts, and a scheduled retryable blob-purge worker.
- Metadata-backed Recent, Starred, Shared with me, Trash, and Storage views.
- Stable-ID rename/move/details/copy/trash/restore/preview/download paths in the active WebUI.
- Viewer/commenter/editor grants to existing accounts, inherited folder authorization, revocation, and cross-user integration coverage.
- Strict mutation Origin enforcement plus API/WebUI security response headers.

### Partially complete and not accepted

- Signed keyset cursors now cover folder listings and file search, and the WebUI consumes every page. Client virtualization, server-selectable sorts, folder search, and the deployed 1,000-item browser proof remain.
- Upload reservation, in-flight/completion quota accounting, atomic keep-both names, immutable version keys, multipart storage primitives, part manifests, bounded presign batches, in-session pause/resume, PostgreSQL/IndexedDB recovery display, explicit abort, and expiry cleanup are implemented. Reselect-to-resume after reload, SHA-256 verification, and interrupted MinIO/browser E2E evidence remain.
- Trash UI, restore, permanent-delete/empty-trash requests, and retention workers exist. Restore fallback/collision policy and deployed destructive-flow evidence remain.
- Activity tables exist, but not every mutation/read records an event and there is no activity UI.
- Preview support is version-aware and HEIC conversion is bounded/background. Office/ODF conversion, archive listing, and the complete browser format matrix remain.
- Effective direct/inherited authorization, sharing UI for existing users, Shared with me, invitation/access-request schemas, and notification outbox exist. Invitation acceptance/email delivery, public links, shared search/activity, and shared-drive roles remain.
- Both repos have CI quality workflows and immutable SHA deployment selection. Branch protection, canary, authenticated smoke, automatic rollback, SBOM/image scan, and provider secret configuration remain deployment gates.
- API server failures now use safe error envelopes and request IDs; production config rejects placeholder credentials and insecure TLS settings; graceful shutdown and HTTP header/idle timeouts are active.

## 4. Workstream and migration map

| Phase | Database migration | Backend work | WebUI work | Release gate |
| --- | --- | --- | --- | --- |
| 1. Safety baseline | `00003_cursor_pagination.sql` | cursors, validation, error envelope, readiness, graceful shutdown | complete pagination, remove fake actions, error boundaries | 1,000-item and failure tests |
| 2. Durable uploads | `00004_upload_jobs_and_quota.sql`, `00005_resumable_uploads.sql` | reservation service, multipart adapter, completion state machine, cleanup worker | controlled queue, pause/resume/reload, folder upload, conflict choice | interrupted multipart E2E |
| 3. Open/preview | `00006_preview_jobs.sql` | preview queue, derivatives, range/disposition policy | Office/ODF renderers, robust preview states | format/browser matrix |
| 4. Lifecycle | `00007_versions_trash_purge.sql` | rename/move/copy, versions, trash/purge workers | details, versions, Trash, restore, batch actions | retention and rollback exercise |
| 5. Discovery | `00008_search_user_state.sql` | indexed search, recent/starred/storage, content extraction | Drive navigation, filters, grid/list, preferences | relevance/latency/load gate |
| 6. Collaboration | `00009_permissions_sharing_comments.sql` | effective ACLs, invitations, links, access requests, comments | sharing dialogs, Shared with me, comments, activity | cross-user authorization suite |
| 7. Production quality | `00010_notifications_admin.sql` | notifications, rate limits, metrics/traces, admin policy hooks | mobile nav, accessibility, localization, offline states | WCAG/security/DR gates |
| 8. Ecosystem | separate service schemas | shared drives, editor/sync/mobile/developer APIs | PWA and integration surfaces | per-client conformance suites |

Migration numbers are reserved in this order so code and rollback documentation can refer to stable phase boundaries.

## 5. Phase 1 — Safety baseline

### 5.1 Cursor pagination

Backend changes:

- Replace `model.Pagination` offsets with an additive cursor response containing `limit`, `returned`, `has_more`, and opaque `next_cursor`.
- Add a versioned HMAC-signed cursor codec in `backend/internal/pagination` carrying drive, parent, sort, direction, sort value, and item ID.
- Query a unified child stream ordered by folder-first plus `(normalized_name, public_id)` for name sorting and `(updated_at, public_id)` for date sorting.
- Fetch `limit + 1`, never issue deep `OFFSET`, and reject a cursor whose drive/parent/sort does not match the request.
- Preserve `offset` only on legacy endpoints during one compatibility release.
- Add an ID-based endpoint: `GET /api/drives/{driveID}/items?parent_id=&cursor=&limit=&sort=&direction=`.

WebUI changes:

- Add typed paginated API responses and a server-side first page.
- Add a client continuation loader with `IntersectionObserver`, deduplication by item UUID, retry, and explicit “Load more” fallback.
- Sort on the server. Do not locally sort a partial dataset.
- Virtualize folders containing more than 250 visible rows while keeping semantic table/list accessibility.

Tests and acceptance:

- Cursor codec tamper, expiry/version, scope mismatch, duplicate sort value, forward pagination, and empty-page tests.
- Real PostgreSQL fixture with more than 1,000 mixed folders/files and repeated names.
- Browser test proves every UUID appears exactly once after all pages load.
- Search receives the same cursor contract.

### 5.2 Validation and reserved namespaces

- Create `backend/internal/itemname` with Unicode NFC normalization and one-segment validation.
- Reject empty names, `.`, `..`, separators, control characters, NUL, names over 255 user-perceived characters, and root-reserved `.previews`, `.keep`, `.uploads`, `.trash`, and `.system` names.
- Remove `AccessDenied` from missing-object classification. Return a distinct storage authorization error.
- Validate object keys at every legacy endpoint until those endpoints are retired.
- Add table-driven unit tests plus handler contract tests.

### 5.3 Safe errors, configuration, and process lifecycle

- Introduce a stable JSON error envelope: `code`, `message`, `request_id`, and optional safe field errors.
- Log internal errors with zerolog and request ID; never return raw PostgreSQL, OAuth, MinIO, or filesystem messages.
- Reject placeholder secrets and insecure database/storage endpoints when `server.is_production=true`.
- Add Google and MinIO upstream timeouts.
- Add `ReadHeaderTimeout`, `IdleTimeout`, maximum request body policies, signal-driven graceful shutdown, and bounded shutdown time.
- Make readiness verify PostgreSQL schema version, MinIO access, and critical configuration; liveness remains process-only.

### 5.4 Remove misleading UI behavior

- Remove Share Space until the ACL/link API exists.
- Remove pause/resume controls until durable upload sessions are active; keep honest cancel/retry behavior.
- Display zero-byte files as `0 B` everywhere.
- Implement the mobile navigation drawer or remove the inert hamburger until it works.
- Add route-level and interaction-level error boundaries with retry actions.

## 6. Phase 2 — Durable upload protocol

### 6.1 Schema

`00004_upload_jobs_and_quota.sql` adds:

- `drive.storage_usage` with committed, reserved, and quota bytes per drive.
- `drive.background_job` with job type, dedupe key, status, attempts, run time, lease owner/expiry, payload, and last error.
- `drive.outbox_event` for transactional cross-system work.
- Upload-session conflict choice, reserved bytes, storage key, checksum algorithm/value, part size, total parts, last error, and completion timestamps.

`00005_resumable_uploads.sql` adds:

- `drive.upload_part` keyed by upload session and part number with size, ETag, and checksum.
- Constraints for legal status transitions and part bounds.
- Indexes for active user sessions, expiry cleanup, job claiming, and outbox delivery.

### 6.2 API contract

- `POST /api/uploads` accepts parent item UUID, display name, MIME type, exact size, optional SHA-256, idempotency key, relative folder path, and conflict choice.
- Conflict choice is one of `keep_both`, `new_version` with target item UUID, or `replace` with target item UUID. The server never infers the choice from a filename.
- The transaction validates editor access, name, parent, quota, idempotency, and conflict target; reserves item/version/storage key and bytes.
- Files below the configured threshold receive one presigned operation. Larger files receive multipart upload metadata and a bounded first batch of part URLs.
- `POST /api/uploads/{uploadID}/parts/presign` issues selected part URLs after status and ownership checks.
- `PUT /api/uploads/{uploadID}/parts/{partNumber}` records uploaded part evidence if browser-to-MinIO callbacks are unavailable.
- `POST /api/uploads/{uploadID}/complete` locks the session, completes multipart if needed, stats the object, verifies size/checksum, marks exactly one current ready version, releases quota reservation, and writes activity/outbox records.
- `POST /api/uploads/{uploadID}/abort` is idempotent and schedules multipart cleanup.
- `GET /api/uploads?status=active` restores the user's queue after reload.

### 6.3 Storage adapter

- Add a narrow `BlobStore` interface separate from metadata repositories.
- Implement MinIO create-multipart, presign-part, list-parts, complete, abort, stat, range-read, delete-version, and copy primitives.
- Use immutable keys: `<drive-public-id>/<item-public-id>/<version-public-id>/content`.
- Never reuse a blob key for a different version or checksum.

### 6.4 Web upload manager

- Move upload orchestration from `archive-browser-view.tsx` into `features/uploads` with reducer/state-machine tests.
- Limit active files and active parts independently; defaults are three files and four parts per file.
- Persist only resumable session IDs, file fingerprints, relative paths, acknowledged parts, and UI state in IndexedDB. Never persist presigned URLs.
- Pause stops scheduling new parts and aborts active XHR/fetch requests without aborting the server session.
- Resume requests fresh part URLs and continues missing parts.
- Cancel calls the abort endpoint and removes recoverable local state after confirmation.
- Folder drag/drop uses `webkitRelativePath` where supported and validates every segment.

### 6.5 Upload acceptance

- Two simultaneous same-name initiations cannot reserve the same item/version transition.
- Completion is safe after client retry, server timeout, and process restart.
- A 10 GiB fixture can pause, reload the browser, resume, and match final size/checksum.
- Quota cannot be exceeded by concurrent reservations.
- Expired sessions release quota and abort MinIO multipart state.
- New versions preserve every older immutable blob and metadata record.

## 7. Phase 3 — File opening and preview pipeline

### 7.1 Version-aware identity

- All open, download, and preview routes accept item/version UUIDs, never object paths.
- Cache keys include version UUID plus checksum/ETag and preview profile.
- HEIC browser cache keys migrate from object path to immutable version identity.
- Presigned downloads set policy-derived `Content-Disposition` and content type.

### 7.2 Background preview jobs

- Add job types for thumbnail, image normalization, HEIC/HEIF, video poster/transcode, PDF thumbnail/text, and Office/ODF conversion.
- Worker processes claim jobs with `FOR UPDATE SKIP LOCKED`, renew leases, enforce attempts/backoff, and publish durable status.
- Input limits cover compressed bytes, decoded pixels, page count, archive expansion, CPU time, memory, and subprocess wall time.
- Run LibreOffice conversions in an isolated container/process profile with no network and bounded temporary storage.
- Store derivatives at immutable version/profile keys and record them in `drive.preview_artifact`.

### 7.3 Web preview matrix

- Native image, audio, video, PDF, and safe text viewers support loading, range access, expired URL refresh, decode failure, and retry.
- DOCX/XLSX/PPTX/ODT/ODS/ODP use generated PDF/HTML previews with download fallback.
- Archives show safe metadata-only listings; executables and unknown formats default to download with warnings.
- Open, Preview, and Download labels follow one MIME policy table shared by details and context menus.

## 8. Phase 4 — Lifecycle, versions, and organization

### 8.1 ID-based item APIs

- Add details, folder create, rename/move/description/color patch, recursive copy, batch mutation, star, and recent-open endpoints.
- Rename and move update metadata only; immutable blobs remain unchanged.
- Move uses database cycle protection and permission checks.
- Recursive copy reserves a destination tree and performs server-side blob copies through jobs with progress.

### 8.2 Versions

- List, download, label, keep-forever, restore, and delete non-current versions.
- Restore creates a new current version referencing a copied immutable blob; history never rewrites.
- Retention protects current, keep-forever, legal-hold, and shared-policy versions.

### 8.3 Trash and permanent purge

- Add Trash list APIs and UI, restore to original parent, and drive-root fallback when the parent is unavailable.
- Add delete-forever and empty-trash endpoints requiring recent authentication for broad destructive actions.
- Purge worker marks versions deleting, removes blobs idempotently, records proof/error, then tombstones metadata.
- Failed deletion remains retryable and visible to operators.

### 8.4 Drive UI workflows

- Multi-select, keyboard shortcuts, context menus, drag-to-move, batch move/copy/download/trash, undo snackbars, list/grid views, density, columns, and persisted preferences.
- All actions use stable item IDs and server authorization.

## 9. Phase 5 — Discovery

- Add live Recent, Starred, Trash, Storage, and Shared with me views.
- Search both files and folders using indexed normalized name and extracted text.
- Support MIME/type, owner, shared user, modified range, location, starred, trash, quoted phrase, and exclusion filters.
- Extraction jobs use the same bounded worker platform as previews.
- Add query-plan fixtures, relevance tests, latency budgets, and representative multi-user load tests.
- Quota UI shows committed, reserved, trash, versions, and organization allocation.

## 10. Phase 6 — Sharing and collaboration

### 10.1 Effective permissions

- Centralize authorization in a permission service used by list, item, search, version, preview, download, comment, activity, and upload queries.
- Effective role is the maximum of drive membership, inherited ancestor permission, direct item permission, and valid share-link context, constrained by organization policy.
- Prevent privilege escalation, owner removal without transfer, cross-drive target leakage, and permission cycles.

### 10.2 Sharing flows

- Direct email invitations with viewer/commenter/editor roles, expiry, resend, revoke, and pending-user state.
- Restricted and anyone-with-link modes with random 256-bit tokens stored only as hashes.
- Link policy covers expiry, download/copy permission, password option, revocation, and rate limiting.
- Access-request create/approve/deny workflows and ownership transfer.
- Shared with me and shared-drive views use effective authorization, not duplicated item rows.

### 10.3 Comments, activity, and notifications

- Threaded comments/replies, resolve/reopen, edit/delete tombstones, and mentions.
- Activity for upload, open, preview, download, rename, move, copy, versions, trash, restore, purge, permissions, links, comments, and access requests.
- In-app notification inbox and email outbox with user preferences, delivery retries, and unsubscribe boundaries.

## 11. Phase 7 — Production quality

### 11.1 CI/CD

Both repos receive pull-request and main-branch verification workflows.

Backend gates:

- gofmt diff, `go test -race ./...`, `go vet ./...`, `govulncheck`, migration up/down/reapply, repository integration, MinIO integration, contract tests, Docker build, SBOM, and image scan.

WebUI gates:

- clean install, TypeScript, ESLint, unit/component tests, production build, npm audit, Playwright desktop/mobile/accessibility suites, and container scan.

Deployment:

- Immutable image tags by Git SHA; `latest` is never the deployment selector.
- Database backup and migration preflight, canary container, readiness and authenticated smoke test, traffic promotion, and automatic rollback to the previous SHA.
- Deployment records image digest, migration version, smoke evidence, and rollback result.

### 11.2 Security and operations

- Rate-limit OAuth start/callback, session validation failures, upload operations, search, sharing, comments, and public links.
- Add CSRF tokens/origin validation for cookie mutations and strict proxy trust configuration.
- Add CSP, HSTS, Referrer-Policy, Permissions-Policy, nosniff, and safe download headers.
- Add metrics, traces, structured audit logs, SLOs, dashboards, and alerts.
- Automate PostgreSQL PITR, MinIO replication/versioning, metadata/blob reconciliation, and restore drills.
- Add session/device management, revoke-all, account deletion, organization policies, retention, legal holds, DLP hooks, and audit export.

### 11.3 Accessibility and internationalization

- Working mobile drawer and touch interactions.
- Modal/menu focus trap and restoration, semantic file table/grid, full keyboard operation, screen-reader live upload status, high contrast, and reduced motion.
- WCAG 2.2 AA automated and manual checks.
- Locale-aware messages, dates, numbers, byte units, time zones, and translation catalogs.

## 12. Phase 8 — Full non-AI ecosystem

This phase begins only after the web API and permission model are stable.

- Shared drives/team spaces with organization ownership and role policy.
- Editor integration service for real-time documents, spreadsheets, and presentations using an established collaborative editor stack; includes presence, suggestions, comments, and conflict handling.
- Offline/PWA metadata and selected-file cache with replayable mutation log.
- Desktop sync clients with stream/mirror modes, selective offline access, filesystem watchers, and deterministic conflict copies.
- Native iOS and Android clients with camera upload and document scan pipelines.
- Public file-picker/API/OAuth platform, scoped application grants, webhooks with signing/retry, and SDK conformance tests.
- Regional replication, tested failover, legal hold, eDiscovery, DLP, audit export, and organization administration.

Each new client/service receives a separate repository, threat model, data-flow spec, rollout plan, and conformance suite against the same public API. No AI capability is introduced.

## 13. Verification ledger

Every requirement maps to one of these evidence types:

| Requirement | Required evidence |
| --- | --- |
| Database invariants | migration SQL, isolated up/down tests, live version query |
| Authorization | cross-user/cross-drive integration tests and API contract tests |
| Blob consistency | MinIO/PostgreSQL reconciliation report with counts, bytes, keys, ETags/checksums |
| Upload recovery | Playwright network interruption/reload scenario plus final checksum |
| No missing items | 1,000+ item browser run with UUID uniqueness/count assertion |
| Preview safety | format matrix, resource-limit tests, worker timeout/retry evidence |
| Trash/purge | retention time travel tests and MinIO deletion proof |
| Accessibility | axe results, keyboard scripts, and manual screen-reader checklist |
| Security | dependency scans, image scan, CSRF/rate-limit tests, threat-model review |
| Disaster recovery | timestamped restore/failover drill report |
| Deployment | immutable digest, migration version, canary smoke, rollback proof |

The goal is complete only when the attached requirement list has no unimplemented or unverified row in the final audit. Until then, this plan remains active.

## 14. Immediate execution sequence

1. Implement cursor pagination and the 1,000-item tests.
2. Add reserved-name validation and correct AccessDenied behavior.
3. Replace raw API errors and harden production configuration/process lifecycle.
4. Remove fake sharing/pause controls, fix zero-byte rendering, and implement mobile navigation.
5. Add CI quality workflows and immutable image deployment.
6. Implement the upload reservation/multipart schema and backend state machine.
7. Implement the resumable WebUI upload manager.
8. Continue through preview, lifecycle, discovery, collaboration, quality, and ecosystem phases in the order above.

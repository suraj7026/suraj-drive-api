# Google Drive Parity Checklist (AI Excluded)

Last updated: 2026-09-14

This checklist covers the Go API and the sibling Next.js WebUI. PostgreSQL is the metadata source of truth; MinIO stores blob bytes. Google Drive AI features are explicitly out of scope.

**Release status: not production-ready.** Checked implementation items and earlier environment checks are not deployment sign-off. The audit below separates defects demonstrated in code, locally tested repairs, and operational controls that still require environment evidence. “Upload anything” means safely store and download supported-size arbitrary files; unsupported preview formats need a clear download fallback, not arbitrary browser execution.

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
- [x] Signed cursor pagination for listings and search, with mixed 1,000+ item and duplicate-detection integration coverage.
- [x] Durable idempotent upload reservations with atomic storage-key allocation and in-flight quota accounting.
- [x] Multipart storage primitives, persisted part manifests, bounded URL batches, in-session pause/resume, and expiry cleanup.
- [x] Metadata completion after server-side and presigned uploads.
- [x] Metadata registration for folder creation and copies.
- [x] Thirty-day soft trash for files and folder subtrees; blob deletion is deferred.
- [x] Stable drive IDs in frontend routes and stable item IDs in frontend state.
- [x] Frontend TypeScript, ESLint, and production build pass.
- [x] Frontend dependency audit is clean locally. On 2026-09-13, upgraded Next.js and eslint-config-next to 16.3.5, sharp to 0.35.4, and js-yaml to 4.3.2. `npm audit --audit-level=high` now reports zero vulnerabilities; unit tests, lint, TypeScript, and the production build pass after the update. Deployment verification remains open.
- [x] Backend `go test`, `go vet`, and `govulncheck` pass with zero reachable vulnerabilities.
- [x] Schema v16 implementation, adding an explicit irreversible deletion state, migration-ledger readiness, and durable immutable-upload completion jobs to the metadata foundation. Isolated PostgreSQL migrations and app-role tests passed with both test URLs supplied. This is not evidence that production has migrated.
- [x] Recent, Starred, Shared with me, Trash, and Storage views are backed by account-scoped metadata queries.
- [x] Active preview/download flows authorize stable item UUIDs instead of browser-provided storage keys.

## Production readiness audit (2026-09-13)

This section contains confirmed implementation defects and release verification requirements. Storage integrity, data loss, authorization, and exploitable dependencies block release. Container hardening, encryption, proxy headers, scanner limits, and scaling controls need deployment evidence; absence from a compose file alone does not prove that the live infrastructure lacks them. Dependency advisory presence does not establish that every exploit applies to the deployed OS and configuration.

Local regression evidence is recorded separately below. It does not replace browser, MinIO, deployed-environment, accessibility, load, or disaster-recovery tests.

### Upload integrity and lifecycle

- [x] Prevent a single-part presigned PUT URL from changing approved bytes. Browser capabilities now target per-session `.uploads/` staging keys; a durable worker conditionally seals the staged ETag to a distinct per-attempt `.objects/` key and publishes only the currently leased attempt. A real MinIO test proves stale PUT replay changes staging only, an existing final key cannot be overwritten, and an ETag change invalidates sealing. Delayed cleanup runs after the last single-PUT URL can remain valid.
- [ ] Verify every upload expiry/completion/abort race against real MinIO. Repository races, stale completion attempts, presigned replay, exact length enforcement, conditional sealing, and immutable destination behavior now pass locally. Complete/abort during multipart assembly and injected storage failures still need real-MinIO coverage.
- [x] Move full-object SHA-256 work out of the HTTP completion request. The endpoint durably queues completion and returns `202`; bounded workers assemble multipart staging, seal it, stream the sealed object through SHA-256, and fence publication by job attempt and lease.
- [x] Bind resumable uploads to file contents. The WebUI streams each file through SHA-256 in a dedicated worker, persists the digest, and the API binds reservation idempotency, reload recovery, completion, and final verification to the same digest.
- [x] Choose multipart size from total file size and renew active sessions on authoritative part-URL requests. Computed uploads remain within MinIO's 10,000-part limit, part URLs last 30 minutes, and each batch extends the session by 45 minutes.
- [ ] Add a time-based heartbeat during a single unusually slow part PUT. Batch presigning renews multipart sessions, but no API traffic occurs while one PUT itself is in progress.
- [x] Cancel queued jobs as well as active requests. Cancellation is recorded before the page queue can start a selected file, while active requests are aborted and their server reservation is cancelled.
- [x] Refresh once per completed upload batch and expose every queued transfer. The three-worker folder queue suppresses per-file route refreshes, refreshes once after the batch, and the transfer drawer uses a bounded scroll region with controls and status for every item.
- [x] Keep verification, scanning, ready, and failed distinct in the upload API and WebUI. The transfer remains active through durable byte verification and ClamAV quarantine, survives reload, reports the current stage, and only says verified after the version becomes ready.

### Malware, quota, and blob cleanup

- [x] Count pending, quarantined, aborted, expired, and failed uploads toward quota until durable blob deletion. Reservation/completion and storage summaries now share the same charging rules, and account-row locking serializes reservation and completion. PostgreSQL tests cover pending/expired/aborted/quarantined/ready states, quota rejection, no double counting, and quota release after deletion. Production storage reconciliation remains open.
- [x] Durably schedule deletion for infected files and scans that exhaust retries, including a worker dying on its final attempt. PostgreSQL tests confirm cleanup jobs and retained quota; actual MinIO deletion and retry failure injection remain open.
- [ ] Verify the ClamAV stream/scan-size configuration against the maximum supported upload and test files above that boundary. The compose service supplies no explicit daemon limits, so large uploads can remain quarantined when the scanner rejects the stream.
- [x] Ensure malware approval applies to the exact immutable object. The upload worker publishes a sealed per-attempt key only after size, ETag, and SHA-256 validation; browser upload capabilities never target that key, and scan publication is fenced by its own lease.
- [ ] Add a MinIO lifecycle safety net for abandoned multipart uploads and staging objects, independent of the API cleanup worker.

### Secure viewing and sharing

- [ ] Treat public-link `allow_download=false` as a UI preference unless previews use a constrained derivative. The current endpoint returns a full raw presigned object URL for preview, which can be saved directly and therefore bypasses the hidden Download button.
- [ ] Finish preview-origin isolation and safe derivatives. Locally repaired: authenticated PDFs use a sandboxed, no-referrer iframe; public pages allowlist raster images, audio, video, and sandboxed PDFs and refuse to render SVG, HTML, text, or unknown files in an iframe. The completion worker now detects MIME from object bytes and replaces untrusted upload metadata before publishing the immutable object. A separate cookieless content origin and constrained no-download derivatives remain.
- [ ] Remove storage keys and bucket-oriented fields from browser API contracts. Current list/search/upload models still return `key`/prefix data even though mutations use stable item IDs.
- [x] Verify MIME type from file bytes rather than trusting browser-provided metadata. The completion worker reads only the signature prefix, stores Go's byte-detected type, and replaces the final object's `Content-Type`; a real-MinIO regression declares HTML as PNG and proves it is sealed as `text/html`. Browser and legacy-reconciliation samples remain part of deployment acceptance.

### Security and deployment

- [x] Upgrade Next.js from 16.3.2 and resolve the critical/high dependency audit findings. The tested lockfile now uses Next.js 16.3.5 and patched transitive dependencies; the production image must still be rebuilt and verified.
- [x] Resolve client IPs through an explicit trusted-proxy chain. Login and public-link limits accept `X-Forwarded-For` only when the socket peer belongs to configured CIDRs, walk the chain from the trusted edge, reject malformed/oversized chains, and otherwise use the socket peer. Unit tests cover spoofing, trusted multi-hop forwarding, malformed input, and invalid configuration. The deployed proxy range and observed client separation still require verification.
- [x] Add a nonce-based WebUI Content-Security-Policy and production HSTS. Next.js Proxy creates a fresh nonce per response, applies it to framework and theme scripts, and restricts frames, workers, connections, objects, forms, and embedding. Local production responses used different nonces, each matching every rendered script, with `strict-dynamic` and HSTS. The actual TLS edge must be checked after deployment.
- [ ] Use a shared rate-limit store or edge enforcement for multi-instance deployments; in-memory counters reset on restart and differ between replicas.
- [ ] Run containers with health checks, read-only filesystems where practical, dropped capabilities, `no-new-privileges`, resource limits, and scanned/pinned base images. Locally repaired for both app containers: non-root runtime users, matching pinned Alpine 3.23 build/runtime for the CGO backend, readiness health checks, read-only roots, bounded writable `/tmp`, all capabilities dropped, `no-new-privileges`, CPU/memory ceilings, and graceful stops. The production ClamAV image is now pinned to the official 1.5.3 OCI digest. App-image digest pinning, ClamAV daemon limit hardening, image scanning, and deployed enforcement remain.
- [ ] Enforce and continuously verify private MinIO bucket policies, encryption at rest, TLS, and least-privilege service credentials. Bucket creation currently relies on storage defaults and one static credential can access/provision all user buckets.
- [x] Gate each deployment on the complete CI result for the same commit. Both deploy workflows call their repository's reusable CI workflow and declare `needs: verify`; production deployments are serialized. `actionlint` validation passed locally. A pushed run and deliberate failure rehearsal remain required.
- [x] Add WebUI readiness, smoke testing, and rollback. The workflow saves the previous exact image configuration, starts the new image, polls `/login` for up to five minutes, prints logs and restores the previous compose/environment on failure, and removes rollback artifacts only after success. A pushed failure rehearsal remains required.

### Performance and UI correctness

- [ ] Preserve pagination through the WebUI. Every archive, search, trash, recent, starred, storage, and shared page currently fetches every 200-item API page sequentially before rendering, then sorts and filters the full collection in the browser; add incremental loading and list/grid virtualization.
- [x] Upload multipart parts with bounded concurrency and recover through authoritative retry. Four signed parts run concurrently; a failed outer attempt reloads MinIO's accepted part manifest before continuing, and part/session sizes adapt to the file.
- [x] Bound text previews to 512 KiB without downloading the full file first. The client requests a prefix, reads a bounded stream even if Range is ignored, cancels the stream at the limit, and aborts on close/navigation or timeout. Six unit tests cover truncation, ignored Range, empty/exact-boundary files, UTF-8 boundaries, cancellation, and HTTP errors. Browser/MinIO verification remains open.
- [ ] Isolate native HEIC decoding in a killable worker process. The Go context timeout cannot interrupt a blocking libvips call, so crafted or corrupt inputs can permanently consume preview worker capacity.
- [x] Make dialogs and mobile navigation production-safe in code. Shared dialogs, full-screen preview, and mobile navigation now trap focus, close on Escape, restore the opener, make body siblings inert, expose dialog labels, and bound scrollable content to `dvh`. Nested-dialog and physical-device verification remains.
- [x] Replace the application shell's fixed `h-screen` with dynamic viewport sizing and use `dvh` bounds for dialogs, transfer queues, loading screens, and full-page states. Mobile browser chrome, rotation, safe-area, keyboard, and touch checks remain in the browser acceptance matrix.
- [ ] Add retry actions for failed uploads and semantic/live progress announcements; failed transfers currently offer dismissal but no direct retry, and upload progress is not exposed as a progress status to assistive technology.
- [x] Add route-level loading and error boundaries with retry controls. The root loading state announces pending work, the error boundary preserves a retry action, and public shared pages now return 404 only for missing/expired resources while backend outages propagate to the error boundary.
- [ ] Paginate public folders rather than permanently truncating them at 500 entries.

### Additional confirmed defects from the continuation audit

Each entry includes the trigger, consequence, repair target, and acceptance check. Entries remain open unless explicitly marked locally repaired. Paths starting with `backend/` refer to this repository; paths starting with `WebUI/` refer to the sibling `suraj-drive-webui` repository.

| Priority | Defect and code evidence | Required repair and acceptance check |
| --- | --- | --- |
| Locally repaired — data loss | Restore previously left queued deletion jobs intact. `trash.go:restoreItem`, `versions.go`, and `purge.go` now coordinate through a transaction lock per drive. A claim rechecks eligibility and marks the version `deleting` before storage work. | PostgreSQL tests cover file/folder restore before and after claims, retries, completed deletion, stale queue snapshots, retained versions, and twelve concurrent restore/claim races. Restore cancels unstarted jobs and returns 409 when current bytes have crossed the deletion boundary. Real MinIO failure injection remains open. |
| Locally repaired — integrity | Upload reservation keys previously matched only user/key. `uploads.go:ReserveUpload` now records a canonical request hash in the mutation ledger, including destination, size, name, MIME, mode, part size, conflict policy, copy source version, and expected content digest. | PostgreSQL tests reject changed payloads, serialize conflicting requests, preserve allocated duplicate names, and reject old reservations with no request binding. Operational TTL changes do not create a new reservation. |
| Locally repaired — integrity | Completion previously performed storage work before any durable exclusive claim. Migration 16 adds a recoverable completion job; every attempt writes a separate final key and metadata publication requires its current unexpired lease. | PostgreSQL tests reject changed digests, stale attempts, and exhausted jobs; real MinIO tests cover exact length, ETag-conditional sealing, replay, and immutable destinations. Complete/abort during multipart assembly still needs injected MinIO race coverage. |
| Locally repaired — security | Google account provisioning previously wrote `status = 'active'` for returning identities and email conflicts in `backend/internal/repository/metadata.go:ProvisionGoogleAccount`, allowing disabled accounts to reactivate. | Provisioning now preserves state and rejects unavailable accounts; the OAuth handler returns 403. Four PostgreSQL cases cover suspended/deleted accounts with existing identities and matching-email provisioning. Concurrent suspension/login and deployed OAuth checks remain open. |
| Locally repaired — reliability | Resume previously trusted a stale database part manifest. The WebUI now loads the individual upload resource; the handler lists MinIO's accepted parts and replaces the database snapshot before resuming, after verifying the file digest. | Add a browser fault-injection test for S3 accepting a part while the response is lost; the authoritative synchronization path is implemented and covered by repository/storage checks. |
| Locally repaired — reliability | The WebUI previously included filenames in idempotency keys, exceeding the API limit for otherwise valid filenames. `archive-browser-view.tsx:handleFilesSelected` now uses only a UUID. | Frontend lint and TypeScript pass. Browser tests with long ASCII, Unicode, and duplicate filenames remain open. |
| Locally repaired — security | A browser could label HTML bytes as an image and the completion path copied that untrusted object `Content-Type` into the published version and MinIO object. | Completion now sniffs the first 512 bytes, uses the detected type for metadata, and replaces the sealed object's content type. Real MinIO proves HTML declared as PNG remains HTML and the public page refuses to image-render it. |
| Locally repaired — security/reliability | Login and public-link limiters keyed the reverse proxy's socket address, so all proxied users could share one bucket; trusting forwarding globally would instead allow spoofing. | A bounded trusted-proxy resolver now ignores forwarding from untrusted peers and selects the first untrusted address walking from the configured edge. Unit tests cover both failure modes; deployed address-chain verification remains. |
| Locally repaired — deployment | The backend built its CGO/libvips binary on an unspecified current Alpine image and ran it on Alpine 3.19 as root, creating an ABI drift and privilege risk. | Build and runtime now both pin Alpine 3.23, the runtime uses an unprivileged UID, and the app containers add health checks, read-only roots, limited writable temp space, dropped capabilities, resource ceilings, and `no-new-privileges`. CI image build and production enforcement remain gates. |
| High — UI reliability | Transfer controllers, selected files, and queue state belong to `ArchiveBrowserView`; route unmounts can leave network work running with no visible controls. | Own transfers above route changes and enforce one global concurrency limit. Test navigation, logout, rapid batches, pause/resume, and returning to the original folder. |
| Locally repaired — worker correctness | Preview and notification lease recovery could leave final attempts processing or roll back recovery when no next job existed. Recovery now commits terminal failures even for an otherwise empty queue. | PostgreSQL tests cover exhausted preview, malware, notification, and deletion leases. Unavailable preview sources are retired rather than left queued. |
| Locally repaired — worker correctness | Preview, malware, purge, and notification results previously matched only job ID/status. All completion/failure methods now require the original attempt and an unexpired lease. Preview outputs use separate object keys per attempt. | PostgreSQL tests reject expired/stale completion and failure after reassignment. Preview publication is serialized with purge and rejected once deletion starts. Native decoder isolation, provider delivery semantics, and orphaned derivative cleanup still require work. |
| Partially verified — deployment | Readiness now calls `drive.schema_version()` and requires ledger version 16 instead of inferring a version from one table's existence. The function has a fixed search path and exposes only the version to the application role. | PostgreSQL tests prove a minimally privileged role can read the version without raw ledger access; the configured `drive_app` test also runs in CI. Missing/older/incompatible production schema and rollback rehearsals remain open. |
| Locally repaired — performance | IndexedDB recovery persistence previously ran on every progress callback, rewrote the full transfer collection, leaked database handles, and shared one store across accounts. | Progress-only writes are now debounced, lifecycle changes persist immediately, every database handle closes with its transaction, and recovery databases are namespaced by stable account ID. Large-queue profiling and account-switch browser verification remain. |

### Repairs completed locally during this continuation

- [x] Keep the latest clean revision readable while a newer revision remains quarantined. Previously `CompleteMalwareScanJob` considered unapproved newer versions when deciding whether a clean version could become current. The regression test proves a later infected revision does not hide the clean file.
- [x] Let trash cleanup advance past already queued/failed jobs. Previously the first limited candidate batch could repeatedly contain only versions already in the job table, starving later trash. `QueueExpiredTrash` now excludes existing jobs before applying its limit; a three-batch PostgreSQL test passes.
- [x] Persist exhausted deletion leases as failed, including when no replacement job is claimable. The previous path left final attempts processing indefinitely; the new regression test verifies the terminal state after the claim transaction returns no work.
- [x] Bound each purge worker's external work to one minute within its two-minute lease and fence its result by attempt. A MinIO timeout/retry test remains required.

### Verification evidence and remaining proof

- 2026-09-13: `go test -race -count=1 ./...` passed against isolated PostgreSQL 16 with **both** `TEST_DATABASE_ADMIN_URL` and `TEST_APP_DATABASE_URL` supplied. Repository races, migration up/down/reapply, and app-role grants/readiness executed. A prior claim that setting only the admin URL ran the app-role test was incorrect: that test skips without its separate URL. CI now migrates its isolated service database, provisions `drive_app`, and supplies both URLs. `go vet ./...` and workflow `actionlint` passed locally.
- 2026-09-13: WebUI `npm run test:unit` passed all six text-preview tests; lint, TypeScript, production build, and zero-vulnerability dependency audit passed after the dependency patch update. The unit command is now included in WebUI CI. The build still reports the existing Google Sans fallback warning.
- 2026-09-14: GitHub repository secret/variable names were inspected without reading secret values. The backend repository still lacks `DATABASE_URL`, `DATABASE_MIGRATION_URL`, and the required SMTP settings; the current deploy workflow requires them. `CLAMAV_IMAGE` is configured as the official ClamAV 1.5.3 image pinned to OCI index digest `sha256:d06c1d6a451d616e1dd79b42f44c8c8c291bba9cf4e75ebc4d0e43c1c6dd87bb`. Configure and validate the missing secrets before a release attempt.
- 2026-09-14: After immutable upload completion, byte-derived MIME handling, trusted-proxy resolution, and dialog/container hardening, the backend race suite passed against isolated PostgreSQL and real MinIO, followed by `go vet`. WebUI unit tests, ESLint, TypeScript, production build, and high-severity dependency audit passed. Two local production responses carried HSTS, different CSP nonces, and nonces matching every rendered script. Both compose files passed `docker compose config`; Alpine 3.23 builder/runtime manifests were verified; both workflow sets passed `actionlint`.
- 2026-09-14: The existing production release completed Google OAuth and opened the authenticated archive with its three existing objects. The current public release still lacks the new CSP/HSTS and returns 401 from the expected readiness path, confirming that this PR commit and schema v16 are not deployed; this is baseline evidence, not acceptance of the repaired release.
- Required before closing release: real MinIO adversarial uploads/cleanup, post-deploy browser file workflows, mobile/keyboard checks, large-folder/upload load tests, scanner-limit tests, exact deployed commit/migration checks, rollback rehearsal, and recovery drills.

## P0: production-safe core drive

### Deployment and migration gate

- [ ] Add `DATABASE_URL` (app role) and `DATABASE_MIGRATION_URL` (migration owner) to GitHub Actions secrets.
- [ ] Rotate the database password disclosed during setup and reprovision both roles.
- [ ] Deploy migrations before the API container and verify migration version/readiness in production. The workflow now migrates first, waits up to five minutes for PostgreSQL, MinIO, and ClamAV-aware readiness, and restores the prior compose configuration on failure; the actual production rehearsal remains.
- [ ] Complete one real Google login against the repaired deployed callback and confirm one user, identity, personal drive, root, membership, and session are created. OAuth and archive loading pass on the pre-PR production release, but the post-deploy database invariants remain unverified.
- [ ] Run reconciliation for every existing user bucket and compare object counts, total bytes, and sampled ETags.
- [ ] Keep the old MinIO-only image available for rollback until reconciliation is signed off.

### Upload reliability

- [x] Replace the WebUI one-shot presigned PUT path with durable upload sessions and idempotency keys. Keep legacy routes only for the compatibility window.
- [x] Complete safe multipart recovery across browser reload. Recovery verifies a persistent SHA-256, refreshes accepted parts from MinIO, renews the session on each part batch, continues verification/scanning polls, and honours queued cancellation.
- [x] Validate and preserve final object identity before marking a version ready. Exact signed lengths, staged ETag, expected SHA-256, per-attempt final keys, and leased metadata publication are enforced; stale presigned PUT replay cannot alter the readable version.
- [x] Preserve relative paths for folder uploads and bound file-level concurrency. Folder metadata is created idempotently from shallowest to deepest before a three-worker upload queue starts.
- [ ] Reserve metadata before upload, then use an outbox/reconciler to repair blob/metadata partial failures. Aborted, expired, and size-mismatched sessions queue durable retryable blob-deletion jobs; owner-only metadata/MinIO audits report missing, orphaned, size-mismatched, and ETag-mismatched blobs without exposing raw orphan keys. Automatic repair remains intentionally gated.
- [ ] Verify per-user quota end to end in production. Local charging and serialization pass PostgreSQL tests, and real MinIO rejects a PUT whose signed content length differs from the reservation. Concurrent production clients and full storage reconciliation remain.
- [x] Add atomic keep-both and new-version duplicate-name policies without the old five-suffix ceiling. A pending revision leaves the prior current version readable until verified completion.
- [ ] Complete bounded ClamAV streaming and quarantine lifecycle. Exact immutable identity and worker claim fencing are repaired; production still needs explicit daemon stream/size limits and failure testing at the maximum upload boundary.

### File and folder operations

- [x] Add ID-based create, rename, move, copy, and details endpoints; retire key/prefix mutation parameters. The WebUI uses stable destination folder UUIDs for folder creation, uploads, nested folder uploads, copies, and a navigable move folder picker; production rejects prefix-only mutations and disables legacy key-based upload/copy/delete/presign routes. Path prefixes remain read-only navigation/search compatibility data.
- [ ] Make all operations transactional at the metadata layer with idempotent request IDs. Upload/copy reservations, folder creation, item rename/move, trash/restore/permanent deletion, star/unstar, version restore/retention, and the bounded batch API now use the request ledger with payload-conflict detection. Sharing and comments still need the common ledger.
- [x] Add collision handling, cyclic-move tests, invalid-name tests, and concurrent mutation tests. Active sibling names are enforced by PostgreSQL, rename/move maps unique conflicts to HTTP 409, and isolated integration coverage races two renames into one destination name.
- [x] Return opaque signed cursor pagination and deterministic sorting; retain offsets only when legacy callers explicitly request them.
- [x] Add batch operations and partial-failure reporting. The bounded 100-item API supports trash, restore, star, and unstar, rejects duplicates, returns stable per-item codes, and uses HTTP 207 for mixed outcomes.
- [x] Use native presigned GET range behavior for large media and force attachment downloads with RFC-compliant filenames.

### Open and preview

- [ ] Verify PDF, image, video, audio, text, Office, archive, HEIC/HEIF, RAW, and unknown-file behavior in browsers.
- [x] Move HEIC conversion out of request memory into bounded background preview jobs.
- [ ] Complete preview status, retry, size limits, effective timeouts, and version-scoped derivatives. Database job states and cached HEIC derivatives exist, but the timeout cannot terminate native decoding and browser behavior still lacks end-to-end coverage.
- [ ] Ensure unsupported files always offer a reliable download action.
- [x] Add explicit inline versus attachment Content-Disposition overrides to stable-ID preview, current download, historical version, and compatibility URLs.

### Trash and recovery

- [x] Build a real Trash page using metadata queries.
- [ ] Verify safe restore against MinIO and in the browser. Root fallback, keep-both naming, cancellation of unstarted deletion, and conflicts after the deletion boundary now have PostgreSQL coverage.
- [x] Add explicit permanent delete and Empty Trash with recent-login enforcement and confirmation.
- [x] Run a scheduled purge after 30 days that deletes MinIO versions only after the database transaction is committed.
- [ ] Verify purge retries, tombstone audit events, and safe restore/retention coordination end to end. Database claims now recheck deletion eligibility, irreversibly hide the version before storage work, and fence worker results. Storage failure/timeout and deployed recovery tests remain.

## P1: sharing and Drive workflows

### Permissions and sharing

- [x] Implement item permissions for viewer, commenter, and editor roles for existing users.
- [x] Implement inherited folder permissions for list, preview, and download through one ancestor-walking access query.
- [x] Add share-by-email invitations, pending users, resend/revoke, and notification delivery. Invitations are durable, token hashes are stored, the WebUI supports resend/revoke, and matching invitations auto-accept on first verified Google login. A bounded STARTTLS SMTP worker leases the outbox, recovers abandoned work, retries with backoff, and dead-letters exhausted jobs; real provider delivery remains a production acceptance gate.
- [ ] Complete hashed public link security, expiry, download controls, revocation, and abuse rate limits. Token hashing, scope, expiry, revocation, and no-index pages exist, but raw full-file preview URLs bypass `allow_download=false`, arbitrary files are iframed, and proxy peer addressing makes the limiter inaccurate.
- [x] Implement “Shared with me” and enforce permissions in every item, version, preview, download, search, and activity query. Shared listing/folder navigation, details, global search, preview/download/version/activity/comments, Recent/Starred state, revocation, and inherited editor renames have cross-user integration coverage.
- [ ] Add shared drives with owner/manager/content-manager/contributor/commenter/viewer semantics.
- [ ] Add permission regression tests preventing cross-user and cross-drive access. Isolated PostgreSQL coverage now proves non-members and item-level viewers cannot trash an owner’s item by stable ID, shared-item revocation removes search/recent/starred/version/activity access, shortcut targets do not leak after revocation, and inherited viewer/commenter/editor boundaries hold; the complete endpoint-by-role matrix remains.

### Drive navigation

- [x] Implement My Drive, Shared with me, Recent, Starred, Trash, and Storage views from live metadata.
- [x] Add list/grid modes, sort options, filters, breadcrumbs, details panel, and persistent view preferences. View mode, density, and sort order are stored per account in PostgreSQL and loaded on every signed-in browser.
- [x] Add keyboard selection and multi-select batch actions. Checkboxes, select-all, Cmd/Ctrl+A, Escape, a 100-item cap, and partial-failure-aware star/unstar/trash/restore controls are wired to the bounded batch API.
- [ ] Add shift-range selection, drag-and-drop move, context menus, and undo snackbars.
- [x] Add shortcuts with permission-aware target resolution and broken-target behavior. Creation is idempotent and collision-safe, targets cannot themselves be shortcuts, listings/search/recent/starred/shared/trash carry shortcut records, and resolution independently checks target access so revocation returns an explicit broken state without target disclosure.
- [x] Add folder colors and user item state. Folder colors are palette-validated metadata mutations, included in personal/shared/search/trash/public listings, rendered by the WebUI, and covered by isolated PostgreSQL integration tests; per-user starred/opened state already drives Starred and Recent.

### Search

- [x] Search files and folders, not only files.
- [x] Add filters for type, owner, location, modified date, shared state, starred state, and trash state. The API validates every filter, keeps filters cursor-bound, and applies access checks before returning matches; the WebUI exposes the user-facing filters while location IDs are ready for the folder picker.
- [ ] Add quoted phrases, exclusions, and Drive-like query chips.
- [ ] Add extracted-text indexing for supported document formats with bounded background jobs.
- [ ] Measure relevance and latency with realistic multi-user datasets.

### Versions, activity, and comments

- [x] Make new file versions immutable at the storage layer. Upload capabilities target staging only, each completion attempt has a unique final key, conditional sealing prevents source races, and the published key is never exposed for writes. Existing-version reconciliation and production acceptance remain release checks.
- [ ] Verify version retention and storage accounting in production. Ready current/history/trash versions and uploads awaiting verification/cleanup count toward quota. Keep-forever and version restore now share deletion locking and cancel unstarted work; claimed versions cannot become readable again.
- [ ] Record all mutations and security-sensitive reads in the activity ledger.
- [x] Build item activity UI with effective-access checks, actor, time, action, target context, and signed cursor pagination.
- [x] Implement comments, replies, resolve/reopen, edit/delete tombstones, and mentions. Viewer/commenter/editor inheritance is enforced; mention notifications are queued only for registered users who already have access, and all comment mutations write activity events atomically.

## P2: platform quality

### Security

- [ ] Add request and login rate limiting, brute-force protection, and sharing abuse controls. The API now has separate bounded login-IP and authenticated-user fixed windows with HTTP 429/Retry-After, and public links already have per-link/IP abuse limits; multi-instance shared counters and edge/WAF enforcement remain production gates.
- [ ] Add CSP, HSTS, Referrer-Policy, Permissions-Policy, and hardened content-type headers at the proxy. API headers and the WebUI's per-response nonce CSP/HSTS are implemented and locally inspected; deployed TLS-edge preservation remains.
- [x] Stop returning raw internal/database errors to clients; return stable codes/messages and log server failures with response request IDs.
- [x] Validate trusted reverse-proxy hops before accepting forwarded client IPs. The resolver accepts `X-Forwarded-For` only from configured CIDRs, walks the chain from the trusted edge, caps header size/hops, rejects malformed chains, and is used consistently by login and public-link limits. Production must verify that the configured Docker proxy range matches the observed socket peer.
- [x] Add strict trusted-Origin CSRF protection for cookie-authenticated mutations, including logout.
- [x] Add session/device management. Active sessions expose opaque IDs and device/IP/last-seen data, identify the current session by hash, support revoke-one and revoke-all-others, clear the cookie when revoking current, and write security activity events.
- [ ] Add account suspension and privacy-safe account deletion workflows, including asynchronous blob cleanup and export/delete confirmation.
- [ ] Add secret rotation runbooks and automated secret scanning.

### Reliability and observability

- [ ] Make every background workload bounded and forcibly interruptible. Worker counts, result fencing, and purge timeouts are implemented. Native libvips isolation and delayed source/derivative storage-write cleanup races remain unresolved.
- [ ] Add structured metrics and traces for auth, database, MinIO, uploads, previews, and reconciliation.
- [ ] Add SLOs and alerts for login, list, upload completion, download, and database readiness.
- [ ] Add PostgreSQL backups with point-in-time recovery and perform restore drills.
- [ ] Add MinIO replication/versioning policy and disaster-recovery drills.
- [x] Add owner-only metadata/blob consistency scans and bounded repair reports with opaque orphan references and audit-ledger events. Destructive repair is not automatic.

### Test coverage

- [ ] Unit-test path validation, pagination, config validation, cookies, CORS, and every repository permission rule. Core validation/cursor/config/request-ID/CSRF and cross-user permission inheritance/revocation are covered; full handler/permission matrix remains.
- [ ] Add handler contract tests for all status codes and response schemas.
- [ ] Complete real MinIO integration coverage for range download, preview, reconciliation, multipart race injection, and purge retries. Upload presign, exact length, stale replay, conditional sealing, and immutable destinations now run locally and in backend CI.
- [ ] Complete upload adversarial/state-machine coverage. Added presigned replay, length mismatch, ETag race, immutable destination, changed resume digest, distinct stale attempts, exhausted completion cleanup, adaptive 10,000-part sizing, quarantine quota, and infected/exhausted cleanup. Multipart complete/abort and client disconnect fault injection remain.
- [ ] Add Playwright flows for login, browse, upload, open, rename, move, share, trash, restore, and logout.
- [ ] Add browser tests for queued cancellation, reload resume, scan-pending/failed states, public no-download links, unsupported downloads, dialog focus, short mobile viewports, and text-preview range requests.
- [x] Add concurrent/race tests and run `go test -race` in CI. CI executes the entire backend with PostgreSQL under the race detector, and repository integration coverage includes concurrent sibling-name collision serialization.
- [ ] Add migration tests from every supported previous schema version with production-scale fixtures.
- [ ] Add load tests and budgets for 10,000+ item folders/search results, concurrent multipart uploads, completion latency, preview queues, and database pool saturation.

### Accessibility and UX

- [ ] Meet WCAG 2.2 AA for keyboard access, focus management, contrast, labels, dialogs, menus, and live upload announcements.
- [ ] Add responsive mobile/tablet layouts and touch interactions. A focusable modal mobile navigation drawer now restores all primary destinations, transfers, theme, account, sign-out, and New actions; browser/device touch verification and dense action-menu redesign remain.
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

# Production readiness implementation and verification

Status: active. Scope: both current working trees, the parity checklist, deployment pipelines, and authenticated verification at https://drive.sudarshanrajagopalan.one.

The user approved implementing the audit, pushing both repositories, watching deployment, signing in, exercising the product, and repeating repairs until verified. Existing uncommitted feature work is part of the candidate being reviewed. No production data or credentials belong in commits.

## Design

Retain Go/chi, PostgreSQL metadata, MinIO blobs, and the Next.js App Router. Repair invariants in their owning layers. Prefer durable upload/cleanup state and immutable blob identity over increasing timeouts or trusting browser state. Keep the original file downloadable while generating safe, bounded previews. Browser state must reflect verification and scanning, and transfers must survive navigation, cancellation, retries, and reload.

A rewrite would delay verification and risk losing existing behavior. Small isolated UI patches alone would leave the storage races intact. The chosen approach is staged repair with regression evidence for each invariant, followed by a coordinated release and real browser acceptance.

## Ordered work

1. Establish release evidence and fix stale dependency/security claims. Record branch/remotes, CI/deployment availability, isolated test infrastructure, and all remaining checklist requirements.
2. Repair upload lifecycle: immutable verified bytes, atomic completion/expiry/abort, renewable leases, adaptive multipart sizes, authoritative part recovery, checksum validation, bounded completion jobs, quarantine accounting and cleanup. Add real PostgreSQL/MinIO regression coverage.
3. Repair previews and browser transfers: bounded text reads, sandboxed active content, explicit unsupported/expired/error states, worker resource isolation, cancel/retry correctness, content-verified resume, scan state, queue persistence, and global concurrency.
4. Repair navigation/performance: true incremental pagination and server sorting, large-list rendering, modal/mobile keyboard behavior, route error/loading states, public-folder paging, and permission-aware actions. Test desktop and mobile widths.
5. Close remaining checklist workflows: mutation replay, sharing authorization matrix, versions/trash recovery, audit events, operational metrics, retention/backup/restore, and account lifecycle. Keep feature work and deployment-dependent verification distinct.
6. Gate both deployments on the exact verified commit; validate configuration, database migrations, image/runtime dependencies, health, rollback, private storage policies, proxy trust, and TLS/security headers. Do not deploy known data-loss paths.
7. Run unit, race, integration, contract, dependency, container, browser, accessibility, and load checks. Commit/push the reviewed candidate, wait on exact workflow runs, and inspect deployment evidence.
8. Sign in to the production site and run the acceptance matrix below. Fix failures, rerun relevant checks, push, await deployment, and repeat. Require user interaction only for credentials/MFA/account selection that cannot be completed from the available signed-in browser.

## Acceptance matrix

- Upload: empty, small, multipart, Unicode/long names, duplicate names/new versions, folders, large/slow transfers, parallel batches, disconnect/reload/resume, wrong-file resume, cancellation before/during/after upload, quarantine success/failure, quota exhaustion.
- Storage invariants: replay old PUT after verification, completion versus expiry/abort/purge, abandoned staging/multipart cleanup, checksums before/after copy/restore/download, denied cross-account access, inaccessible quarantine, backup and restore.
- Preview: image, HEIC/HEIF, PDF, audio, browser-supported and unsupported video, large text, HTML/SVG, Office, archive, RAW, unknown files; unsupported formats must have a truthful fallback. Downloads, ranges, expiry, revocation, and resource bounds are verified.
- Organization: create, rename, move, copy, star, recent, search/filter/sort, shortcuts, comments/activity, sharing/invitations/public links, versions, trash/restore/permanent delete, session revoke/logout.
- UI: mobile/desktop, scroll and keyboard focus, modal nesting, no invisible queued jobs, no stale scan success, incremental large folders, refresh/route transitions, offline/retry/error states.
- Release: exact commit and image, CI gates, migration version, readiness, authenticated smoke, deployment rollback, private MinIO/TLS/credential policies, metrics/alerts, and recovery evidence.

## Evidence ledger

- Initial inspection: API remote `suraj7026/SurajDrive`, frontend remote `suraj7026/suraj-drive-webui`; authenticated GitHub CLI is available. Both working trees contain uncommitted metadata/parity implementation. Previous deployed backend run predates the metadata foundation.
- Initial audit: Go tests/race/vet and frontend lint/typecheck/build passed. npm audit found one critical and two high package entries. No production/browser verification has yet established acceptance.
- No checklist item is closed solely because code exists. Record command results and browser/deployment evidence as stages finish.

### Continuation: locally tested repairs, 2026-09-13

- Added eleven further audit findings to the parity checklist, with code evidence, priority, and acceptance checks. Reopened restore/purge and version-retention claims because queued deletion can invalidate a restored or retained file. Distinguished confirmed defects from deployment verification gaps.
- Unified upload quota accounting across ready, quarantined, pending, expired, aborted, and failed states. Preserve the larger of expected/recorded bytes until blob deletion; serialize reservation/completion using the account row. Storage UI explains retained uploads and reservations.
- Reject elapsed upload completion. Expiry now transitions metadata and queues cleanup before storage work; the durable deletion worker handles multipart abort and bounds external work to one minute.
- Queue infected/exhausted scan cleanup, including final-attempt worker death. Keep a clean revision current while newer revisions await scanning. Repair trash queue starvation and exhausted deletion lease recovery.
- Reject Google provisioning for suspended/deleted accounts, preserving their state for returning identities and matching-email conflicts. OAuth returns forbidden for unavailable accounts.
- Text previews now request a bounded prefix, cancel ignored-Range streams, preserve UTF-8 boundaries, and abort on navigation/close/timeout. Added six unit tests and the WebUI CI test step.
- Upgraded Next.js/eslint-config-next to 16.3.5, sharp to 0.35.4, js-yaml to 4.3.2. Post-update unit/lint/typecheck/build passed; npm audit reports zero vulnerabilities. Existing Google Sans fallback warning remains.
- `go test -race -count=1 ./...` passed against a separate local PostgreSQL 16 container, including migrations and app-role tests. Follow-up repository/handler race tests and `go vet ./...` passed after the account-state repair. The temporary PostgreSQL container was removed after verification; no existing databases were changed.
- Deployment configuration gap confirmed by repository secret/variable name inventory: database app/migration URLs, SMTP settings, and the ClamAV image pin are missing. No secrets were printed or committed.

Next: immutable upload staging/completion claims and content-verified resume; restore/retention versus purge coordination and worker claim fencing; transfer cancellation/global ownership and browser UI checks; release configuration and gates. The active goal is not complete: nothing from this candidate has been pushed or accepted in production yet.

### Continuation: deletion coordination and worker claims, 2026-09-13

- Added migration 15: versions enter `deleting` before irreversible storage work. Previously attempted deletion jobs are conservatively moved across that boundary during migration. Downgrade leaves these versions failed/unreadable rather than claiming their bytes are intact.
- Restore, retention, version restore, preview publication, malware publication, and deletion claims coordinate through a transaction advisory lock per drive. Claims recheck trash/retention/quarantine eligibility. Restore cancels unstarted jobs and rejects file/folder restoration if current bytes are deleting/deleted.
- Added PostgreSQL tests for file and folder restore before/after claim, retry and completion; stale queue snapshots; retained versions; twelve restore/claim races; stale deletion attempts; and least-privilege schema-version reads.
- Fenced preview, malware, notification, and purge completion/failure by attempt and actual lease expiry. Preview artifacts have distinct keys per attempt. Added stale/reassigned and exhausted-lease tests for all worker families, plus preview publication versus purge. Native decoding and orphaned source/derivative write cleanup remain open.
- Readiness now checks migration-ledger version 15 via a fixed-search-path function. Fixed the verification gap where `TEST_APP_DATABASE_URL` was absent: CI now prepares a migrated isolated database and application role and runs the role test explicitly. The provisioner reapplies nonprivileged role flags even for an existing role.
- Both deployment workflows now call reusable CI and require it through `needs: verify`, with serialized production deployment. `actionlint` v1.7.12 validated both repositories' CI and deploy workflows.
- Upload/copy reservations now bind idempotency keys to canonical payload hashes in the common ledger, including copy source version. Tests cover changed payloads, concurrent conflicting requests, duplicate allocated names, legacy unbound-key rejection, and 200-character API keys. The WebUI uses UUID-only keys so long filenames cannot exceed the API key limit.
- Full Go race tests passed with both admin and application-role URLs supplied, including the new migration and repository tests. Vet passed. Frontend lint/typecheck passed after the UUID change. No candidate commit has been pushed/deployed.
- Docker became unavailable during the turn. Its log reports failure to install a privileged helper due to incorrect administrator credentials; opening Docker did not recover the engine. Installed Homebrew PostgreSQL 16.15 and started a separate temporary cluster with Unix-socket-only access at `/tmp/suraj-drive-pg.j5749T` (data subdirectory `data`). This test server is still running for the next upload integration stage. Admin URL: `postgres://postgres@/postgres?host=/tmp/suraj-drive-pg.j5749T&sslmode=disable`; app-role URL uses `drive_app` instead of `postgres`. These are local test fixtures, not production credentials. A Docker test container named `suraj-drive-lifecycle-postgres` was created before Docker became unavailable; inspect/clean it when the engine is available again.
- An optional Docker Hub manifest lookup hung and was terminated; no container base-image change was made. Container runtime/ABI validation remains open.
- Asked the user asynchronously for the approved credential location and SMTP sender because production migration/SMTP settings are missing. No response has arrived yet. Continue implementation independently; do not mark the goal blocked while useful repair work remains.

Next critical work: seal uploaded bytes into immutable storage identities, make completion recoverable and bounded outside HTTP, bind resume to content, refresh part manifests/leases, then finish transfer cancellation/UI and production release acceptance. The user's instructions to finish, push, await deployment, sign in, verify all workflows, and iterate remain active.

Upload design constraints for the next implementation stage:

- Presigned capabilities must target only per-session staging keys. Approved file versions must point to a separate final object written by the server. A stale completion worker must use a different final key from its replacement, so fencing metadata also fences the bytes a reader sees.
- Queue completion durably and return a processing status quickly. Stream staged bytes under an ETag condition into a unique final object while hashing/detecting type; publish only after size/checksum validation and the current job claim check. Avoid a second synchronous full-file GET in the HTTP handler.
- Record every attempted final/staging key before external work so rejected, cancelled, expired, crashed, and stale attempts can be cleaned durably. Retain a storage lifecycle safety net for abandoned staging/multipart objects. Do not allow cleanup to delete approved versions.
- Bind upload/resume to a complete content digest computed incrementally off the UI thread. Keep browser identity, API reservation, part recovery, and final verification consistent. Name/size-only recovery is insufficient.
- Sign/enforce expected part/object lengths where the storage protocol supports it; otherwise use an enforceable upload policy. A zero-byte reservation must not authorize arbitrarily large temporary storage. Verify the actual MinIO behavior with adversarial requests, not just signature generation.
- The installed MinIO SDK is `github.com/minio/minio-go/v7@v7.0.74`; its `PresignHeader` accepts extra signed headers. Inspect canonical-signing and HTTP handling and prove length enforcement against MinIO before relying on it. A Docker Hub manifest request could not complete in this environment; native/local storage test infrastructure is still needed while Docker's helper installation is unresolved.

### Continuation: immutable uploads and content-bound recovery, 2026-09-14

- Added migration 16 and a durable upload-completion worker. Browser PUT capabilities now target per-session `.uploads/` keys; worker attempts conditionally copy the staged ETag to distinct `.objects/.../attempt-N/content` keys. Only the current unexpired job attempt can publish its key. Failed final attempts fail the session/version and queue durable cleanup.
- Completion now returns quickly with `202` and moves hashing outside the HTTP write timeout. The worker validates exact size and the browser's expected SHA-256 before publishing, then queues delayed staging cleanup after all single-PUT URLs have expired. Purge also removes staging keys.
- The WebUI hashes `File.stream()` in a dedicated worker using incremental WASM, persists the digest, and compares it on reservation, reload resume, and completion. Already-uploaded verification and malware scanning continue across reload; the UI does not claim success until the version is ready.
- Single and multipart presigned URLs sign exact content length. Multipart part size adapts to remain within 10,000 parts, URLs last 30 minutes, each part batch renews the session for 45 minutes, and four parts upload concurrently. Resume loads an individual upload resource that refreshes its part manifest from MinIO.
- Queued cancellation is recorded before a page worker starts the job. Public previews now refuse arbitrary active content; raster image/audio/video are rendered by dedicated elements and PDFs use sandboxed no-referrer iframes.
- Installed an isolated native MinIO test server because Docker Desktop remains unavailable. The adversarial storage test proves wrong-length rejection, stale URL isolation, ETag-conditional sealing, and immutable destinations. Backend race tests (PostgreSQL plus MinIO), vet, frontend unit/lint/typecheck/build, and the official pinned MinIO image manifest passed locally. Backend CI now starts that MinIO image and runs the storage test.

Next: finish proxy trust/CSP and container/runtime controls, preview process isolation, global transfer ownership and full queue UI, true frontend pagination/virtualization, browser/accessibility/load coverage, production configuration, coordinated push/deploy, and authenticated production acceptance.

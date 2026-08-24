# Drive Metadata Database Design

Date: 2026-08-24  
Status: Approved architecture; awaiting written-spec review  
Owners: Suraj Drive backend and web UI

## 1. Objective

Make PostgreSQL the authoritative catalog for identity, file hierarchy, versions, permissions, sharing, trash, user state, and activity. Keep MinIO as blob storage. Preserve every existing MinIO object without rewriting or moving it during migration.

The first release remains compatible with the current personal-drive UI while establishing the data model needed for multi-user sharing and later Google Drive parity.

## 2. Current State

- Google OAuth profile fields are copied into a stateless JWT cookie. No user or session record is persisted.
- A MinIO bucket name is derived from the Google subject identifier.
- Object keys represent both hierarchy and identity.
- Folders are `.keep` placeholder objects.
- Listings and search scan MinIO directly.
- Duplicate uploads probe for `v1` through `v5` suffixes.
- Delete operations permanently remove MinIO objects.
- There is no permission, share, version, quota, activity, or trash model.
- The target PostgreSQL `drive` database is reachable over TLS, currently has no application tables, and the supplied administration role can create schema objects.

## 3. Design Principles

1. PostgreSQL is the source of truth for visible state; MinIO is the source of truth only for blob bytes.
2. Every user-visible resource has a stable public UUID independent of its name, parent, bucket, or object key.
3. File versions are immutable. Replacing a file creates a new version and changes a metadata pointer.
4. Rename and move are database transactions, not MinIO object copies.
5. Existing MinIO objects stay in place and receive catalog records through an idempotent importer.
6. New functionality is introduced through backward-compatible deployment phases with reconciliation before read cutover.
7. Authorization is enforced in the Go service. PostgreSQL roles provide least-privilege infrastructure isolation; row-level security is deferred until connection-pool-safe request identity propagation is designed.
8. User-facing list APIs use cursor pagination, never deep `OFFSET` pagination.
9. Public tokens and session credentials are stored only as cryptographic hashes.
10. No migration deletes legacy MinIO objects.

## 4. Architecture

```text
Browser
  -> Go HTTP API
       -> Auth/session service
       -> Metadata repository -> PostgreSQL
       -> Authorization service -> PostgreSQL
       -> Blob service -> MinIO
       -> Import/reconciliation worker -> PostgreSQL + MinIO
```

The Go service issues presigned MinIO requests only after PostgreSQL authorization. Clients never choose bucket names or authoritative object keys.

## 5. Identifier Strategy

Each table uses `BIGINT GENERATED ALWAYS AS IDENTITY` as its internal primary key. Resources exposed in URLs or APIs also have a `public_id UUID NOT NULL UNIQUE`.

- Internal bigint keys keep joins and indexes compact.
- Public UUIDs prevent sequential identifier enumeration and remain stable across rename, move, and import.
- The Go service generates UUIDv7 values for new public resources. Imported resources receive new UUIDv7 identifiers while retaining legacy storage locators.
- JWT subjects remain internal user public UUIDs after the auth migration; the Google subject moves to `oauth_identity.provider_subject`.

## 6. Database Namespace and Roles

Application objects live in a dedicated `drive` schema rather than `public`.

Required extensions:

- `pg_trgm` for indexed filename search.

Token generation and hashing happen in Go. Email addresses use `TEXT` with unique indexes on `lower(email)` so identity rules do not depend on an extension-specific type.

Roles:

- Migration owner: owns the `drive` schema and runs migrations; not used by the application.
- Application role: `CONNECT`, schema `USAGE`, and the minimum DML/sequence permissions needed by the Go service.
- Read-only operations role: optional future role for support and reporting.

The current PostgreSQL administration credential must not be used as the deployed application's `DATABASE_URL`.

## 7. Schema

All timestamps are `TIMESTAMPTZ NOT NULL`. Mutable tables have `created_at` and `updated_at`. State values use `TEXT` plus `CHECK` constraints rather than PostgreSQL enum types.

### 7.1 Identity and authentication

#### `user_account`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique, immutable |
| `primary_email` | `TEXT` | Not null; unique index on `lower(primary_email)` |
| `display_name` | `TEXT` | Not null |
| `picture_url` | `TEXT` | Nullable |
| `status` | `TEXT` | `active`, `suspended`, `deleted` |
| `storage_quota_bytes` | `BIGINT` | Non-negative; configured default |
| `last_login_at` | `TIMESTAMPTZ` | Nullable |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Deleting an account is a lifecycle operation, not a direct row delete. `status = 'deleted'` starts an asynchronous retention and purge process.

#### `oauth_identity`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `user_id` | `BIGINT` | FK to `user_account`, cascade |
| `provider` | `TEXT` | Initially `google` |
| `provider_subject` | `TEXT` | Not null |
| `provider_email` | `TEXT` | Not null |
| `email_verified` | `BOOLEAN` | Not null |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Unique constraints: `(provider, provider_subject)` and `(user_id, provider)`.

Google access and refresh tokens are not stored because the application needs only identity scopes.

#### `auth_session`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique; used as JWT `jti` |
| `user_id` | `BIGINT` | FK to `user_account`, cascade |
| `token_hash` | `BYTEA` | SHA-256 of the random session token/JTI secret material |
| `user_agent` | `TEXT` | Nullable, length-limited by application |
| `ip_address` | `INET` | Nullable |
| `expires_at` | `TIMESTAMPTZ` | Not null |
| `revoked_at` | `TIMESTAMPTZ` | Nullable |
| `last_seen_at` | `TIMESTAMPTZ` | Not null |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |

Only active, non-expired, non-revoked sessions authorize requests. Logout revokes the row and clears the cookie.

### 7.2 Drives and membership

#### `drive_space`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique |
| `kind` | `TEXT` | `personal` or `shared` |
| `name` | `TEXT` | Not null |
| `owner_user_id` | `BIGINT` | FK to user; required for personal drives |
| `storage_bucket` | `TEXT` | Not null; MinIO bucket used by this drive |
| `status` | `TEXT` | `active`, `suspended`, `deleting` |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Constraints:

- A user has at most one active personal drive.
- A personal drive has an owner.
- Bucket selection is server-controlled and never accepted from an API caller.

#### `drive_member`

| Column | Type | Rules |
| --- | --- | --- |
| `drive_id` | `BIGINT` | FK to drive, cascade |
| `user_id` | `BIGINT` | FK to user, cascade |
| `role` | `TEXT` | `viewer`, `commenter`, `editor`, `manager`, `owner` |
| `created_by_user_id` | `BIGINT` | FK to user, restrict |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Primary key: `(drive_id, user_id)`.

### 7.3 Items and hierarchy

#### `item`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique |
| `drive_id` | `BIGINT` | FK to drive, cascade |
| `parent_id` | `BIGINT` | Self FK, restrict; null only for drive root |
| `kind` | `TEXT` | `folder`, `file`, `shortcut` |
| `name` | `TEXT` | Not null; validated as one path segment |
| `owner_user_id` | `BIGINT` | FK to user, restrict |
| `shortcut_target_item_id` | `BIGINT` | Nullable self FK, restrict |
| `description` | `TEXT` | Nullable |
| `folder_color` | `TEXT` | Nullable validated token |
| `trashed_at` | `TIMESTAMPTZ` | Nullable |
| `trashed_by_user_id` | `BIGINT` | Nullable FK to user, set null |
| `purge_after` | `TIMESTAMPTZ` | Nullable; normally trash time plus 30 days |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Constraints:

- Exactly one root folder exists per drive through a partial unique index on `drive_id WHERE parent_id IS NULL`.
- A file must eventually have exactly one ready version marked current; folders and shortcuts have no versions.
- A shortcut must have a target and cannot target another shortcut.
- Parent and child must belong to the same drive, enforced by a composite FK using `(drive_id, parent_id)` to `(drive_id, id)`.
- Duplicate names are allowed because Google Drive permits them. All mutations address items by public UUID, never by path.
- Name length is capped by the application and database constraint; empty names, separators, control characters, `.` and `..` are rejected.
- Cycles are prevented by the move transaction, which checks ancestors before updating `parent_id`.

#### `file_version`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique |
| `item_id` | `BIGINT` | FK to file item, cascade |
| `version_number` | `BIGINT` | Positive, monotonically increasing per item |
| `state` | `TEXT` | `pending`, `ready`, `failed`, `quarantined`, `deleted` |
| `storage_bucket` | `TEXT` | Not null |
| `storage_key` | `TEXT` | Not null |
| `storage_etag` | `TEXT` | Nullable until completion |
| `size_bytes` | `BIGINT` | Non-negative; nullable until completion |
| `mime_type` | `TEXT` | Not null |
| `sha256` | `BYTEA` | Nullable until verified |
| `source_modified_at` | `TIMESTAMPTZ` | Nullable local source timestamp |
| `created_by_user_id` | `BIGINT` | FK to user, restrict |
| `keep_forever` | `BOOLEAN` | Defaults false |
| `is_current` | `BOOLEAN` | Defaults false; true for exactly one ready version per file |
| `legacy_object` | `BOOLEAN` | Defaults false |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `ready_at` | `TIMESTAMPTZ` | Nullable |

Unique constraints: `(item_id, version_number)` and `(storage_bucket, storage_key)`. A partial unique index on `(item_id) WHERE is_current AND state = 'ready'` enforces at most one current ready version without a circular foreign key.

New storage keys use `objects/{drive_public_id}/{item_public_id}/{version_public_id}`. Legacy records retain the original bucket and key.

### 7.4 Upload lifecycle

#### `upload_session`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique |
| `user_id` | `BIGINT` | FK to user, cascade |
| `drive_id` | `BIGINT` | FK to drive, cascade |
| `parent_item_id` | `BIGINT` | FK to folder, restrict |
| `item_id` | `BIGINT` | Nullable FK to reserved item, cascade |
| `file_version_id` | `BIGINT` | Nullable FK to pending version, cascade |
| `idempotency_key` | `TEXT` | Not null |
| `requested_name` | `TEXT` | Not null |
| `expected_size_bytes` | `BIGINT` | Non-negative |
| `expected_sha256` | `BYTEA` | Nullable |
| `mime_type` | `TEXT` | Not null |
| `minio_upload_id` | `TEXT` | Nullable for multipart upload |
| `status` | `TEXT` | `initiated`, `uploading`, `completing`, `completed`, `aborted`, `expired`, `failed` |
| `expires_at` | `TIMESTAMPTZ` | Not null |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Unique constraint: `(user_id, idempotency_key)`. Retrying an initiation returns the existing session.

### 7.5 Permissions and sharing

#### `item_permission`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `item_id` | `BIGINT` | FK to item, cascade |
| `grantee_user_id` | `BIGINT` | FK to user, cascade |
| `role` | `TEXT` | `viewer`, `commenter`, `editor` |
| `expires_at` | `TIMESTAMPTZ` | Nullable |
| `created_by_user_id` | `BIGINT` | FK to user, restrict |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |
| `updated_at` | `TIMESTAMPTZ` | Defaults to now |

Unique constraint: `(item_id, grantee_user_id)`.

Effective access is the greatest of personal ownership, drive membership, inherited ancestor permission, direct item permission, or a valid share link. A child cannot reduce permission inherited from a parent in the initial model.

#### `share_link`

| Column | Type | Rules |
| --- | --- | --- |
| `id` | `BIGINT` | Identity primary key |
| `public_id` | `UUID` | Unique |
| `item_id` | `BIGINT` | FK to item, cascade |
| `token_hash` | `BYTEA` | Unique; raw token returned once |
| `role` | `TEXT` | `viewer` or `commenter` initially |
| `allow_download` | `BOOLEAN` | Defaults true |
| `expires_at` | `TIMESTAMPTZ` | Nullable |
| `revoked_at` | `TIMESTAMPTZ` | Nullable |
| `created_by_user_id` | `BIGINT` | FK to user, restrict |
| `created_at` | `TIMESTAMPTZ` | Defaults to now |

Share URLs use the random token, not a drive, bucket, or item identifier by itself.

### 7.6 User state, collaboration, and audit

#### `user_item_state`

Primary key `(user_id, item_id)`. Stores `starred_at`, `last_opened_at`, and future user-specific preferences. Both foreign keys cascade.

#### `comment`

Stores `public_id`, `item_id`, optional `parent_comment_id`, author, body, created/updated timestamps, and `resolved_at`/`resolved_by_user_id`. Comment deletion is a tombstone so audit context remains.

#### `activity_event`

Append-only table containing bigint ID, public UUID, drive, nullable item/version, actor, event type, bounded JSONB details, request ID, IP address, and `created_at`.

Event types include login, upload lifecycle, open, download, create folder, rename, move, copy, trash, restore, purge, version restore, permission changes, share-link changes, comments, and quota rejection.

No foreign key cascade may delete activity. Referenced IDs are nullable or retained as public identifiers in event details. At high volume, this table can be range-partitioned by `created_at` without changing the API.

#### `legacy_import_record`

Stores drive, legacy bucket, legacy key, ETag, imported item/version, status, last error, and timestamps. Unique `(legacy_bucket, legacy_key, legacy_etag)` makes import retries idempotent.

## 8. Index Plan

All foreign-key columns receive indexes unless covered by a composite primary or unique key.

Hot-path indexes:

- `oauth_identity(provider, provider_subject)` unique for login.
- `auth_session(user_id, expires_at)` filtered to non-revoked sessions.
- `drive_space(owner_user_id)` unique and filtered to active personal drives.
- `drive_member(user_id, drive_id)` for Shared views.
- `item(drive_id, parent_id, kind, name, id)` filtered to non-trashed rows for folder listing and stable cursor pagination.
- `item(drive_id, trashed_at, id)` filtered to trashed rows for Trash.
- Trigram GIN index on `item.name` for filename search.
- `file_version(item_id, version_number DESC)`.
- `upload_session(user_id, status, expires_at)` for resumable queues and cleanup.
- `item_permission(grantee_user_id, item_id)` for Shared with me.
- `share_link(token_hash)` unique and filtered to non-revoked rows.
- `user_item_state(user_id, starred_at, item_id)` filtered to starred rows.
- `user_item_state(user_id, last_opened_at DESC, item_id)` for Recent.
- `activity_event(drive_id, created_at DESC, id DESC)` and `(item_id, created_at DESC, id DESC)`.
- `legacy_import_record(drive_id, status)` for reconciliation.

Indexes must be validated with representative `EXPLAIN (ANALYZE, BUFFERS)` plans before production cutover.

## 9. Core Data Flows

### 9.1 Login

1. Validate Google OAuth state and verified identity.
2. In one transaction, upsert `user_account` and `oauth_identity`, ensure the personal drive and root item exist, update `last_login_at`, and create `auth_session`.
3. Issue a short-lived JWT containing user public ID, session public ID, issuer, audience, and expiry.
4. On authenticated requests, validate the signature and active session row.
5. Start the user's idempotent legacy import asynchronously if reconciliation is incomplete.

### 9.2 List folder

1. Resolve authenticated user and requested drive/item public IDs.
2. Calculate effective permission.
3. Query direct child items from PostgreSQL using `(name, id)` or `(updated_at, id)` cursor pagination.
4. Join only the current ready version fields needed by the response.
5. Never list MinIO in the request path after read cutover.

### 9.3 Upload

1. Client requests an upload session with parent item UUID, name, MIME type, size, checksum when available, and idempotency key.
2. Transaction validates editor access and quota, reserves an item/version and immutable storage key, and creates `upload_session`.
3. Server returns single-part or multipart presigned operations.
4. Client uploads directly to MinIO.
5. Completion endpoint locks the upload session, verifies MinIO object metadata, clears the previous version's current flag, marks the new version ready and current, records activity, and commits.
6. Failed or expired sessions are aborted and pending metadata is reconciled by a cleanup worker.

An upload with the same name requires an explicit client choice: create a new version of a selected item or keep both as a separate item. The server never guesses by filename alone.

### 9.4 Rename and move

Rename updates `item.name`. Move validates destination editor permission and prevents hierarchy cycles, then updates `parent_id`. Both operations record activity in the same transaction. Blob storage does not change.

### 9.5 Trash and restore

Moving an item to trash sets `trashed_at`, actor, and purge deadline on the selected root item. Descendants are hidden through ancestor-aware queries. Restore clears the trash fields and retains the original parent when available; otherwise it restores to the drive root. A scheduled purge job first marks eligible versions `deleted`, removes their MinIO blobs idempotently, and only then removes or tombstones metadata after storage reconciliation succeeds. Failed blob deletion leaves retryable catalog state rather than an untracked orphan.

### 9.6 Share

Permission changes require owner/manager authority and execute transactionally with an activity event. Share links contain a high-entropy token; only its hash is stored. Download presigning occurs only after link and item policy validation.

## 10. Legacy Import

The importer runs per personal drive and never mutates MinIO.

1. Determine the user's legacy bucket through the current bucket derivation logic.
2. Recursively list objects, excluding internal preview artifacts from user-visible items.
3. Treat `.keep` markers as folders; synthesize missing ancestor folders for normal objects.
4. Create deterministic import work ordered by normalized path depth, then key.
5. Create one item and ready file version for each object, retaining bucket, key, ETag, size, MIME type, and modification time.
6. Record every result in `legacy_import_record`.
7. Re-run safely until database counts, keys, ETags, and byte totals reconcile with MinIO.

Duplicate object names are preserved. Invalid legacy names remain displayable but cannot be recreated until renamed. Orphan preview objects remain untouched and are reported for a later cleanup operation requiring separate approval.

During dual-read validation, production responses still come from MinIO while structured logs compare them with database results. Read cutover occurs per user only after reconciliation passes.

## 11. API Evolution

Existing path-based endpoints remain temporarily compatible. New APIs use public IDs:

- `GET /api/drives`
- `GET /api/drives/{driveId}/items?parent_id=&cursor=&limit=&sort=`
- `POST /api/items/folders`
- `PATCH /api/items/{itemId}` for rename/move/description/color
- `POST /api/items/{itemId}/copy`
- `POST /api/items/{itemId}/trash`
- `POST /api/items/{itemId}/restore`
- `DELETE /api/items/{itemId}` for authorized permanent purge only
- `GET /api/items/{itemId}/versions`
- `POST /api/items/{itemId}/versions/{versionId}/restore`
- `POST /api/uploads`
- `POST /api/uploads/{uploadId}/complete`
- `POST /api/uploads/{uploadId}/abort`
- `GET/POST/DELETE /api/items/{itemId}/permissions`
- `GET/POST/DELETE /api/items/{itemId}/share-links`
- `GET/POST /api/items/{itemId}/comments`
- `GET /api/search`
- `GET /api/activity`

Responses include stable item UUIDs and opaque cursors. Legacy bucket IDs and MinIO keys are removed from browser-visible routing after frontend migration.

## 12. Error Handling and Consistency

- Database operations use bounded contexts and explicit transactions.
- API errors expose stable machine codes and safe messages, not raw PostgreSQL or MinIO errors.
- MinIO and PostgreSQL cannot share a transaction. Upload and purge therefore use durable state machines plus reconciliation rather than pretending to be atomic.
- Completion is idempotent and uses row locks to prevent duplicate version activation.
- Activity recording is part of the same PostgreSQL transaction as the metadata mutation.
- Retryable errors are distinguished from conflicts, authorization failures, quota failures, and invalid input.
- Cleanup jobs retry with exponential backoff and retain terminal error details for operators.

## 13. Security

- Rotate the PostgreSQL password disclosed during connectivity testing before deployment.
- Store migration and application credentials in deployment secrets, never repository files.
- Require TLS for PostgreSQL and MinIO.
- Use a least-privilege application role, not the current superuser.
- Hash session and share-link tokens with SHA-256 over at least 256 bits of random input.
- Validate JWT issuer, audience, expiry, session ID, and user status.
- Rate-limit login, upload initiation/completion, search, sharing, and public-link access.
- Enforce permission checks before issuing every presigned URL.
- Add malware/quarantine state before shared downloads are considered complete.
- Avoid logging tokens, presigned URLs, database credentials, or sensitive object names at info level.

## 14. Migration and Rollback

### Phase 0: Foundations

- Rotate credentials and create migration/application roles.
- Add versioned, reversible SQL migrations.
- Create extensions, schema, tables, constraints, and indexes.
- Add database readiness checks and connection-pool metrics.

Rollback: application remains MinIO-only; unused additive schema can remain or be dropped by the down migration while empty.

### Phase 1: Persist identity

- Upsert users, OAuth identities, personal drives, roots, and sessions during login.
- Continue current MinIO file behavior.

Rollback: return to stateless JWT validation; preserve identity rows.

### Phase 2: Import and shadow reads

- Import existing buckets idempotently.
- Compare database listings/search with MinIO results without serving database results.

Rollback: disable import/shadow flags; no object changes occurred.

### Phase 3: Dual-write uploads

- Use upload sessions and immutable keys for new uploads.
- Maintain temporary compatibility responses for the existing frontend.

Rollback: stop new session initiation; completed blobs remain addressable through metadata and can be exported to legacy-compatible paths only through an explicit recovery tool.

### Phase 4: Metadata read cutover

- Switch reconciled users to PostgreSQL listing, search, preview authorization, and download authorization.
- Migrate frontend routes to drive/item UUIDs.

Rollback: switch affected users back to legacy reads while retaining dual-write records.

### Phase 5: Drive lifecycle features

- Enable rename, move, trash, restore, versions, stars, recent, and activity.

### Phase 6: Collaboration

- Enable members, direct permissions, share links, comments, notifications, and Shared with me.

No phase removes legacy objects. Cleanup is a later, separately approved operation after backups and reconciliation evidence.

## 15. Testing Strategy

### Migration tests

- Apply every up migration to an empty PostgreSQL 16 database.
- Apply down migrations where data-preserving rollback is valid.
- Upgrade a snapshot containing earlier schema versions.
- Assert constraints, indexes, privileges, and extension availability.

### Repository and service tests

- Unit-test authorization, cursor encoding, naming validation, quota calculations, hierarchy-cycle detection, and state transitions.
- Integration-test PostgreSQL repositories with real transactions and MinIO with isolated buckets.
- Contract-test old and new API responses during compatibility phases.
- Race-test simultaneous same-name upload initiation and completion.

### Import tests

- Empty bucket, nested paths, `.keep` folders, duplicate names, zero-byte files, special characters, hidden artifacts, and more than 1,000 objects.
- Interrupted import followed by safe resume.
- Repeated import produces no duplicate items or versions.
- Database and MinIO counts, keys, ETags, and byte totals reconcile.

### End-to-end tests

- Google login creates exactly one user, identity, personal drive, root, and session.
- Logout revokes the server session.
- Upload small and multipart files, interrupt/resume, retry completion, and replace as a new version.
- List more than 1,000 items without omission or duplication.
- Rename/move without changing blob location.
- Trash/restore nested folders and purge only after retention.
- Share/revoke file and folder access with inheritance.
- Expired/revoked public links fail immediately.

### Operational tests

- Database unavailable, MinIO unavailable, partial upload, checksum mismatch, importer interruption, and purge retry.
- Backup restore to a clean PostgreSQL instance plus validation that catalogued MinIO objects remain accessible.
- Load tests for listing, search, activity, permission evaluation, and concurrent upload completion.

## 16. Acceptance Criteria

The metadata foundation is complete when:

1. Migrations apply cleanly to PostgreSQL 16 using a non-superuser migration role.
2. The application connects with a least-privilege role over TLS.
3. Login persists users and revocable sessions without storing Google tokens.
4. Every existing MinIO object is catalogued or reported with an actionable import error; no object is moved or deleted.
5. Re-running import creates no duplicates.
6. New uploads create immutable versions and become visible only after verified completion.
7. Listings come from PostgreSQL with cursor pagination and return all items.
8. Rename and move do not copy blobs.
9. Delete defaults to recoverable 30-day trash.
10. Every metadata mutation is authorized, audited, and covered by integration tests.
11. Legacy read mode can be re-enabled until reconciliation and backup gates are complete.
12. Database credentials, tokens, and presigned URLs do not appear in source control or routine logs.

## 17. Improvement Roadmap After Metadata Cutover

1. Reliability: resumable multipart uploads, preview workers, checksums, reconciliation dashboards, dependency upgrades, and end-to-end CI gates.
2. Organization: rename, move, copy, trash, restore, versions, stars, recent, grid/list views, multi-select, and keyboard actions.
3. Discovery: indexed filename search, filters, owner/type/date/location search, and optional content extraction.
4. Collaboration: permissions, inherited folder access, public links, access requests, comments, notifications, and activity.
5. Capacity and safety: quotas, malware scanning, dangerous-file handling, retention jobs, backups, and disaster-recovery drills.
6. Extended parity: shared drives, offline/desktop sync, mobile clients, editor integration, and administrative controls.

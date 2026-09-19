package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	drivemigrations "surajdrive/backend/internal/database/migrations"
	pagecursor "surajdrive/backend/internal/pagination"
)

func TestAccountProvisioningAndSessionLifecycle(t *testing.T) {
	adminURL := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminDB, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	defer adminDB.Close()

	databaseName := fmt.Sprintf("drive_repository_test_%d", time.Now().UnixNano())
	quotedDatabaseName := pgx.Identifier{databaseName}.Sanitize()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+quotedDatabaseName); err != nil {
		t.Fatalf("create temporary database: %v", err)
	}
	defer func() {
		if _, err := adminDB.ExecContext(context.Background(), "DROP DATABASE "+quotedDatabaseName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop temporary database: %v", err)
		}
	}()

	targetConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	targetConfig.Database = databaseName
	migrationDB := stdlib.OpenDB(*targetConfig)
	goose.SetBaseFS(drivemigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpContext(ctx, migrationDB, "."); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if err := migrationDB.Close(); err != nil {
		t.Fatalf("close migration database: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse repository pool configuration: %v", err)
	}
	poolConfig.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open repository pool: %v", err)
	}
	defer pool.Close()
	repository := NewMetadata(pool)

	input := GoogleAccount{
		Subject:       "google-subject-1",
		Email:         "person@example.com",
		EmailVerified: true,
		Name:          "First Name",
		Picture:       "https://example.com/first.png",
		StorageBucket: "drive-google-subject-1",
	}
	principal, err := repository.ProvisionGoogleAccount(ctx, input)
	if err != nil {
		t.Fatalf("provision account: %v", err)
	}
	if principal.UserID == "" || principal.DriveID == "" || principal.StorageBucket != input.StorageBucket {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	preferences, err := repository.GetUserPreferences(ctx, principal.UserID)
	if err != nil {
		t.Fatalf("get default user preferences: %v", err)
	}
	if preferences.ViewMode != "list" || preferences.Density != "comfortable" || preferences.SortKey != "default" {
		t.Fatalf("unexpected default user preferences: %+v", preferences)
	}
	grid, compact, newest := "grid", "compact", "date-newest"
	preferences, err = repository.UpdateUserPreferences(ctx, UpdateUserPreferencesInput{
		UserPublicID: principal.UserID, ViewMode: &grid, Density: &compact, SortKey: &newest,
	})
	if err != nil {
		t.Fatalf("update user preferences: %v", err)
	}
	if preferences.ViewMode != grid || preferences.Density != compact || preferences.SortKey != newest {
		t.Fatalf("unexpected updated user preferences: %+v", preferences)
	}

	input.Name = "Updated Name"
	updatedPrincipal, err := repository.ProvisionGoogleAccount(ctx, input)
	if err != nil {
		t.Fatalf("reprovision account: %v", err)
	}
	if updatedPrincipal.UserID != principal.UserID || updatedPrincipal.DriveID != principal.DriveID {
		t.Fatal("reprovisioning changed stable public IDs")
	}
	if updatedPrincipal.Name != input.Name {
		t.Fatalf("expected updated profile name, got %q", updatedPrincipal.Name)
	}

	folderMarker := LegacyObject{
		Bucket:       input.StorageBucket,
		Key:          "Projects/.keep",
		ETag:         "folder-etag",
		MIMEType:     "application/x-directory",
		LastModified: time.Now(),
		FolderMarker: true,
	}
	if imported, err := repository.ImportLegacyObject(ctx, principal.DriveID, folderMarker); err != nil || !imported {
		t.Fatalf("import folder marker: imported=%v err=%v", imported, err)
	}
	fileObject := LegacyObject{
		Bucket:       input.StorageBucket,
		Key:          "Projects/plan.pdf",
		ETag:         "file-etag-1",
		SizeBytes:    4096,
		MIMEType:     "application/pdf",
		LastModified: time.Now(),
	}
	if imported, err := repository.ImportLegacyObject(ctx, principal.DriveID, fileObject); err != nil || !imported {
		t.Fatalf("import file: imported=%v err=%v", imported, err)
	}
	if imported, err := repository.ImportLegacyObject(ctx, principal.DriveID, fileObject); err != nil || imported {
		t.Fatalf("repeat import should skip: imported=%v err=%v", imported, err)
	}
	fileObject.ETag = "file-etag-2"
	fileObject.SizeBytes = 8192
	if imported, err := repository.ImportLegacyObject(ctx, principal.DriveID, fileObject); err != nil || !imported {
		t.Fatalf("refresh changed object: imported=%v err=%v", imported, err)
	}

	var itemCount, versionCount, importCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM drive.item").Scan(&itemCount); err != nil {
		t.Fatalf("count imported items: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM drive.file_version").Scan(&versionCount); err != nil {
		t.Fatalf("count imported versions: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM drive.legacy_import_record").Scan(&importCount); err != nil {
		t.Fatalf("count import records: %v", err)
	}
	if itemCount != 3 || versionCount != 1 || importCount != 3 {
		t.Fatalf("unexpected import counts: items=%d versions=%d records=%d", itemCount, versionCount, importCount)
	}
	freshObject := LegacyObject{
		Bucket:       input.StorageBucket,
		Key:          "fresh.txt",
		ETag:         "fresh-etag",
		SizeBytes:    12,
		MIMEType:     "text/plain",
		LastModified: time.Now(),
	}
	if recorded, err := repository.RecordStoredObject(ctx, principal.DriveID, freshObject); err != nil || !recorded {
		t.Fatalf("record new stored object: recorded=%v err=%v", recorded, err)
	}
	var freshIsLegacy bool
	if err := pool.QueryRow(ctx, `
		SELECT legacy_object FROM drive.file_version
		WHERE storage_bucket = $1 AND storage_key = $2
	`, freshObject.Bucket, freshObject.Key).Scan(&freshIsLegacy); err != nil {
		t.Fatalf("read new stored object metadata: %v", err)
	}
	if freshIsLegacy {
		t.Fatal("new uploads must not be marked as legacy objects")
	}
	var freshItemID string
	if err := pool.QueryRow(ctx, `
		SELECT item.public_id::text
		FROM drive.item item
		JOIN drive.file_version version ON version.item_id = item.id
		WHERE version.storage_bucket = $1 AND version.storage_key = $2
	`, freshObject.Bucket, freshObject.Key).Scan(&freshItemID); err != nil {
		t.Fatalf("resolve fresh item id: %v", err)
	}
	uploadFolder, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "", "Folder upload")
	if err != nil || uploadFolder.ID == "" || uploadFolder.Kind != "folder" {
		t.Fatalf("create folder upload metadata: folder=%+v err=%v", uploadFolder, err)
	}
	repeatedFolder, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "", "Folder upload")
	if err != nil || repeatedFolder.ID != uploadFolder.ID {
		t.Fatalf("idempotent folder creation changed identity: first=%+v repeated=%+v err=%v", uploadFolder, repeatedFolder, err)
	}
	rootByID, _, err := repository.ListDriveCursor(ctx, principal.DriveID, "", nil, 50)
	if err != nil || rootByID.CurrentFolderID == "" {
		t.Fatalf("drive listing did not return the current folder id: listing=%+v err=%v", rootByID, err)
	}
	shortcut, err := repository.CreateShortcut(ctx, CreateShortcutInput{
		UserPublicID: principal.UserID, TargetPublicID: freshItemID,
		ParentPublicID: rootByID.CurrentFolderID, Name: "Reference link", IdempotencyKey: "create-reference-shortcut",
	})
	if err != nil || shortcut.Kind != "shortcut" {
		t.Fatalf("create shortcut: item=%+v err=%v", shortcut, err)
	}
	replayedShortcut, err := repository.CreateShortcut(ctx, CreateShortcutInput{
		UserPublicID: principal.UserID, TargetPublicID: freshItemID,
		ParentPublicID: rootByID.CurrentFolderID, Name: "Reference link", IdempotencyKey: "create-reference-shortcut",
	})
	if err != nil || replayedShortcut != shortcut {
		t.Fatalf("replay shortcut creation: item=%+v err=%v", replayedShortcut, err)
	}
	shortcutListing, _, err := repository.ListDriveCursor(ctx, principal.DriveID, "", nil, 50)
	if err != nil || len(shortcutListing.Shortcuts) != 1 || shortcutListing.Shortcuts[0].ID != shortcut.ID {
		t.Fatalf("shortcut missing from drive listing: listing=%+v err=%v", shortcutListing, err)
	}
	resolvedShortcut, err := repository.ResolveShortcut(ctx, principal.UserID, shortcut.ID)
	if err != nil || resolvedShortcut.Broken || resolvedShortcut.Target == nil || resolvedShortcut.Target.ID != freshItemID {
		t.Fatalf("owner could not resolve shortcut: resolution=%+v err=%v", resolvedShortcut, err)
	}
	idFolder, err := repository.EnsureFolderByID(ctx, principal.DriveID, principal.UserID, rootByID.CurrentFolderID, "Folder upload")
	if err != nil || idFolder.ID != uploadFolder.ID {
		t.Fatalf("ID-based folder creation changed identity: first=%+v byID=%+v err=%v", uploadFolder, idFolder, err)
	}
	ledgerFolder, err := repository.EnsureFolderByIDIdempotent(
		ctx, principal.DriveID, principal.UserID, rootByID.CurrentFolderID, "Ledger folder", "create-ledger-folder",
	)
	if err != nil || ledgerFolder.ID == "" {
		t.Fatalf("create idempotent folder: folder=%+v err=%v", ledgerFolder, err)
	}
	replayedLedgerFolder, err := repository.EnsureFolderByIDIdempotent(
		ctx, principal.DriveID, principal.UserID, rootByID.CurrentFolderID, "Ledger folder", "create-ledger-folder",
	)
	if err != nil || replayedLedgerFolder != ledgerFolder {
		t.Fatalf("replay idempotent folder creation: folder=%+v err=%v", replayedLedgerFolder, err)
	}
	if _, err := repository.EnsureFolderByIDIdempotent(
		ctx, principal.DriveID, principal.UserID, rootByID.CurrentFolderID, "Changed ledger folder", "create-ledger-folder",
	); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed folder payload, got %v", err)
	}
	var folderCreatedActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id
		WHERE item.public_id = $1::uuid AND event.event_type = 'folder.created'
	`, ledgerFolder.ID).Scan(&folderCreatedActivityCount); err != nil || folderCreatedActivityCount != 1 {
		t.Fatalf("folder creation replay emitted duplicate activity: count=%d err=%v", folderCreatedActivityCount, err)
	}
	nestedFolder, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "Folder upload", "Nested")
	if err != nil || nestedFolder.ID == "" {
		t.Fatalf("create nested folder metadata: folder=%+v err=%v", nestedFolder, err)
	}
	if _, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "", freshObject.Key); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("folder creation should reject an existing file name, got %v", err)
	}
	versions, err := repository.ListFileVersions(ctx, principal.UserID, freshItemID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("list fresh file versions: versions=%+v err=%v", versions, err)
	}
	if !versions[0].IsCurrent || versions[0].SizeBytes != freshObject.SizeBytes || versions[0].ETag != freshObject.ETag {
		t.Fatalf("unexpected fresh file version: %+v", versions[0])
	}
	firstVersionID := versions[0].ID
	resolvedVersion, err := repository.ResolveAccessibleVersion(ctx, principal.UserID, freshItemID, firstVersionID)
	if err != nil || resolvedVersion.StorageKey != freshObject.Key || resolvedVersion.Bucket != freshObject.Bucket {
		t.Fatalf("resolve fresh file version: version=%+v err=%v", resolvedVersion, err)
	}
	if err := repository.SetVersionKeepForeverIdempotent(ctx, principal.UserID, freshItemID, firstVersionID, true, "retain-first-version"); err != nil {
		t.Fatalf("keep fresh file version forever: %v", err)
	}
	if err := repository.SetVersionKeepForeverIdempotent(ctx, principal.UserID, freshItemID, firstVersionID, true, "retain-first-version"); err != nil {
		t.Fatalf("replay version retention update: %v", err)
	}
	if err := repository.SetVersionKeepForeverIdempotent(ctx, principal.UserID, freshItemID, firstVersionID, false, "retain-first-version"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed retention payload, got %v", err)
	}
	var retentionActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.file_version version ON version.id = event.file_version_id
		WHERE version.public_id = $1::uuid AND event.event_type = 'version.retention_updated'
	`, firstVersionID).Scan(&retentionActivityCount); err != nil || retentionActivityCount != 1 {
		t.Fatalf("retention replay emitted duplicate activity: count=%d err=%v", retentionActivityCount, err)
	}
	versions, err = repository.ListFileVersions(ctx, principal.UserID, freshItemID)
	if err != nil || len(versions) != 1 || !versions[0].KeepForever {
		t.Fatalf("retained file version not reflected: versions=%+v err=%v", versions, err)
	}
	versionTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin version fixture transaction: %v", err)
	}
	if _, err := versionTx.Exec(ctx, `
		UPDATE drive.file_version SET is_current = false
		WHERE item_id = (SELECT id FROM drive.item WHERE public_id = $1::uuid) AND is_current
	`, freshItemID); err != nil {
		_ = versionTx.Rollback(ctx)
		t.Fatalf("retire first version fixture: %v", err)
	}
	var secondVersionID string
	if err := versionTx.QueryRow(ctx, `
		INSERT INTO drive.file_version (
			item_id, version_number, state, storage_bucket, storage_key, storage_etag,
			size_bytes, mime_type, created_by_user_id, is_current, ready_at
		)
		SELECT item.id, 2, 'ready', $2, $3, 'fresh-etag-v2', 16, 'text/plain', actor.id, true, now()
		FROM drive.item item
		JOIN drive.user_account actor ON actor.public_id = $4::uuid
		WHERE item.public_id = $1::uuid
		RETURNING public_id::text
	`, freshItemID, freshObject.Bucket, ".objects/version-test/content-v2", principal.UserID).Scan(&secondVersionID); err != nil {
		_ = versionTx.Rollback(ctx)
		t.Fatalf("insert second version fixture: %v", err)
	}
	if err := versionTx.Commit(ctx); err != nil {
		t.Fatalf("commit version fixture: %v", err)
	}
	if err := repository.RestoreFileVersionIdempotent(ctx, principal.UserID, freshItemID, firstVersionID, "restore-first-version"); err != nil {
		t.Fatalf("restore first file version: %v", err)
	}
	if err := repository.RestoreFileVersionIdempotent(ctx, principal.UserID, freshItemID, firstVersionID, "restore-first-version"); err != nil {
		t.Fatalf("replay first file version restore: %v", err)
	}
	if err := repository.RestoreFileVersionIdempotent(ctx, principal.UserID, freshItemID, secondVersionID, "restore-first-version"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed version restore payload, got %v", err)
	}
	var restoredVersionActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.file_version version ON version.id = event.file_version_id
		WHERE version.public_id = $1::uuid AND event.event_type = 'version.restored'
	`, firstVersionID).Scan(&restoredVersionActivityCount); err != nil || restoredVersionActivityCount != 1 {
		t.Fatalf("version restore replay emitted duplicate activity: count=%d err=%v", restoredVersionActivityCount, err)
	}
	versions, err = repository.ListFileVersions(ctx, principal.UserID, freshItemID)
	if err != nil || len(versions) != 2 || versions[0].IsCurrent || !versions[1].IsCurrent || versions[1].ID != firstVersionID {
		t.Fatalf("unexpected restored version history: versions=%+v err=%v", versions, err)
	}
	if err := repository.MarkItemOpened(ctx, principal.DriveID, principal.UserID, freshItemID); err != nil {
		t.Fatalf("mark item opened: %v", err)
	}
	if err := repository.SetItemStarredIdempotent(ctx, principal.UserID, freshItemID, true, "star-fresh-item"); err != nil {
		t.Fatalf("star item: %v", err)
	}
	if err := repository.SetItemStarredIdempotent(ctx, principal.UserID, freshItemID, true, "star-fresh-item"); err != nil {
		t.Fatalf("replay star item: %v", err)
	}
	if err := repository.SetItemStarredIdempotent(ctx, principal.UserID, freshItemID, false, "star-fresh-item"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed star payload, got %v", err)
	}
	var starredActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id
		WHERE item.public_id = $1::uuid AND event.event_type = 'item.starred'
	`, freshItemID).Scan(&starredActivityCount); err != nil || starredActivityCount != 1 {
		t.Fatalf("star replay emitted duplicate activity: count=%d err=%v", starredActivityCount, err)
	}
	for _, view := range []string{UserViewRecent, UserViewStarred, UserViewStorage} {
		listing, next, err := repository.ListUserViewCursor(ctx, principal.DriveID, principal.UserID, view, nil, 25)
		if err != nil {
			t.Fatalf("list %s view: %v", view, err)
		}
		if next != nil || listing.Pagination.Returned == 0 {
			t.Fatalf("unexpected %s pagination: %+v next=%+v", view, listing.Pagination, next)
		}
	}
	storageSummary, err := repository.GetStorageSummary(ctx, principal.DriveID, principal.UserID)
	if err != nil {
		t.Fatalf("get storage summary: %v", err)
	}
	if storageSummary.QuotaBytes <= 0 || storageSummary.CommittedBytes < freshObject.SizeBytes+16 || storageSummary.VersionBytes < 16 || storageSummary.ObjectCount == 0 {
		t.Fatalf("unexpected storage summary: %+v", storageSummary)
	}
	activityPage, activityCursor, err := repository.ListItemActivity(ctx, principal.UserID, freshItemID, nil, 1)
	if err != nil || len(activityPage) != 1 || activityCursor == nil {
		t.Fatalf("list first activity page: events=%+v cursor=%+v err=%v", activityPage, activityCursor, err)
	}
	nextActivityPage, _, err := repository.ListItemActivity(ctx, principal.UserID, freshItemID, activityCursor, 1)
	if err != nil || len(nextActivityPage) != 1 || nextActivityPage[0].ID == activityPage[0].ID {
		t.Fatalf("list next activity page: events=%+v err=%v", nextActivityPage, err)
	}
	if err := repository.MarkItemOpened(ctx, principal.DriveID, "00000000-0000-0000-0000-000000000001", freshItemID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("non-member item access should be denied as not found, got %v", err)
	}
	sharedUser, err := repository.ProvisionGoogleAccount(ctx, GoogleAccount{
		Subject: "google-subject-shared-user", Email: "shared@example.com", EmailVerified: true,
		Name: "Shared User", StorageBucket: "drive-google-subject-shared-user",
	})
	if err != nil {
		t.Fatalf("provision shared user: %v", err)
	}
	if _, err := repository.EnsureFolderByIDIdempotent(
		ctx, principal.DriveID, sharedUser.UserID, rootByID.CurrentFolderID, "Cross-user folder", "cross-user-folder-denied",
	); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("non-member should not create a folder in another drive, got %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, sharedUser.UserID, freshItemID, true, "unauthorized-trash"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("non-member should not trash an item by stable ID, got %v", err)
	}
	publicLink, publicToken, err := repository.CreateShareLink(ctx, principal.UserID, freshItemID, "viewer", false, nil)
	if err != nil || publicLink.ID == "" || publicToken == "" || publicLink.AllowDownload {
		t.Fatalf("create public share link: link=%+v token_present=%v err=%v", publicLink, publicToken != "", err)
	}
	publicLinks, err := repository.ListShareLinks(ctx, principal.UserID, freshItemID)
	if err != nil || len(publicLinks) != 1 || publicLinks[0].ID != publicLink.ID {
		t.Fatalf("list public share links: links=%+v err=%v", publicLinks, err)
	}
	publicItem, err := repository.ResolvePublicLink(ctx, publicToken)
	if err != nil || publicItem.ItemID != freshItemID || publicItem.AllowDownload {
		t.Fatalf("resolve public share link: item=%+v err=%v", publicItem, err)
	}
	if _, err := repository.ResolvePublicLinkFile(ctx, publicToken, freshItemID); err != nil {
		t.Fatalf("resolve root file through public link: %v", err)
	}
	if err := repository.RevokeShareLink(ctx, principal.UserID, freshItemID, publicLink.ID); err != nil {
		t.Fatalf("revoke public share link: %v", err)
	}
	if _, err := repository.ResolvePublicLink(ctx, publicToken); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("revoked public link should be inaccessible, got %v", err)
	}
	folderLink, folderToken, err := repository.CreateShareLink(ctx, principal.UserID, uploadFolder.ID, "viewer", true, nil)
	if err != nil {
		t.Fatalf("create public folder link: %v", err)
	}
	_, publicChildren, err := repository.ListPublicLinkChildren(ctx, folderToken, "")
	if err != nil || len(publicChildren.Folders) != 1 || publicChildren.Folders[0].ID != nestedFolder.ID {
		t.Fatalf("list public folder children: listing=%+v err=%v", publicChildren, err)
	}
	if err := repository.RevokeShareLink(ctx, principal.UserID, uploadFolder.ID, folderLink.ID); err != nil {
		t.Fatalf("revoke public folder link: %v", err)
	}
	pendingInvitation, err := repository.CreateShareInvitation(ctx, principal.UserID, freshItemID, "pending@example.com", "commenter")
	if err != nil || pendingInvitation.ID == "" || pendingInvitation.Status != "pending" {
		t.Fatalf("create pending share invitation: invitation=%+v err=%v", pendingInvitation, err)
	}
	pendingInvitations, err := repository.ListShareInvitations(ctx, principal.UserID, freshItemID)
	if err != nil || len(pendingInvitations) != 1 || pendingInvitations[0].ID != pendingInvitation.ID {
		t.Fatalf("list pending share invitations: invitations=%+v err=%v", pendingInvitations, err)
	}
	resentInvitation, err := repository.ResendShareInvitation(ctx, principal.UserID, freshItemID, pendingInvitation.ID)
	if err != nil || resentInvitation.ExpiresAt.Before(pendingInvitation.ExpiresAt) {
		t.Fatalf("resend pending share invitation: invitation=%+v err=%v", resentInvitation, err)
	}
	notificationJob, err := repository.ClaimNotificationJob(ctx, time.Minute)
	if err != nil || notificationJob == nil || notificationJob.Type != "share.invited" || notificationJob.Recipient != "pending@example.com" {
		t.Fatalf("claim invitation notification: job=%+v err=%v", notificationJob, err)
	}
	if err := repository.FailNotificationJob(ctx, notificationJob.ID, notificationJob.Attempt, errors.New("temporary SMTP failure")); err != nil {
		t.Fatalf("retry invitation notification: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE drive.notification_outbox
		SET run_after = CASE WHEN public_id = $1::uuid THEN now() ELSE now() + interval '1 hour' END
		WHERE status = 'queued'
	`, notificationJob.ID); err != nil {
		t.Fatalf("make notification retry due: %v", err)
	}
	retriedNotification, err := repository.ClaimNotificationJob(ctx, time.Minute)
	if err != nil || retriedNotification == nil || retriedNotification.ID != notificationJob.ID || retriedNotification.Attempt != 2 {
		t.Fatalf("reclaim invitation notification: job=%+v err=%v", retriedNotification, err)
	}
	if err := repository.CompleteNotificationJob(ctx, retriedNotification.ID, retriedNotification.Attempt); err != nil {
		t.Fatalf("complete invitation notification: %v", err)
	}
	pendingUser, err := repository.ProvisionGoogleAccount(ctx, GoogleAccount{
		Subject: "google-subject-pending-user", Email: "pending@example.com", EmailVerified: true,
		Name: "Pending User", StorageBucket: "drive-google-subject-pending-user",
	})
	if err != nil {
		t.Fatalf("provision invited user: %v", err)
	}
	if _, err := repository.ResolveAccessibleFile(ctx, pendingUser.UserID, freshItemID); err != nil {
		t.Fatalf("pending invitation was not accepted on login: %v", err)
	}
	revokedInvitation, err := repository.CreateShareInvitation(ctx, principal.UserID, freshItemID, "revoked@example.com", "viewer")
	if err != nil {
		t.Fatalf("create revocable invitation: %v", err)
	}
	if err := repository.RevokeShareInvitation(ctx, principal.UserID, freshItemID, revokedInvitation.ID); err != nil {
		t.Fatalf("revoke pending invitation: %v", err)
	}
	revokedUser, err := repository.ProvisionGoogleAccount(ctx, GoogleAccount{
		Subject: "google-subject-revoked-user", Email: "revoked@example.com", EmailVerified: true,
		Name: "Revoked User", StorageBucket: "drive-google-subject-revoked-user",
	})
	if err != nil {
		t.Fatalf("provision revoked invitee: %v", err)
	}
	if _, err := repository.ResolveAccessibleFile(ctx, revokedUser.UserID, freshItemID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("revoked invitation should not grant access, got %v", err)
	}
	expectedBlobs, err := repository.ListExpectedBlobs(ctx, principal.DriveID, principal.UserID)
	if err != nil || len(expectedBlobs) == 0 {
		t.Fatalf("list expected blob inventory: blobs=%+v err=%v", expectedBlobs, err)
	}
	if _, err := repository.ListExpectedBlobs(ctx, principal.DriveID, sharedUser.UserID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("non-member should not run drive consistency audit, got %v", err)
	}
	if err := repository.RecordConsistencyAudit(ctx, principal.DriveID, principal.UserID, len(expectedBlobs), len(expectedBlobs), 0); err != nil {
		t.Fatalf("record consistency audit: %v", err)
	}
	if _, err := repository.GrantItemPermission(ctx, principal.UserID, shortcut.ID, sharedUser.Email, "viewer", nil); err != nil {
		t.Fatalf("share shortcut without target: %v", err)
	}
	brokenShortcut, err := repository.ResolveShortcut(ctx, sharedUser.UserID, shortcut.ID)
	if err != nil || !brokenShortcut.Broken || brokenShortcut.Target != nil {
		t.Fatalf("shortcut should be broken before target access: resolution=%+v err=%v", brokenShortcut, err)
	}
	permission, err := repository.GrantItemPermission(ctx, principal.UserID, freshItemID, sharedUser.Email, "viewer", nil)
	if err != nil {
		t.Fatalf("grant item permission: %v", err)
	}
	if permission.ID == "" || permission.Role != "viewer" {
		t.Fatalf("unexpected permission: %+v", permission)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, sharedUser.UserID, freshItemID, true, "viewer-trash-denied"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer should not trash a shared item, got %v", err)
	}
	if _, err := repository.ResolveAccessibleFile(ctx, sharedUser.UserID, freshItemID); err != nil {
		t.Fatalf("shared user could not resolve file: %v", err)
	}
	resolvedSharedShortcut, err := repository.ResolveShortcut(ctx, sharedUser.UserID, shortcut.ID)
	if err != nil || resolvedSharedShortcut.Broken || resolvedSharedShortcut.Target == nil || resolvedSharedShortcut.Target.ID != freshItemID {
		t.Fatalf("shortcut did not recover after target access: resolution=%+v err=%v", resolvedSharedShortcut, err)
	}
	if item, err := repository.GetAccessibleItem(ctx, sharedUser.UserID, freshItemID); err != nil || item.ID != freshItemID {
		t.Fatalf("shared user could not load item details: item=%+v err=%v", item, err)
	}
	sharedSearch, _, err := repository.SearchAccessibleCursor(ctx, sharedUser.UserID, "fresh", nil, 25)
	if err != nil || len(sharedSearch.Results) != 1 || sharedSearch.Results[0].ID != freshItemID || !sharedSearch.Results[0].Shared {
		t.Fatalf("shared file missing from global search: results=%+v err=%v", sharedSearch, err)
	}
	if err := repository.MarkItemOpened(ctx, sharedUser.DriveID, sharedUser.UserID, freshItemID); err != nil {
		t.Fatalf("shared user could not mark shared item opened: %v", err)
	}
	if err := repository.SetItemStarred(ctx, sharedUser.DriveID, sharedUser.UserID, freshItemID, true); err != nil {
		t.Fatalf("shared user could not star shared item: %v", err)
	}
	advancedSharedSearch, _, err := repository.AdvancedSearchAccessibleCursor(ctx, sharedUser.UserID, "fresh", SearchFilters{
		Type: "file", Owner: principal.Email, Shared: "yes", Starred: "yes",
	}, nil, 25)
	if err != nil || len(advancedSharedSearch.Results) != 1 || advancedSharedSearch.Results[0].ID != freshItemID {
		t.Fatalf("advanced shared search filters did not return the accessible file: results=%+v err=%v", advancedSharedSearch, err)
	}
	advancedPrivateSearch, _, err := repository.AdvancedSearchAccessibleCursor(ctx, sharedUser.UserID, "fresh", SearchFilters{Shared: "no"}, nil, 25)
	if err != nil || len(advancedPrivateSearch.Results) != 0 {
		t.Fatalf("shared-state exclusion leaked a shared file: results=%+v err=%v", advancedPrivateSearch, err)
	}
	future := time.Now().Add(24 * time.Hour)
	advancedFutureSearch, _, err := repository.AdvancedSearchAccessibleCursor(ctx, sharedUser.UserID, "fresh", SearchFilters{ModifiedAfter: &future}, nil, 25)
	if err != nil || len(advancedFutureSearch.Results) != 0 {
		t.Fatalf("modified-after filter returned an older file: results=%+v err=%v", advancedFutureSearch, err)
	}
	for _, view := range []string{UserViewRecent, UserViewStarred} {
		sharedView, _, err := repository.ListUserViewCursor(ctx, sharedUser.DriveID, sharedUser.UserID, view, nil, 25)
		if err != nil || len(sharedView.Files) != 1 || sharedView.Files[0].ID != freshItemID {
			t.Fatalf("shared item missing from %s view: listing=%+v err=%v", view, sharedView, err)
		}
	}
	sharedVersions, err := repository.ListFileVersions(ctx, sharedUser.UserID, freshItemID)
	if err != nil || len(sharedVersions) != 2 || sharedVersions[1].ID != firstVersionID {
		t.Fatalf("shared user could not list versions: versions=%+v err=%v", sharedVersions, err)
	}
	if _, err := repository.ResolveAccessibleVersion(ctx, sharedUser.UserID, freshItemID, firstVersionID); err != nil {
		t.Fatalf("shared user could not resolve version: %v", err)
	}
	sharedActivity, _, err := repository.ListItemActivity(ctx, sharedUser.UserID, freshItemID, nil, 25)
	if err != nil || len(sharedActivity) == 0 {
		t.Fatalf("shared user could not list activity: events=%+v err=%v", sharedActivity, err)
	}
	if err := repository.SetVersionKeepForever(ctx, sharedUser.UserID, freshItemID, firstVersionID, false); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer should not change version retention, got %v", err)
	}
	if err := repository.RestoreFileVersion(ctx, sharedUser.UserID, freshItemID, secondVersionID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer should not restore a version, got %v", err)
	}
	sharedListing, next, err := repository.ListSharedWithMeCursor(ctx, sharedUser.UserID, nil, 25)
	if err != nil || next != nil || len(sharedListing.Files) != 1 || sharedListing.Files[0].ID != freshItemID {
		t.Fatalf("unexpected shared listing: listing=%+v next=%+v err=%v", sharedListing, next, err)
	}
	permissions, err := repository.ListItemPermissions(ctx, principal.UserID, freshItemID)
	if err != nil || len(permissions) != 2 {
		t.Fatalf("unexpected permission list: permissions=%+v err=%v", permissions, err)
	}
	if _, err := repository.GrantItemPermission(ctx, sharedUser.UserID, freshItemID, principal.Email, "editor", nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer should not manage sharing, got %v", err)
	}
	if err := repository.RevokeItemPermission(ctx, principal.UserID, freshItemID, permission.ID); err != nil {
		t.Fatalf("revoke item permission: %v", err)
	}
	if _, err := repository.ResolveAccessibleFile(ctx, sharedUser.UserID, freshItemID); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("revoked user should lose file access, got %v", err)
	}
	brokenShortcut, err = repository.ResolveShortcut(ctx, sharedUser.UserID, shortcut.ID)
	if err != nil || !brokenShortcut.Broken || brokenShortcut.Target != nil {
		t.Fatalf("shortcut did not break after target revocation: resolution=%+v err=%v", brokenShortcut, err)
	}
	sharedSearch, _, err = repository.SearchAccessibleCursor(ctx, sharedUser.UserID, "fresh", nil, 25)
	if err != nil || len(sharedSearch.Results) != 0 {
		t.Fatalf("revoked file remained in global search: results=%+v err=%v", sharedSearch, err)
	}
	for _, view := range []string{UserViewRecent, UserViewStarred} {
		sharedView, _, err := repository.ListUserViewCursor(ctx, sharedUser.DriveID, sharedUser.UserID, view, nil, 25)
		if err != nil || len(sharedView.Files) != 0 {
			t.Fatalf("revoked item remained in %s view: listing=%+v err=%v", view, sharedView, err)
		}
	}
	if _, _, err := repository.ListItemActivity(ctx, sharedUser.UserID, freshItemID, nil, 25); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("revoked user should lose activity access, got %v", err)
	}
	var projectsFolderID, planItemID string
	if err := pool.QueryRow(ctx, `
		SELECT folder.public_id::text, file.public_id::text
		FROM drive.item folder JOIN drive.item file ON file.parent_id = folder.id AND file.name = 'plan.pdf'
		WHERE folder.name = 'Projects' AND folder.drive_id = (SELECT id FROM drive.drive_space WHERE public_id = $1::uuid)
	`, principal.DriveID).Scan(&projectsFolderID, &planItemID); err != nil {
		t.Fatalf("resolve shared folder fixture: %v", err)
	}
	if _, err := repository.GrantItemPermission(ctx, principal.UserID, projectsFolderID, sharedUser.Email, "viewer", nil); err != nil {
		t.Fatalf("grant folder permission: %v", err)
	}
	children, next, err := repository.ListAccessibleChildrenCursor(ctx, sharedUser.UserID, projectsFolderID, nil, 25)
	if err != nil || next != nil || len(children.Files) != 1 || children.Files[0].ID != planItemID {
		t.Fatalf("unexpected shared folder children: children=%+v next=%+v err=%v", children, next, err)
	}
	if _, err := repository.ResolveAccessibleFile(ctx, sharedUser.UserID, planItemID); err != nil {
		t.Fatalf("inherited folder permission did not allow child file: %v", err)
	}
	if _, err := repository.CreateComment(ctx, sharedUser.UserID, planItemID, "", "viewer comment"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("viewer should not create comments, got %v", err)
	}
	rootComment, err := repository.CreateComment(ctx, principal.UserID, planItemID, "", "Please review this @shared@example.com")
	if err != nil || rootComment.ID == "" || !rootComment.CanResolve {
		t.Fatalf("create root comment: comment=%+v err=%v", rootComment, err)
	}
	sharedComments, err := repository.ListComments(ctx, sharedUser.UserID, planItemID)
	if err != nil || len(sharedComments) != 1 || sharedComments[0].CanReply {
		t.Fatalf("viewer comment listing was invalid: comments=%+v err=%v", sharedComments, err)
	}
	var mentionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.notification_outbox
		WHERE recipient_user_id = (SELECT id FROM drive.user_account WHERE public_id = $1::uuid)
			AND notification_type = 'comment.mentioned'
	`, sharedUser.UserID).Scan(&mentionCount); err != nil || mentionCount != 1 {
		t.Fatalf("comment mention was not queued exactly once: count=%d err=%v", mentionCount, err)
	}
	if _, err := repository.GrantItemPermission(ctx, principal.UserID, projectsFolderID, sharedUser.Email, "commenter", nil); err != nil {
		t.Fatalf("upgrade inherited permission to commenter: %v", err)
	}
	reply, err := repository.CreateComment(ctx, sharedUser.UserID, planItemID, rootComment.ID, "Reviewed")
	if err != nil || reply.ParentID != rootComment.ID || !reply.CanEdit {
		t.Fatalf("create comment reply: reply=%+v err=%v", reply, err)
	}
	if _, err := repository.SetCommentResolved(ctx, sharedUser.UserID, planItemID, rootComment.ID, true); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("commenter should not resolve a thread, got %v", err)
	}
	resolvedComment, err := repository.SetCommentResolved(ctx, principal.UserID, planItemID, rootComment.ID, true)
	if err != nil || resolvedComment.ResolvedAt == nil {
		t.Fatalf("resolve comment thread: comment=%+v err=%v", resolvedComment, err)
	}
	updatedReply, err := repository.UpdateComment(ctx, sharedUser.UserID, planItemID, reply.ID, "Reviewed and approved")
	if err != nil || updatedReply.Body != "Reviewed and approved" {
		t.Fatalf("update own reply: reply=%+v err=%v", updatedReply, err)
	}
	if err := repository.DeleteComment(ctx, sharedUser.UserID, planItemID, reply.ID); err != nil {
		t.Fatalf("delete own reply: %v", err)
	}
	commentsAfterDelete, err := repository.ListComments(ctx, sharedUser.UserID, planItemID)
	if err != nil || len(commentsAfterDelete) != 2 || !commentsAfterDelete[1].Deleted || commentsAfterDelete[1].Body != "" {
		t.Fatalf("deleted reply did not remain as a tombstone: comments=%+v err=%v", commentsAfterDelete, err)
	}
	sharedRename := "shared-editor-plan.pdf"
	if _, err := repository.UpdateItem(ctx, UpdateItemInput{
		UserPublicID: sharedUser.UserID, ItemPublicID: planItemID, Name: &sharedRename, IdempotencyKey: "commenter-rename-denied",
	}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("commenter should not rename shared item, got %v", err)
	}
	if _, err := repository.GrantItemPermission(ctx, principal.UserID, projectsFolderID, sharedUser.Email, "editor", nil); err != nil {
		t.Fatalf("upgrade inherited permission to editor: %v", err)
	}
	sharedUpdated, err := repository.UpdateItem(ctx, UpdateItemInput{
		UserPublicID: sharedUser.UserID, ItemPublicID: planItemID, Name: &sharedRename, IdempotencyKey: "shared-editor-rename",
	})
	if err != nil || sharedUpdated.Name != sharedRename {
		t.Fatalf("shared editor could not rename item: item=%+v err=%v", sharedUpdated, err)
	}
	originalSharedName := "plan.pdf"
	if _, err := repository.UpdateItem(ctx, UpdateItemInput{
		UserPublicID: sharedUser.UserID, ItemPublicID: planItemID, Name: &originalSharedName, IdempotencyKey: "shared-editor-restore-name",
	}); err != nil {
		t.Fatalf("shared editor could not restore item name: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM drive.legacy_import_record").Scan(&importCount); err != nil {
		t.Fatalf("recount import records: %v", err)
	}
	if importCount != 3 {
		t.Fatalf("new upload unexpectedly created a legacy import record: %d", importCount)
	}
	previewRequest, err := repository.GetOrQueuePreview(ctx, principal.DriveID, freshObject.Key, HEICPreviewProfile)
	if err != nil || previewRequest.Status != "queued" || previewRequest.VersionID == "" {
		t.Fatalf("queue preview: request=%+v err=%v", previewRequest, err)
	}
	previewJob, err := repository.ClaimPreviewJob(ctx, time.Minute)
	if err != nil || previewJob == nil || previewJob.ID != previewRequest.JobID {
		t.Fatalf("claim preview: job=%+v err=%v", previewJob, err)
	}
	previewKey := previewJob.ArtifactKey
	if err := repository.CompletePreviewJob(ctx, PreviewArtifactInput{
		JobID: previewJob.ID, Attempt: previewJob.Attempt, Bucket: input.StorageBucket, Key: previewKey,
		ETag: "preview-etag", SizeBytes: 100, MIMEType: "image/jpeg",
	}); err != nil {
		t.Fatalf("complete preview: %v", err)
	}
	completedPreview, err := repository.GetOrQueuePreview(ctx, principal.DriveID, freshObject.Key, HEICPreviewProfile)
	if err != nil || completedPreview.Status != "succeeded" || completedPreview.ArtifactKey != previewKey {
		t.Fatalf("load completed preview: request=%+v err=%v", completedPreview, err)
	}
	reservationInput := ReserveUploadInput{
		DrivePublicID:  principal.DriveID,
		UserPublicID:   principal.UserID,
		Bucket:         input.StorageBucket,
		ParentPublicID: rootByID.CurrentFolderID,
		Name:           "reserved.txt",
		SizeBytes:      25,
		MIMEType:       "text/plain",
		IdempotencyKey: "reservation-1",
		TTL:            time.Hour,
	}
	reservation, err := repository.ReserveUpload(ctx, reservationInput)
	if err != nil {
		t.Fatalf("reserve upload: %v", err)
	}
	repeatedReservation, err := repository.ReserveUpload(ctx, reservationInput)
	if err != nil || repeatedReservation.ID != reservation.ID || repeatedReservation.StorageKey != reservation.StorageKey {
		t.Fatalf("idempotent reservation changed: first=%+v repeated=%+v err=%v", reservation, repeatedReservation, err)
	}
	listingBeforeCompletion, _, err := repository.ListDriveCursor(ctx, principal.DriveID, "", nil, 50)
	if err != nil {
		t.Fatalf("list before upload completion: %v", err)
	}
	for _, file := range listingBeforeCompletion.Files {
		if file.ID == reservation.ID || file.Name == reservation.Name {
			t.Fatal("pending upload was exposed in drive listing")
		}
	}
	completedReservation, err := repository.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: principal.DriveID,
		UserPublicID:  principal.UserID,
		UploadID:      reservation.ID,
		ETag:          "reservation-etag",
		SizeBytes:     reservation.ExpectedSize,
		MIMEType:      reservation.MIMEType,
		LastModified:  time.Now(),
		SHA256:        make([]byte, 32),
	})
	if err != nil || completedReservation.Status != "completed" {
		t.Fatalf("complete upload reservation: reservation=%+v err=%v", completedReservation, err)
	}
	var storedChecksumLength int
	if err := pool.QueryRow(ctx, `
		SELECT octet_length(version.sha256)
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		WHERE session.public_id = $1::uuid
	`, reservation.ID).Scan(&storedChecksumLength); err != nil || storedChecksumLength != 32 {
		t.Fatalf("completed upload checksum missing: length=%d err=%v", storedChecksumLength, err)
	}
	revisionInput := reservationInput
	revisionInput.IdempotencyKey = "reservation-revision"
	revisionInput.ConflictMode = "new_version"
	revision, err := repository.ReserveUpload(ctx, revisionInput)
	if err != nil || revision.Name != reservation.Name || revision.StorageKey == reservation.StorageKey {
		t.Fatalf("reserve new file version: revision=%+v err=%v", revision, err)
	}
	if _, err := repository.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID, UploadID: revision.ID,
		ETag: "revision-etag", SizeBytes: revision.ExpectedSize, MIMEType: revision.MIMEType,
		LastModified: time.Now(), SHA256: make([]byte, 32),
	}); err != nil {
		t.Fatalf("complete new file version: %v", err)
	}
	var sameNameItems, readyVersions, currentVersions int
	if err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT item.id), count(version.id), count(version.id) FILTER (WHERE version.is_current)
		FROM drive.item item
		JOIN drive.file_version version ON version.item_id = item.id AND version.state = 'ready'
		WHERE item.drive_id = (SELECT id FROM drive.drive_space WHERE public_id = $1::uuid)
			AND item.name = $2
	`, principal.DriveID, reservation.Name).Scan(&sameNameItems, &readyVersions, &currentVersions); err != nil {
		t.Fatalf("inspect new file version: %v", err)
	}
	if sameNameItems != 1 || readyVersions != 2 || currentVersions != 1 {
		t.Fatalf("new version changed stable item identity: items=%d ready=%d current=%d", sameNameItems, readyVersions, currentVersions)
	}
	scanInput := reservationInput
	scanInput.IdempotencyKey = "reservation-scan-clean"
	scanInput.Name = "scan-clean.txt"
	scanReservation, err := repository.ReserveUpload(ctx, scanInput)
	if err != nil {
		t.Fatalf("reserve malware scan fixture: %v", err)
	}
	if _, err := repository.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID, UploadID: scanReservation.ID,
		ETag: "scan-clean-etag", SizeBytes: scanReservation.ExpectedSize, MIMEType: scanReservation.MIMEType,
		LastModified: time.Now(), SHA256: make([]byte, 32), RequireMalwareScan: true,
	}); err != nil {
		t.Fatalf("quarantine malware scan fixture: %v", err)
	}
	var quarantinedState, queuedScanStatus string
	if err := pool.QueryRow(ctx, `
		SELECT version.state, scan.status
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		JOIN drive.malware_scan_job scan ON scan.file_version_id = version.id
		WHERE session.public_id = $1::uuid
	`, scanReservation.ID).Scan(&quarantinedState, &queuedScanStatus); err != nil || quarantinedState != "quarantined" || queuedScanStatus != "queued" {
		t.Fatalf("unexpected quarantined upload: state=%s scan=%s err=%v", quarantinedState, queuedScanStatus, err)
	}
	scanJob, err := repository.ClaimMalwareScanJob(ctx, time.Minute)
	if err != nil || scanJob == nil || scanJob.StorageKey != scanReservation.FinalStorageKey {
		t.Fatalf("claim malware scan fixture: job=%+v err=%v", scanJob, err)
	}
	if err := repository.CompleteMalwareScanJob(ctx, scanJob.ID, scanJob.Attempt, "test-scanner", true, ""); err != nil {
		t.Fatalf("release clean scan fixture: %v", err)
	}
	var releasedState string
	var releasedCurrent bool
	if err := pool.QueryRow(ctx, `SELECT state, is_current FROM drive.file_version WHERE public_id = $1::uuid`, scanJob.VersionID).Scan(&releasedState, &releasedCurrent); err != nil || releasedState != "ready" || !releasedCurrent {
		t.Fatalf("clean scan was not released: state=%s current=%v err=%v", releasedState, releasedCurrent, err)
	}
	infectedInput := reservationInput
	infectedInput.IdempotencyKey = "reservation-scan-infected"
	infectedInput.Name = "scan-infected.txt"
	infectedReservation, err := repository.ReserveUpload(ctx, infectedInput)
	if err != nil {
		t.Fatalf("reserve infected scan fixture: %v", err)
	}
	if _, err := repository.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID, UploadID: infectedReservation.ID,
		ETag: "scan-infected-etag", SizeBytes: infectedReservation.ExpectedSize, MIMEType: infectedReservation.MIMEType,
		LastModified: time.Now(), SHA256: make([]byte, 32), RequireMalwareScan: true,
	}); err != nil {
		t.Fatalf("quarantine infected scan fixture: %v", err)
	}
	infectedJob, err := repository.ClaimMalwareScanJob(ctx, time.Minute)
	if err != nil || infectedJob == nil {
		t.Fatalf("claim infected scan fixture: job=%+v err=%v", infectedJob, err)
	}
	if err := repository.CompleteMalwareScanJob(ctx, infectedJob.ID, infectedJob.Attempt, "test-scanner", false, "Eicar-Test-Signature"); err != nil {
		t.Fatalf("record infected scan fixture: %v", err)
	}
	var infectedState, infectedStatus, infectedSignature string
	if err := pool.QueryRow(ctx, `
		SELECT version.state, scan.status, scan.signature
		FROM drive.file_version version JOIN drive.malware_scan_job scan ON scan.file_version_id = version.id
		WHERE version.public_id = $1::uuid
	`, infectedJob.VersionID).Scan(&infectedState, &infectedStatus, &infectedSignature); err != nil || infectedState != "quarantined" || infectedStatus != "infected" || infectedSignature != "Eicar-Test-Signature" {
		t.Fatalf("infected version escaped quarantine: state=%s status=%s signature=%s err=%v", infectedState, infectedStatus, infectedSignature, err)
	}
	infectedDeletion, err := repository.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || infectedDeletion == nil || infectedDeletion.VersionID != infectedJob.VersionID {
		t.Fatalf("infected upload cleanup missing: job=%+v err=%v", infectedDeletion, err)
	}
	if err := repository.CompleteBlobDeletionJob(ctx, infectedDeletion.ID, infectedDeletion.Attempt); err != nil {
		t.Fatalf("complete infected upload cleanup: %v", err)
	}
	collisionInput := reservationInput
	collisionInput.IdempotencyKey = "reservation-2"
	collision, err := repository.ReserveUpload(ctx, collisionInput)
	if err != nil {
		t.Fatalf("reserve colliding upload: %v", err)
	}
	if collision.Name != "reserved (1).txt" || !strings.HasPrefix(collision.StorageKey, ".uploads/") || collision.StorageKey == reservation.StorageKey {
		t.Fatalf("unexpected collision reservation: %+v", collision)
	}
	if err := repository.AbortUpload(ctx, principal.DriveID, principal.UserID, collision.ID); err != nil {
		t.Fatalf("abort upload reservation: %v", err)
	}
	if err := repository.AbortUpload(ctx, principal.DriveID, principal.UserID, collision.ID); err != nil {
		t.Fatalf("repeat upload abort should be idempotent: %v", err)
	}
	var abortedStatus, abortedVersionState, abortedDeletionStatus string
	if err := pool.QueryRow(ctx, `
		SELECT session.status, version.state, deletion.status
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		JOIN drive.blob_deletion_job deletion ON deletion.file_version_id = version.id
		WHERE session.public_id = $1::uuid
	`, collision.ID).Scan(&abortedStatus, &abortedVersionState, &abortedDeletionStatus); err != nil {
		t.Fatalf("load aborted upload state: %v", err)
	}
	if abortedStatus != "aborted" || abortedVersionState != "failed" || abortedDeletionStatus != "queued" {
		t.Fatalf("unexpected aborted upload state: session=%s version=%s deletion=%s", abortedStatus, abortedVersionState, abortedDeletionStatus)
	}
	abortedDeletion, err := repository.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || abortedDeletion == nil || abortedDeletion.StorageKey != collision.FinalStorageKey || abortedDeletion.StagingKey != collision.StorageKey {
		t.Fatalf("claim aborted upload cleanup: job=%+v err=%v", abortedDeletion, err)
	}
	if err := repository.CompleteBlobDeletionJob(ctx, abortedDeletion.ID, abortedDeletion.Attempt); err != nil {
		t.Fatalf("complete aborted upload cleanup: %v", err)
	}
	multipartInput := reservationInput
	multipartInput.IdempotencyKey = "multipart-reservation"
	multipartInput.Name = "large-video.mp4"
	multipartInput.SizeBytes = 10 << 20
	multipartInput.MIMEType = "video/mp4"
	multipartInput.UploadMode = "multipart"
	multipartInput.PartSize = 5 << 20
	multipart, err := repository.ReserveUpload(ctx, multipartInput)
	if err != nil {
		t.Fatalf("reserve multipart upload: %v", err)
	}
	if multipart.UploadMode != "multipart" || multipart.PartSize != 5<<20 {
		t.Fatalf("unexpected multipart reservation: %+v", multipart)
	}
	attachedID, err := repository.AttachMultipartUpload(ctx, principal.DriveID, principal.UserID, multipart.ID, "minio-upload-test")
	if err != nil || attachedID != "minio-upload-test" {
		t.Fatalf("attach multipart upload: id=%q err=%v", attachedID, err)
	}
	if err := repository.ReplaceUploadParts(ctx, principal.DriveID, principal.UserID, multipart.ID, []UploadPart{
		{Number: 1, ETag: "part-1", Size: 5 << 20},
		{Number: 2, ETag: "part-2", Size: 5 << 20},
	}); err != nil {
		t.Fatalf("record multipart parts: %v", err)
	}
	storedMultipart, err := repository.LoadUpload(ctx, principal.DriveID, principal.UserID, multipart.ID)
	if err != nil || storedMultipart.MinIOUploadID != "minio-upload-test" || storedMultipart.UploadMode != "multipart" {
		t.Fatalf("load multipart upload: upload=%+v err=%v", storedMultipart, err)
	}
	var recordedPartCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM drive.upload_part part
		JOIN drive.upload_session session ON session.id = part.upload_session_id
		WHERE session.public_id = $1::uuid
	`, multipart.ID).Scan(&recordedPartCount); err != nil || recordedPartCount != 2 {
		t.Fatalf("unexpected recorded multipart part count: count=%d err=%v", recordedPartCount, err)
	}
	activeUploads, err := repository.ListActiveUploads(ctx, principal.DriveID, principal.UserID)
	if err != nil {
		t.Fatalf("list active uploads: %v", err)
	}
	foundActiveMultipart := false
	for _, activeUpload := range activeUploads {
		if activeUpload.ID == multipart.ID {
			foundActiveMultipart = len(activeUpload.UploadedParts) == 2
		}
	}
	if !foundActiveMultipart {
		t.Fatalf("active multipart upload was not recoverable: %+v", activeUploads)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE drive.upload_session
		SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
		WHERE public_id = $1::uuid
	`, multipart.ID); err != nil {
		t.Fatalf("age multipart upload: %v", err)
	}
	expiredUploads, err := repository.ListExpiredUploads(ctx, 50)
	if err != nil {
		t.Fatalf("list expired uploads: %v", err)
	}
	foundExpired := false
	for _, expired := range expiredUploads {
		if expired.ID == multipart.ID {
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatal("expired multipart upload was not returned for cleanup")
	}
	if err := repository.MarkUploadExpired(ctx, multipart.ID); err != nil {
		t.Fatalf("mark upload expired: %v", err)
	}
	var expiredStatus, expiredVersionState, expiredDeletionStatus string
	if err := pool.QueryRow(ctx, `
		SELECT session.status, version.state, deletion.status
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		JOIN drive.blob_deletion_job deletion ON deletion.file_version_id = version.id
		WHERE session.public_id = $1::uuid
	`, multipart.ID).Scan(&expiredStatus, &expiredVersionState, &expiredDeletionStatus); err != nil {
		t.Fatalf("load expired upload state: %v", err)
	}
	if expiredStatus != "expired" || expiredVersionState != "failed" || expiredDeletionStatus != "queued" {
		t.Fatalf("unexpected expired upload state: session=%s version=%s deletion=%s", expiredStatus, expiredVersionState, expiredDeletionStatus)
	}
	tooLarge := reservationInput
	tooLarge.IdempotencyKey = "reservation-over-quota"
	tooLarge.Name = "too-large.bin"
	tooLarge.SizeBytes = 16_106_127_361
	if _, err := repository.ReserveUpload(ctx, tooLarge); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
	rootListing, err := repository.ListDrive(ctx, principal.DriveID, "", 0, 50)
	if err != nil {
		t.Fatalf("list drive root: %v", err)
	}
	folderNames := make(map[string]string, len(rootListing.Folders))
	for _, folder := range rootListing.Folders {
		folderNames[folder.Name] = folder.ID
	}
	if folderNames["Projects"] == "" || folderNames["Folder upload"] != uploadFolder.ID {
		t.Fatalf("unexpected root listing: %+v", rootListing)
	}
	projectListing, err := repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil {
		t.Fatalf("list imported folder: %v", err)
	}
	if len(projectListing.Files) != 1 || projectListing.Files[0].Name != "plan.pdf" || projectListing.Files[0].Size != 8192 || projectListing.Files[0].ID == "" {
		t.Fatalf("unexpected project listing: %+v", projectListing)
	}
	searchResults, err := repository.SearchDrive(ctx, principal.DriveID, "", "plan", 0, 50)
	if err != nil {
		t.Fatalf("search imported file: %v", err)
	}
	if len(searchResults.Results) != 1 || searchResults.Results[0].ID != projectListing.Files[0].ID {
		t.Fatalf("unexpected search results: %+v", searchResults)
	}
	trashedFileID, err := repository.TrashFileByStorageKey(ctx, principal.DriveID, principal.UserID, fileObject.Key)
	if err != nil {
		t.Fatalf("trash file by storage key: %v", err)
	}
	projectListing, err = repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil || len(projectListing.Files) != 0 {
		t.Fatalf("trashed file remained visible: listing=%+v err=%v", projectListing, err)
	}
	trashListing, _, err := repository.ListTrashCursor(ctx, principal.DriveID, nil, 50)
	if err != nil || len(trashListing.Files) != 1 || trashListing.Files[0].ID != trashedFileID {
		t.Fatalf("trashed file missing from trash: listing=%+v err=%v", trashListing, err)
	}
	if err := repository.SetItemTrashed(ctx, principal.DriveID, principal.UserID, trashedFileID, false); err != nil {
		t.Fatalf("restore file: %v", err)
	}
	trashedFolderID, err := repository.TrashFolderByPrefix(ctx, principal.DriveID, principal.UserID, "Projects")
	if err != nil {
		t.Fatalf("trash folder by prefix: %v", err)
	}
	rootListing, err = repository.ListDrive(ctx, principal.DriveID, "", 0, 50)
	projectsVisible := false
	for _, folder := range rootListing.Folders {
		projectsVisible = projectsVisible || folder.Name == "Projects"
	}
	if err != nil || projectsVisible {
		t.Fatalf("trashed folder remained visible: listing=%+v err=%v", rootListing, err)
	}
	trashListing, _, err = repository.ListTrashCursor(ctx, principal.DriveID, nil, 50)
	if err != nil || len(trashListing.Folders) != 1 || trashListing.Folders[0].ID != trashedFolderID || len(trashListing.Files) != 0 {
		t.Fatalf("trash did not collapse trashed subtree: listing=%+v err=%v", trashListing, err)
	}
	if err := repository.SetItemTrashed(ctx, principal.DriveID, principal.UserID, trashedFolderID, false); err != nil {
		t.Fatalf("restore folder tree: %v", err)
	}
	projectListing, err = repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil || len(projectListing.Files) != 1 {
		t.Fatalf("restored folder tree is incomplete: listing=%+v err=%v", projectListing, err)
	}
	renamed := "renamed-plan.pdf"
	updatedItem, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: projectListing.Files[0].ID, Name: &renamed, IdempotencyKey: "owner-rename-plan",
	})
	if err != nil || updatedItem.Name != renamed {
		t.Fatalf("rename item: item=%+v err=%v", updatedItem, err)
	}
	replayedItem, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: projectListing.Files[0].ID, Name: &renamed, IdempotencyKey: "owner-rename-plan",
	})
	if err != nil || replayedItem != updatedItem {
		t.Fatalf("replayed rename did not return the original result: item=%+v err=%v", replayedItem, err)
	}
	differentName := "different-plan.pdf"
	if _, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: projectListing.Files[0].ID, Name: &differentName, IdempotencyKey: "owner-rename-plan",
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for a changed rename payload, got %v", err)
	}
	projectListing, err = repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil || projectListing.Files[0].Name != renamed || projectListing.Files[0].Key != fileObject.Key {
		t.Fatalf("rename changed storage identity or failed: listing=%+v err=%v", projectListing, err)
	}
	originalName := "plan.pdf"
	if _, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: projectListing.Files[0].ID, Name: &originalName, IdempotencyKey: "owner-restore-plan-name",
	}); err != nil {
		t.Fatalf("restore item name: %v", err)
	}
	nestedMarker := LegacyObject{
		Bucket: input.StorageBucket, Key: "Projects/Nested/.keep", ETag: "nested-folder-etag",
		MIMEType: "application/x-directory", LastModified: time.Now(), FolderMarker: true,
	}
	if recorded, err := repository.RecordStoredObject(ctx, principal.DriveID, nestedMarker); err != nil || !recorded {
		t.Fatalf("create nested folder fixture: recorded=%v err=%v", recorded, err)
	}
	nestedListing, err := repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil || len(nestedListing.Folders) != 1 {
		t.Fatalf("load nested folder fixture: listing=%+v err=%v", nestedListing, err)
	}
	nestedParentID := nestedListing.Folders[0].ID
	rootAfterRestore, err := repository.ListDrive(ctx, principal.DriveID, "", 0, 50)
	var restoredProjectsID string
	for _, folder := range rootAfterRestore.Folders {
		if folder.Name == "Projects" {
			restoredProjectsID = folder.ID
		}
	}
	if err != nil || restoredProjectsID == "" {
		t.Fatalf("load folder for cycle test: listing=%+v err=%v", rootAfterRestore, err)
	}
	collisionA, err := repository.EnsureFolderByID(ctx, principal.DriveID, principal.UserID, rootAfterRestore.CurrentFolderID, "Collision A")
	if err != nil {
		t.Fatalf("create first collision fixture: %v", err)
	}
	collisionB, err := repository.EnsureFolderByID(ctx, principal.DriveID, principal.UserID, rootAfterRestore.CurrentFolderID, "Collision B")
	if err != nil {
		t.Fatalf("create second collision fixture: %v", err)
	}
	blue := "blue"
	coloredFolder, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: collisionA.ID, FolderColor: &blue, IdempotencyKey: "color-collision-folder",
	})
	if err != nil || coloredFolder.FolderColor != blue {
		t.Fatalf("set folder color: item=%+v err=%v", coloredFolder, err)
	}
	coloredListing, _, err := repository.ListDriveCursor(ctx, principal.DriveID, "", nil, 100)
	if err != nil {
		t.Fatalf("list colored folder: %v", err)
	}
	foundColoredFolder := false
	for _, folder := range coloredListing.Folders {
		if folder.ID == collisionA.ID && folder.FolderColor == blue {
			foundColoredFolder = true
		}
	}
	if !foundColoredFolder {
		t.Fatalf("folder color missing from listing: %+v", coloredListing.Folders)
	}
	collisionName := "Collision Target"
	startCollision := make(chan struct{})
	collisionResults := make(chan error, 2)
	for index, itemID := range []string{collisionA.ID, collisionB.ID} {
		go func(index int, itemID string) {
			<-startCollision
			_, updateErr := repository.UpdateItem(ctx, UpdateItemInput{
				DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
				ItemPublicID: itemID, Name: &collisionName,
				IdempotencyKey: fmt.Sprintf("concurrent-collision-%d", index),
			})
			collisionResults <- updateErr
		}(index, itemID)
	}
	close(startCollision)
	succeeded, conflicted := 0, 0
	for range 2 {
		updateErr := <-collisionResults
		if updateErr == nil {
			succeeded++
		} else if errors.Is(updateErr, ErrNameConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected concurrent rename result: %v", updateErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent collision was not serialized: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	if _, err := repository.UpdateItem(ctx, UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: restoredProjectsID, ParentPublicID: &nestedParentID, IdempotencyKey: "cyclic-move-denied",
	}); !errors.Is(err, ErrInvalidMove) {
		t.Fatalf("expected cyclic folder move to fail, got %v", err)
	}
	fallbackParent, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "", "Fallback parent")
	if err != nil {
		t.Fatalf("create fallback parent fixture: %v", err)
	}
	fallbackChild, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "Fallback parent", "Recovered folder")
	if err != nil {
		t.Fatalf("create fallback child fixture: %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, principal.UserID, fallbackParent.ID, true, "trash-fallback-parent"); err != nil {
		t.Fatalf("trash fallback parent fixture: %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, principal.UserID, fallbackParent.ID, true, "trash-fallback-parent"); err != nil {
		t.Fatalf("replay trash fallback parent fixture: %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, principal.UserID, fallbackParent.ID, false, "trash-fallback-parent"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed trash payload, got %v", err)
	}
	var trashActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id
		WHERE item.public_id = $1::uuid AND event.event_type = 'item.trashed'
	`, fallbackParent.ID).Scan(&trashActivityCount); err != nil || trashActivityCount != 1 {
		t.Fatalf("trash replay emitted duplicate activity: count=%d err=%v", trashActivityCount, err)
	}
	if _, err := repository.EnsureFolder(ctx, principal.DriveID, principal.UserID, "", "Recovered folder"); err != nil {
		t.Fatalf("create restore name conflict fixture: %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, principal.UserID, fallbackChild.ID, false, "restore-fallback-child"); err != nil {
		t.Fatalf("restore child with missing parent: %v", err)
	}
	if err := repository.SetItemTrashedIdempotent(ctx, principal.DriveID, principal.UserID, fallbackChild.ID, false, "restore-fallback-child"); err != nil {
		t.Fatalf("replay restore child with missing parent: %v", err)
	}
	var restoreActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id
		WHERE item.public_id = $1::uuid AND event.event_type = 'item.restored'
	`, fallbackChild.ID).Scan(&restoreActivityCount); err != nil || restoreActivityCount != 1 {
		t.Fatalf("restore replay emitted duplicate activity: count=%d err=%v", restoreActivityCount, err)
	}
	restoredFallbackChild, err := repository.GetItem(ctx, principal.DriveID, fallbackChild.ID)
	if err != nil || restoredFallbackChild.Name != "Recovered folder (1)" {
		t.Fatalf("restore fallback did not resolve root conflict: item=%+v err=%v", restoredFallbackChild, err)
	}
	var restoredUnderRoot bool
	if err := pool.QueryRow(ctx, `
		SELECT parent.parent_id IS NULL
		FROM drive.item child JOIN drive.item parent ON parent.id = child.parent_id
		WHERE child.public_id = $1::uuid
	`, fallbackChild.ID).Scan(&restoredUnderRoot); err != nil || !restoredUnderRoot {
		t.Fatalf("restored item did not fall back to drive root: root=%v err=%v", restoredUnderRoot, err)
	}
	purgeObject := LegacyObject{
		Bucket: input.StorageBucket, Key: "purge-me.txt", ETag: "purge-etag",
		SizeBytes: 10, MIMEType: "text/plain", LastModified: time.Now(),
	}
	if recorded, err := repository.RecordStoredObject(ctx, principal.DriveID, purgeObject); err != nil || !recorded {
		t.Fatalf("record purge fixture: recorded=%v err=%v", recorded, err)
	}
	purgeItemID, err := repository.TrashFileByStorageKey(ctx, principal.DriveID, principal.UserID, purgeObject.Key)
	if err != nil {
		t.Fatalf("trash purge fixture: %v", err)
	}
	if err := repository.QueueItemForPermanentDeletionIdempotent(ctx, principal.DriveID, principal.UserID, purgeItemID, "delete-purge-item"); err != nil {
		t.Fatalf("queue permanent deletion fixture: %v", err)
	}
	if err := repository.QueueItemForPermanentDeletionIdempotent(ctx, principal.DriveID, principal.UserID, purgeItemID, "delete-purge-item"); err != nil {
		t.Fatalf("replay permanent deletion fixture: %v", err)
	}
	if err := repository.QueueItemForPermanentDeletionIdempotent(ctx, principal.DriveID, principal.UserID, fallbackParent.ID, "delete-purge-item"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed permanent-delete target, got %v", err)
	}
	var permanentDeleteActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event event
		JOIN drive.item item ON item.id = event.item_id
		WHERE item.public_id = $1::uuid AND event.event_type = 'item.permanent_delete_requested'
	`, purgeItemID).Scan(&permanentDeleteActivityCount); err != nil || permanentDeleteActivityCount != 1 {
		t.Fatalf("permanent-delete replay emitted duplicate activity: count=%d err=%v", permanentDeleteActivityCount, err)
	}
	if queued, err := repository.QueueExpiredTrash(ctx, 50); err != nil || queued != 1 {
		t.Fatalf("queue purge fixture: queued=%d err=%v", queued, err)
	}
	var deletionJob *BlobDeletionJob
	for attempt := 0; attempt < 10; attempt++ {
		candidate, claimErr := repository.ClaimBlobDeletionJob(ctx, time.Minute)
		if claimErr != nil || candidate == nil {
			t.Fatalf("claim purge fixture: job=%+v err=%v", candidate, claimErr)
		}
		if candidate.StorageKey == purgeObject.Key {
			deletionJob = candidate
			break
		}
		if err := repository.CompleteBlobDeletionJob(ctx, candidate.ID, candidate.Attempt); err != nil {
			t.Fatalf("complete earlier cleanup fixture: %v", err)
		}
	}
	if deletionJob == nil {
		t.Fatalf("purge fixture was not claimed after earlier cleanup jobs")
	}
	if err := repository.CompleteBlobDeletionJob(ctx, deletionJob.ID, deletionJob.Attempt); err != nil {
		t.Fatalf("complete purge fixture: %v", err)
	}
	var purgedVersionState, deletionStatus string
	if err := pool.QueryRow(ctx, `
		SELECT version.state, job.status
		FROM drive.file_version version
		JOIN drive.blob_deletion_job job ON job.file_version_id = version.id
		WHERE version.public_id = $1::uuid
	`, deletionJob.VersionID).Scan(&purgedVersionState, &deletionStatus); err != nil {
		t.Fatalf("load purge result: %v", err)
	}
	if purgedVersionState != "deleted" || deletionStatus != "succeeded" {
		t.Fatalf("unexpected purge result: version=%s job=%s", purgedVersionState, deletionStatus)
	}
	emptyTrashCount, err := repository.QueueAllTrashForPermanentDeletionIdempotent(ctx, principal.DriveID, principal.UserID, "empty-owner-trash")
	if err != nil || emptyTrashCount < 1 {
		t.Fatalf("queue empty trash: count=%d err=%v", emptyTrashCount, err)
	}
	replayedEmptyTrashCount, err := repository.QueueAllTrashForPermanentDeletionIdempotent(ctx, principal.DriveID, principal.UserID, "empty-owner-trash")
	if err != nil || replayedEmptyTrashCount != emptyTrashCount {
		t.Fatalf("replay empty trash changed result: first=%d replay=%d err=%v", emptyTrashCount, replayedEmptyTrashCount, err)
	}
	if _, err := repository.QueueAllTrashForPermanentDeletionIdempotent(ctx, sharedUser.DriveID, principal.UserID, "empty-owner-trash"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for changed empty-trash drive, got %v", err)
	}
	var emptyTrashActivityCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM drive.activity_event
		WHERE drive_id = (SELECT id FROM drive.drive_space WHERE public_id = $1::uuid)
			AND actor_user_id = (SELECT id FROM drive.user_account WHERE public_id = $2::uuid)
			AND event_type = 'trash.empty_requested'
	`, principal.DriveID, principal.UserID).Scan(&emptyTrashActivityCount); err != nil || emptyTrashActivityCount != 1 {
		t.Fatalf("empty-trash replay emitted duplicate activity: count=%d err=%v", emptyTrashActivityCount, err)
	}

	sessionToken, _, err := repository.CreateSession(ctx, principal.UserID, "integration-test", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	authenticated, err := repository.AuthenticateSession(ctx, principal.UserID, sessionToken)
	if err != nil {
		t.Fatalf("authenticate session: %v", err)
	}
	if authenticated.UserID != principal.UserID || authenticated.StorageBucket != input.StorageBucket {
		t.Fatalf("unexpected authenticated principal: %+v", authenticated)
	}
	otherToken, _, err := repository.CreateSession(ctx, principal.UserID, "integration-test-other", "127.0.0.2", time.Hour)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	sessions, err := repository.ListAuthSessions(ctx, principal.UserID, sessionToken)
	if err != nil || len(sessions) != 2 || !sessions[0].Current {
		t.Fatalf("unexpected active sessions: sessions=%+v err=%v", sessions, err)
	}
	var otherSessionID string
	for _, session := range sessions {
		if !session.Current {
			otherSessionID = session.ID
		}
	}
	if current, err := repository.RevokeAuthSessionByID(ctx, principal.UserID, otherSessionID, sessionToken); err != nil || current {
		t.Fatalf("revoke other session by id: current=%t err=%v", current, err)
	}
	if _, err := repository.AuthenticateSession(ctx, principal.UserID, otherToken); err != ErrInvalidSession {
		t.Fatalf("revoked other session still authenticated: %v", err)
	}
	thirdToken, _, err := repository.CreateSession(ctx, principal.UserID, "integration-test-third", "127.0.0.3", time.Hour)
	if err != nil {
		t.Fatalf("create third session: %v", err)
	}
	if revoked, err := repository.RevokeOtherAuthSessions(ctx, principal.UserID, sessionToken); err != nil || revoked != 1 {
		t.Fatalf("revoke other sessions: count=%d err=%v", revoked, err)
	}
	if _, err := repository.AuthenticateSession(ctx, principal.UserID, thirdToken); err != ErrInvalidSession {
		t.Fatalf("revoked third session still authenticated: %v", err)
	}
	if err := repository.RevokeSession(ctx, principal.UserID, sessionToken); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := repository.AuthenticateSession(ctx, principal.UserID, sessionToken); err != ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession after revocation, got %v", err)
	}

	var driveID, rootID, userID int64
	if err := pool.QueryRow(ctx, `
		SELECT d.id, root.id, d.owner_user_id
		FROM drive.drive_space d
		JOIN drive.item root ON root.drive_id = d.id AND root.parent_id IS NULL
		WHERE d.public_id = $1::uuid
	`, principal.DriveID).Scan(&driveID, &rootID, &userID); err != nil {
		t.Fatalf("resolve drive identifiers for pagination test: %v", err)
	}
	var baseFolderCount, baseFileCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE item.kind = 'folder'),
			count(*) FILTER (WHERE item.kind = 'file' AND EXISTS (
				SELECT 1 FROM drive.file_version version
				WHERE version.item_id = item.id AND version.is_current AND version.state = 'ready'
			))
		FROM drive.item item
		WHERE item.drive_id = $1 AND item.parent_id = $2 AND item.trashed_at IS NULL
	`, driveID, rootID).Scan(&baseFolderCount, &baseFileCount); err != nil {
		t.Fatalf("count pagination baseline: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
		SELECT $1, $2, 'folder', format('Cursor Folder %s', lpad(value::text, 4, '0')), $3
		FROM generate_series(1, 502) AS value
	`, driveID, rootID, userID); err != nil {
		t.Fatalf("insert pagination folders: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		WITH inserted AS (
			INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
			SELECT $1, $2, 'file', format('Cursor File %s.txt', lpad(value::text, 4, '0')), $3
			FROM generate_series(1, 501) AS value
			RETURNING id, name
		)
		INSERT INTO drive.file_version (
			item_id, version_number, state, storage_bucket, storage_key, storage_etag,
			size_bytes, mime_type, created_by_user_id, is_current, ready_at
		)
		SELECT id, 1, 'ready', $4, 'cursor-test/' || name, 'cursor-etag-' || id,
			0, 'text/plain', $3, true, now()
		FROM inserted
	`, driveID, rootID, userID, input.StorageBucket); err != nil {
		t.Fatalf("insert pagination files: %v", err)
	}

	expectedFolderCount := baseFolderCount + 502
	expectedFileCount := baseFileCount + 501
	seen := make(map[string]struct{}, expectedFolderCount+expectedFileCount)
	var after *pagecursor.Position
	folderCount := 0
	fileCount := 0
	for pageNumber := 0; ; pageNumber++ {
		listing, next, err := repository.ListDriveCursor(ctx, principal.DriveID, "", after, 73)
		if err != nil {
			t.Fatalf("list cursor page %d: %v", pageNumber, err)
		}
		if listing.Pagination.Returned == 0 {
			t.Fatalf("cursor page %d was unexpectedly empty", pageNumber)
		}
		for _, folder := range listing.Folders {
			if _, duplicate := seen[folder.ID]; duplicate {
				t.Fatalf("folder %s appeared more than once", folder.ID)
			}
			seen[folder.ID] = struct{}{}
			folderCount++
		}
		for _, file := range listing.Files {
			if _, duplicate := seen[file.ID]; duplicate {
				t.Fatalf("file %s appeared more than once", file.ID)
			}
			seen[file.ID] = struct{}{}
			fileCount++
		}
		if next == nil {
			if listing.Pagination.HasMore {
				t.Fatal("final cursor page reported more results without a cursor")
			}
			break
		}
		if !listing.Pagination.HasMore {
			t.Fatal("non-final cursor page did not report more results")
		}
		after = next
	}
	if len(seen) != expectedFolderCount+expectedFileCount || folderCount != expectedFolderCount || fileCount != expectedFileCount {
		t.Fatalf("incomplete cursor listing: unique=%d folders=%d files=%d", len(seen), folderCount, fileCount)
	}
	seenSearchResults := make(map[string]struct{}, 501)
	var searchAfter *pagecursor.Position
	for pageNumber := 0; ; pageNumber++ {
		results, next, err := repository.SearchDriveCursor(ctx, principal.DriveID, "", "Cursor File", searchAfter, 37)
		if err != nil {
			t.Fatalf("search cursor page %d: %v", pageNumber, err)
		}
		for _, file := range results.Results {
			if _, duplicate := seenSearchResults[file.ID]; duplicate {
				t.Fatalf("search result %s appeared more than once", file.ID)
			}
			seenSearchResults[file.ID] = struct{}{}
		}
		if next == nil {
			if results.Pagination.HasMore {
				t.Fatal("final search page reported more results without a cursor")
			}
			break
		}
		if !results.Pagination.HasMore {
			t.Fatal("non-final search page did not report more results")
		}
		searchAfter = next
	}
	if len(seenSearchResults) != 501 {
		t.Fatalf("incomplete cursor search: unique=%d", len(seenSearchResults))
	}
}

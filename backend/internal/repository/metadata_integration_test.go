package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	drivemigrations "surajdrive/backend/internal/database/migrations"
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
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM drive.legacy_import_record").Scan(&importCount); err != nil {
		t.Fatalf("recount import records: %v", err)
	}
	if importCount != 3 {
		t.Fatalf("new upload unexpectedly created a legacy import record: %d", importCount)
	}
	rootListing, err := repository.ListDrive(ctx, principal.DriveID, "", 0, 50)
	if err != nil {
		t.Fatalf("list drive root: %v", err)
	}
	if len(rootListing.Folders) != 1 || rootListing.Folders[0].Name != "Projects" || rootListing.Folders[0].ID == "" {
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
	if err := repository.SetItemTrashed(ctx, principal.DriveID, principal.UserID, trashedFileID, false); err != nil {
		t.Fatalf("restore file: %v", err)
	}
	trashedFolderID, err := repository.TrashFolderByPrefix(ctx, principal.DriveID, principal.UserID, "Projects")
	if err != nil {
		t.Fatalf("trash folder by prefix: %v", err)
	}
	rootListing, err = repository.ListDrive(ctx, principal.DriveID, "", 0, 50)
	if err != nil || len(rootListing.Folders) != 0 {
		t.Fatalf("trashed folder remained visible: listing=%+v err=%v", rootListing, err)
	}
	if err := repository.SetItemTrashed(ctx, principal.DriveID, principal.UserID, trashedFolderID, false); err != nil {
		t.Fatalf("restore folder tree: %v", err)
	}
	projectListing, err = repository.ListDrive(ctx, principal.DriveID, "Projects", 0, 50)
	if err != nil || len(projectListing.Files) != 1 {
		t.Fatalf("restored folder tree is incomplete: listing=%+v err=%v", projectListing, err)
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
	if err := repository.RevokeSession(ctx, principal.UserID, sessionToken); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := repository.AuthenticateSession(ctx, principal.UserID, sessionToken); err != ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession after revocation, got %v", err)
	}
}

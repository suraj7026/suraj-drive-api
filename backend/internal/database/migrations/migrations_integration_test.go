package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestMetadataFoundationMigration(t *testing.T) {
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

	databaseName := fmt.Sprintf("drive_migration_test_%d", time.Now().UnixNano())
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
		t.Fatalf("parse admin database URL: %v", err)
	}
	targetConfig.Database = databaseName

	targetDB := stdlib.OpenDB(*targetConfig)

	goose.SetBaseFS(FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpContext(ctx, targetDB, "."); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	var tableCount int
	if err := targetDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = 'drive' AND table_type = 'BASE TABLE'
	`).Scan(&tableCount); err != nil {
		t.Fatalf("count migrated tables: %v", err)
	}
	if tableCount != 14 {
		t.Fatalf("expected 14 drive tables, got %d", tableCount)
	}

	if _, err := targetDB.ExecContext(ctx, `
		INSERT INTO drive.user_account (primary_email, display_name)
		VALUES ('migration-test@example.com', 'Migration Test')
	`); err != nil {
		t.Fatalf("insert valid user: %v", err)
	}
	if _, err := targetDB.ExecContext(ctx, `
		INSERT INTO drive.user_account (primary_email, display_name)
		VALUES ('MIGRATION-TEST@example.com', 'Duplicate Email')
	`); err == nil {
		t.Fatal("expected case-insensitive email uniqueness violation")
	}

	if err := goose.DownToContext(ctx, targetDB, ".", 0); err != nil {
		t.Fatalf("roll back migrations: %v", err)
	}
	var schemaExists bool
	if err := targetDB.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'drive')
	`).Scan(&schemaExists); err != nil {
		t.Fatalf("check rolled-back schema: %v", err)
	}
	if schemaExists {
		t.Fatal("drive schema still exists after rollback")
	}

	if err := goose.UpContext(ctx, targetDB, "."); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	if err := targetDB.Close(); err != nil {
		t.Fatalf("close temporary database: %v", err)
	}
}

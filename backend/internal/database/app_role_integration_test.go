package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfiguredAppRoleCanUseRequiredSchema(t *testing.T) {
	databaseURL := os.Getenv("TEST_APP_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_APP_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("configure app-role database pool: %v", err)
	}
	defer pool.Close()

	var role string
	var schemaUsage, canSelect, canInsert, canUpdate, canDelete, canUseMutationTable bool
	if err := pool.QueryRow(ctx, `
		SELECT current_user,
			has_schema_privilege(current_user, 'drive', 'USAGE'),
			has_table_privilege(current_user, 'drive.user_preference', 'SELECT'),
			has_table_privilege(current_user, 'drive.user_preference', 'INSERT'),
			has_table_privilege(current_user, 'drive.user_preference', 'UPDATE'),
			has_table_privilege(current_user, 'drive.user_preference', 'DELETE'),
			has_table_privilege(current_user, 'drive.mutation_request', 'SELECT,INSERT,UPDATE,DELETE')
	`).Scan(&role, &schemaUsage, &canSelect, &canInsert, &canUpdate, &canDelete, &canUseMutationTable); err != nil {
		t.Fatalf("inspect app-role grants: %v", err)
	}
	if role != "drive_app" {
		t.Fatalf("expected drive_app, got %q", role)
	}
	var privileged, canReadLedger bool
	var schemaVersion int64
	if err := pool.QueryRow(ctx, `
		SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls,
			has_table_privilege(current_user, 'public.goose_db_version', 'SELECT'), drive.schema_version()
		FROM pg_roles WHERE rolname = current_user
	`).Scan(&privileged, &canReadLedger, &schemaVersion); err != nil {
		t.Fatalf("inspect least-privilege readiness: %v", err)
	}
	if privileged || canReadLedger || schemaVersion != 16 {
		t.Fatalf("unsafe application role or incompatible schema: privileged=%t ledger=%t version=%d", privileged, canReadLedger, schemaVersion)
	}
	if !schemaUsage || !canSelect || !canInsert || !canUpdate || !canDelete || !canUseMutationTable {
		t.Fatalf("drive_app grants are incomplete: schema=%t select=%t insert=%t update=%t delete=%t mutation=%t", schemaUsage, canSelect, canInsert, canUpdate, canDelete, canUseMutationTable)
	}
}

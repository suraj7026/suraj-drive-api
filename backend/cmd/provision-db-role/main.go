package main

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog/log"
)

func main() {
	adminURL := os.Getenv("DATABASE_ADMIN_URL")
	appPassword := os.Getenv("DATABASE_APP_PASSWORD")
	if adminURL == "" || appPassword == "" {
		log.Fatal().Msg("DATABASE_ADMIN_URL and DATABASE_APP_PASSWORD are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", adminURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to configure database connection")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}

	if _, err := db.ExecContext(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'drive_app') THEN
				CREATE ROLE drive_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
			END IF;
		END
		$$
	`); err != nil {
		log.Fatal().Err(err).Msg("failed to create application role")
	}

	var passwordStatement string
	if err := db.QueryRowContext(ctx, `
		SELECT format('ALTER ROLE drive_app PASSWORD %L', $1::text)
	`, appPassword).Scan(&passwordStatement); err != nil {
		log.Fatal().Err(err).Msg("failed to construct password statement")
	}
	if _, err := db.ExecContext(ctx, passwordStatement); err != nil {
		log.Fatal().Err(err).Msg("failed to set application role password")
	}

	var databaseName string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		log.Fatal().Err(err).Msg("failed to identify database")
	}
	grantConnect := "GRANT CONNECT ON DATABASE " + pgx.Identifier{databaseName}.Sanitize() + " TO drive_app"
	statements := []string{
		"ALTER ROLE drive_app NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		grantConnect,
		"GRANT USAGE ON SCHEMA drive TO drive_app",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA drive TO drive_app",
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA drive TO drive_app",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA drive GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO drive_app",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA drive GRANT USAGE, SELECT ON SEQUENCES TO drive_app",
		"ALTER ROLE drive_app SET search_path TO drive, public",
		"ALTER ROLE drive_app CONNECTION LIMIT 50",
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			log.Fatal().Err(err).Msg("failed to grant application role privileges")
		}
	}
	log.Info().Str("role", "drive_app").Str("database", databaseName).Msg("database application role provisioned")
}

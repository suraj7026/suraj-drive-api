package main

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/config"
	drivemigrations "surajdrive/backend/internal/database/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if !allowedCommand(command) {
		log.Fatal().Str("command", command).Msg("unsupported migration command")
	}

	databaseURL := strings.TrimSpace(cfg.Database.MigrationURL)
	if databaseURL == "" {
		databaseURL = cfg.Database.URL
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to configure migration database")
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to migration database")
	}

	goose.SetBaseFS(drivemigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal().Err(err).Msg("failed to set migration dialect")
	}
	if err := goose.RunContext(ctx, command, db, "."); err != nil {
		log.Fatal().Err(err).Str("command", command).Msg("migration command failed")
	}

	log.Info().Str("command", command).Msg("migration command completed")
}

func allowedCommand(command string) bool {
	switch command {
	case "up", "up-by-one", "down", "status", "version":
		return true
	default:
		return false
	}
}

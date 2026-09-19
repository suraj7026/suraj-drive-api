package migrations

import "embed"

// FS contains the versioned database migrations used by the migration command.
//
//go:embed *.sql
var FS embed.FS

package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrDeletionStarted = errors.New("one or more files are already being permanently deleted")
var ErrJobLeaseLost = errors.New("worker claim has expired or been replaced")

func lockBlobLifecycle(ctx context.Context, tx pgx.Tx, driveID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('blob-lifecycle:' || $1::bigint::text, 0))`, driveID)
	if err != nil {
		return fmt.Errorf("lock blob lifecycle: %w", err)
	}
	return nil
}

func lockItemBlobLifecycle(ctx context.Context, tx pgx.Tx, itemPublicID string) error {
	var driveID int64
	if err := tx.QueryRow(ctx, `SELECT drive_id FROM drive.item WHERE public_id = $1::uuid`, itemPublicID).Scan(&driveID); err != nil {
		return ErrItemNotFound
	}
	return lockBlobLifecycle(ctx, tx, driveID)
}

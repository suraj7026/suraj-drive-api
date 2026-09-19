package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Reservations remain charged until completion or durable blob deletion. Failed
// uploads may have no recorded size, so retain their reservation as a lower bound.
const chargedVersionBytes = `GREATEST(COALESCE(version.size_bytes, 0),
	COALESCE(session.bytes_received, 0), COALESCE(session.expected_size_bytes, 0))`

const activeReservation = `session.status IN ('initiated', 'uploading', 'completing')`

func accountStorageUsage(ctx context.Context, tx pgx.Tx, userID int64) (committed, reserved int64, err error) {
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(sum(`+chargedVersionBytes+`) FILTER (WHERE NOT COALESCE(`+activeReservation+`, false)), 0),
			COALESCE(sum(`+chargedVersionBytes+`) FILTER (WHERE `+activeReservation+`), 0)
		FROM drive.file_version version
		JOIN drive.item item ON item.id = version.item_id
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		LEFT JOIN drive.upload_session session ON session.file_version_id = version.id
		WHERE drive.owner_user_id = $1 AND version.state <> 'deleted'
	`, userID).Scan(&committed, &reserved)
	if err != nil {
		return 0, 0, fmt.Errorf("calculate account storage usage: %w", err)
	}
	return committed, reserved, nil
}

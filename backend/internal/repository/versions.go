package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type FileVersionInfo struct {
	ID            string    `json:"id"`
	VersionNumber int64     `json:"version_number"`
	SizeBytes     int64     `json:"size_bytes"`
	MIMEType      string    `json:"mime_type"`
	ETag          string    `json:"etag"`
	KeepForever   bool      `json:"keep_forever"`
	IsCurrent     bool      `json:"is_current"`
	CreatedAt     time.Time `json:"created_at"`
	ReadyAt       time.Time `json:"ready_at"`
}

func (m *Metadata) ListFileVersions(ctx context.Context, userPublicID, itemPublicID string) ([]FileVersionInfo, error) {
	if _, err := m.ResolveAccessibleFile(ctx, userPublicID, itemPublicID); err != nil {
		return nil, err
	}
	rows, err := m.pool.Query(ctx, `
		SELECT version.public_id::text, version.version_number, version.size_bytes,
			version.mime_type, version.storage_etag, version.keep_forever,
			version.is_current, version.created_at, version.ready_at
		FROM drive.file_version version
		JOIN drive.item item ON item.id = version.item_id
		WHERE item.public_id = $1::uuid AND version.state = 'ready'
		ORDER BY version.version_number DESC, version.id DESC
	`, itemPublicID)
	if err != nil {
		return nil, fmt.Errorf("list file versions: %w", err)
	}
	defer rows.Close()
	versions := make([]FileVersionInfo, 0)
	for rows.Next() {
		var version FileVersionInfo
		if err := rows.Scan(
			&version.ID, &version.VersionNumber, &version.SizeBytes, &version.MIMEType,
			&version.ETag, &version.KeepForever, &version.IsCurrent,
			&version.CreatedAt, &version.ReadyAt,
		); err != nil {
			return nil, fmt.Errorf("scan file version: %w", err)
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (m *Metadata) ResolveAccessibleVersion(ctx context.Context, userPublicID, itemPublicID, versionPublicID string) (AccessibleFileVersion, error) {
	current, err := m.ResolveAccessibleFile(ctx, userPublicID, itemPublicID)
	if err != nil {
		return AccessibleFileVersion{}, err
	}
	var result AccessibleFileVersion
	err = m.pool.QueryRow(ctx, `
		SELECT item.public_id::text, drive.public_id::text, version.public_id::text,
			item.name, version.mime_type, version.size_bytes, version.storage_bucket, version.storage_key, true
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		JOIN drive.file_version version ON version.item_id = item.id
		WHERE item.public_id = $1::uuid AND version.public_id = $2::uuid AND version.state = 'ready'
	`, itemPublicID, versionPublicID).Scan(
		&result.ItemID, &result.DriveID, &result.VersionID, &result.Name,
		&result.MIMEType, &result.SizeBytes, &result.Bucket, &result.StorageKey, &result.AllowDownload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessibleFileVersion{}, ErrItemNotFound
	}
	if err != nil {
		return AccessibleFileVersion{}, fmt.Errorf("resolve accessible version: %w", err)
	}
	if result.DriveID != current.DriveID {
		return AccessibleFileVersion{}, ErrItemNotFound
	}
	return result, nil
}

func (m *Metadata) SetVersionKeepForever(ctx context.Context, userPublicID, itemPublicID, versionPublicID string, keep bool) error {
	return m.setVersionKeepForever(ctx, userPublicID, itemPublicID, versionPublicID, keep, "")

}

func (m *Metadata) SetVersionKeepForeverIdempotent(ctx context.Context, userPublicID, itemPublicID, versionPublicID string, keep bool, idempotencyKey string) error {
	return m.setVersionKeepForever(ctx, userPublicID, itemPublicID, versionPublicID, keep, idempotencyKey)
}

func (m *Metadata) setVersionKeepForever(ctx context.Context, userPublicID, itemPublicID, versionPublicID string, keep bool, idempotencyKey string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin version retention update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID).Scan(&actorID); err != nil {
		return ErrPermissionDenied
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%t", itemPublicID, versionPublicID, keep)))
		replayed, err := claimMutationRequest(ctx, tx, actorID, idempotencyKey, "version.retention", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	var itemID, driveID, versionID int64
	var currentKeep bool
	if err := lockItemBlobLifecycle(ctx, tx, itemPublicID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		SELECT item.id, item.drive_id, version.id, version.keep_forever
		FROM drive.item item
		JOIN drive.file_version version ON version.item_id = item.id
		JOIN drive.drive_member member ON member.drive_id = item.drive_id AND member.user_id = $3
		WHERE item.public_id = $1::uuid AND version.public_id = $2::uuid
			AND item.trashed_at IS NULL AND version.state = 'ready'
			AND member.role IN ('owner', 'manager', 'editor')
		FOR UPDATE OF version
	`, itemPublicID, versionPublicID, actorID).Scan(&itemID, &driveID, &versionID, &currentKeep); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPermissionDenied
		}
		return fmt.Errorf("authorize version retention update: %w", err)
	}
	if currentKeep != keep {
		if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET keep_forever = $2 WHERE id = $1`, versionID, keep); err != nil {
			return fmt.Errorf("update version retention: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type, details)
			VALUES ($1, $2, $3, $4, 'version.retention_updated', jsonb_build_object('keep_forever', $5::boolean))
		`, driveID, itemID, versionID, actorID, keep); err != nil {
			return fmt.Errorf("record version retention update: %w", err)
		}
	}
	if keep {
		if _, err := tx.Exec(ctx, `DELETE FROM drive.blob_deletion_job WHERE file_version_id = $1 AND status IN ('queued', 'failed')`, versionID); err != nil {
			return fmt.Errorf("cancel retained version deletion: %w", err)
		}
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, actorID, idempotencyKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version retention update: %w", err)
	}
	return nil
}

// RestoreFileVersion makes an existing immutable version current without
// rewriting either the selected blob or the version history.
func (m *Metadata) RestoreFileVersion(ctx context.Context, userPublicID, itemPublicID, versionPublicID string) error {
	return m.restoreFileVersion(ctx, userPublicID, itemPublicID, versionPublicID, "")
}

func (m *Metadata) RestoreFileVersionIdempotent(ctx context.Context, userPublicID, itemPublicID, versionPublicID, idempotencyKey string) error {
	return m.restoreFileVersion(ctx, userPublicID, itemPublicID, versionPublicID, idempotencyKey)
}

func (m *Metadata) restoreFileVersion(ctx context.Context, userPublicID, itemPublicID, versionPublicID, idempotencyKey string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin version restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var actorID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID).Scan(&actorID); err != nil {
		return ErrPermissionDenied
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(itemPublicID + "\x00" + versionPublicID))
		replayed, err := claimMutationRequest(ctx, tx, actorID, idempotencyKey, "version.restore", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	var itemID, driveID, versionID int64
	var isCurrent bool
	if err := lockItemBlobLifecycle(ctx, tx, itemPublicID); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `
		SELECT item.id, item.drive_id, version.id, version.is_current
		FROM drive.item item
		JOIN drive.file_version version ON version.item_id = item.id AND version.public_id = $2::uuid
		JOIN drive.drive_member member ON member.drive_id = item.drive_id AND member.user_id = $3
		WHERE item.public_id = $1::uuid
			AND item.kind = 'file' AND item.trashed_at IS NULL
			AND version.state = 'ready'
			AND member.role IN ('owner', 'manager', 'editor')
		FOR UPDATE OF item
	`, itemPublicID, versionPublicID, actorID).Scan(&itemID, &driveID, &versionID, &isCurrent)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPermissionDenied
	}
	if err != nil {
		return fmt.Errorf("authorize version restore: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drive.blob_deletion_job WHERE file_version_id = $1 AND status IN ('queued', 'failed')`, versionID); err != nil {
		return fmt.Errorf("cancel restored version deletion: %w", err)
	}
	if isCurrent {
		if idempotencyKey != "" {
			if err := completeMutationRequest(ctx, tx, actorID, idempotencyKey); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.file_version
		SET is_current = false
		WHERE item_id = $1 AND is_current
	`, itemID); err != nil {
		return fmt.Errorf("retire current version: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.file_version
		SET is_current = true
		WHERE id = $1 AND item_id = $2 AND state = 'ready'
	`, versionID, itemID)
	if err != nil {
		return fmt.Errorf("restore selected version: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrPermissionDenied
	}
	if _, err := tx.Exec(ctx, `UPDATE drive.item SET updated_at = now() WHERE id = $1`, itemID); err != nil {
		return fmt.Errorf("touch restored item: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type)
		VALUES ($1, $2, $3, $4, 'version.restored')
	`, driveID, itemID, versionID, actorID); err != nil {
		return fmt.Errorf("record version restore: %w", err)
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, actorID, idempotencyKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version restore: %w", err)
	}
	return nil
}

package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type AccessibleFileVersion struct {
	ItemID        string
	DriveID       string
	VersionID     string
	Name          string
	MIMEType      string
	SizeBytes     int64
	Bucket        string
	StorageKey    string
	SHA256        []byte
	AllowDownload bool
}

func (m *Metadata) ResolveAccessibleFile(ctx context.Context, userPublicID, itemPublicID string) (AccessibleFileVersion, error) {
	var result AccessibleFileVersion
	err := m.pool.QueryRow(ctx, `
		WITH RECURSIVE selected_user AS (
			SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), target AS (
			SELECT item.id, item.parent_id, item.drive_id, item.public_id, item.name
			FROM drive.item item
			WHERE item.public_id = $2::uuid AND item.kind = 'file' AND item.trashed_at IS NULL
		), ancestors AS (
			SELECT id, parent_id FROM target
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM drive.item parent JOIN ancestors child ON child.parent_id = parent.id
		), access AS (
			SELECT EXISTS (
				SELECT 1 FROM target
				JOIN selected_user actor ON true
				LEFT JOIN drive.drive_member member ON member.drive_id = target.drive_id AND member.user_id = actor.id
				WHERE member.user_id IS NOT NULL OR EXISTS (
					SELECT 1 FROM drive.item_permission permission
					WHERE permission.item_id IN (SELECT id FROM ancestors)
						AND permission.grantee_user_id = actor.id
						AND (permission.expires_at IS NULL OR permission.expires_at > now())
				)
			) AS allowed
		)
		SELECT target.public_id::text, drive.public_id::text, version.public_id::text,
			target.name, version.mime_type, version.size_bytes, version.storage_bucket, version.storage_key, version.sha256, access.allowed
		FROM target
		JOIN drive.drive_space drive ON drive.id = target.drive_id AND drive.status = 'active'
		JOIN drive.file_version version ON version.item_id = target.id AND version.is_current AND version.state = 'ready'
		CROSS JOIN access
		WHERE access.allowed
	`, userPublicID, itemPublicID).Scan(
		&result.ItemID, &result.DriveID, &result.VersionID, &result.Name,
		&result.MIMEType, &result.SizeBytes, &result.Bucket, &result.StorageKey, &result.SHA256, &result.AllowDownload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessibleFileVersion{}, ErrItemNotFound
	}
	if err != nil {
		return AccessibleFileVersion{}, fmt.Errorf("resolve accessible file: %w", err)
	}
	return result, nil
}

func (m *Metadata) RecordItemRead(ctx context.Context, userPublicID, itemPublicID, eventType string) error {
	if eventType != "file.downloaded" && eventType != "file.previewed" {
		return fmt.Errorf("unsupported read event")
	}
	_, err := m.pool.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type)
		SELECT item.drive_id, item.id, version.id, actor.id, $3
		FROM drive.user_account actor
		JOIN drive.item item ON item.public_id = $2::uuid
		JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE actor.public_id = $1::uuid
	`, userPublicID, itemPublicID, eventType)
	if err != nil {
		return fmt.Errorf("record item read: %w", err)
	}
	return nil
}

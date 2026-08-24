package repository

import (
	"context"
	"fmt"
)

func (m *Metadata) TrashFileByStorageKey(ctx context.Context, drivePublicID, userPublicID, storageKey string) (string, error) {
	var itemPublicID string
	err := m.pool.QueryRow(ctx, `
		WITH actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $2::uuid AND status = 'active'
		), target AS (
			SELECT i.id, i.public_id
			FROM drive.item i
			JOIN drive.drive_space d ON d.id = i.drive_id
			JOIN drive.file_version fv ON fv.item_id = i.id AND fv.is_current
			WHERE d.public_id = $1::uuid AND fv.storage_key = $3 AND i.trashed_at IS NULL
			LIMIT 1
		), updated AS (
			UPDATE drive.item i
			SET trashed_at = now(), trashed_by_user_id = actor.id,
				purge_after = now() + interval '30 days'
			FROM actor, target
			WHERE i.id = target.id
			RETURNING i.public_id
		)
		SELECT public_id::text FROM updated
	`, drivePublicID, userPublicID, storageKey).Scan(&itemPublicID)
	if err != nil {
		return "", fmt.Errorf("trash file: %w", err)
	}
	return itemPublicID, nil
}

func (m *Metadata) TrashFolderByPrefix(ctx context.Context, drivePublicID, userPublicID, prefix string) (string, error) {
	_, folderID, _, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return "", err
	}
	var itemPublicID string
	err = m.pool.QueryRow(ctx, `
		WITH RECURSIVE actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $2::uuid AND status = 'active'
		), subtree AS (
			SELECT id FROM drive.item WHERE id = $1 AND parent_id IS NOT NULL
			UNION ALL
			SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		), updated AS (
			UPDATE drive.item i
			SET trashed_at = now(), trashed_by_user_id = actor.id,
				purge_after = now() + interval '30 days'
			FROM actor
			WHERE i.id IN (SELECT id FROM subtree)
			RETURNING i.id, i.public_id
		)
		SELECT public_id::text FROM updated WHERE id = $1
	`, folderID, userPublicID).Scan(&itemPublicID)
	if err != nil {
		return "", fmt.Errorf("trash folder: %w", err)
	}
	return itemPublicID, nil
}

func (m *Metadata) SetItemTrashed(ctx context.Context, drivePublicID, userPublicID, itemPublicID string, trashed bool) error {
	var command string
	if trashed {
		command = `
			WITH RECURSIVE actor AS (
				SELECT id FROM drive.user_account WHERE public_id = $2::uuid AND status = 'active'
			), target AS (
				SELECT i.id FROM drive.item i JOIN drive.drive_space d ON d.id = i.drive_id
				WHERE d.public_id = $1::uuid AND i.public_id = $3::uuid AND i.parent_id IS NOT NULL
			), subtree AS (
				SELECT id FROM target
				UNION ALL
				SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
			)
			UPDATE drive.item i
			SET trashed_at = now(), trashed_by_user_id = actor.id,
				purge_after = now() + interval '30 days'
			FROM actor
			WHERE i.id IN (SELECT id FROM subtree)
		`
	} else {
		command = `
			WITH RECURSIVE actor AS (
				SELECT id FROM drive.user_account WHERE public_id = $2::uuid AND status = 'active'
			), target AS (
				SELECT i.id FROM drive.item i JOIN drive.drive_space d ON d.id = i.drive_id
				WHERE d.public_id = $1::uuid AND i.public_id = $3::uuid AND i.parent_id IS NOT NULL
			), subtree AS (
				SELECT id FROM target
				UNION ALL
				SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
			)
			UPDATE drive.item i
			SET trashed_at = NULL, trashed_by_user_id = NULL, purge_after = NULL
			FROM actor
			WHERE i.id IN (SELECT id FROM subtree)
		`
	}
	commandTag, err := m.pool.Exec(ctx, command, drivePublicID, userPublicID, itemPublicID)
	if err != nil {
		return fmt.Errorf("update item trash state: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return fmt.Errorf("item not found")
	}
	return nil
}

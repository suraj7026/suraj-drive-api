package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/model"
	pagecursor "surajdrive/backend/internal/pagination"
)

var ErrRecentAuthenticationRequired = errors.New("recent authentication required")

func (m *Metadata) ListTrashCursor(ctx context.Context, drivePublicID string, after *pagecursor.Position, limit int) (model.ListResponse, *pagecursor.Position, error) {
	hasCursor := after != nil
	positionTime := time.Time{}
	positionID := int64(0)
	if after != nil {
		positionTime = time.UnixMicro(after.Time)
		positionID = after.ID
	}
	rows, err := m.pool.Query(ctx, `
		SELECT item.id, item.public_id::text, item.kind, item.name, item.trashed_at,
			COALESCE(version.storage_key, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.source_modified_at, version.created_at, item.updated_at),
			COALESCE(version.mime_type, ''), COALESCE(version.storage_etag, ''), COALESCE(item.folder_color, '')
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		LEFT JOIN drive.item parent ON parent.id = item.parent_id
		LEFT JOIN drive.file_version version
			ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE drive.public_id = $1::uuid AND item.trashed_at IS NOT NULL
			AND item.parent_id IS NOT NULL
			AND (parent.trashed_at IS NULL OR parent.id IS NULL)
			AND item.kind IN ('folder', 'file', 'shortcut')
			AND (item.kind IN ('folder', 'shortcut') OR version.id IS NOT NULL)
			AND (
				NOT $2::boolean
				OR item.trashed_at < $3
				OR (item.trashed_at = $3 AND item.id < $4)
			)
		ORDER BY item.trashed_at DESC, item.id DESC
		LIMIT $5
	`, drivePublicID, hasCursor, positionTime, positionID, limit+1)
	if err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("list trash: %w", err)
	}
	defer rows.Close()
	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var (
			internalID  int64
			publicID    string
			kind        string
			name        string
			trashedAt   time.Time
			file        model.FileObject
			folderColor string
		)
		if err := rows.Scan(
			&internalID, &publicID, &kind, &name, &trashedAt,
			&file.Key, &file.Size, &file.LastModified, &file.ContentType, &file.ETag, &folderColor,
		); err != nil {
			return model.ListResponse{}, nil, fmt.Errorf("scan trash: %w", err)
		}
		positions = append(positions, pagecursor.Position{ID: internalID, Time: trashedAt.UnixMicro()})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{ID: publicID, Name: name, TrashedAt: &trashedAt, FolderColor: folderColor})
		} else if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name})
		} else {
			file.ID = publicID
			file.Name = name
			file.TrashedAt = &trashedAt
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("iterate trash: %w", err)
	}
	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.ListResponse{
		Folders: folders, Files: files, Shortcuts: shortcuts,
		Pagination: model.Pagination{Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore},
	}, next, nil
}

func (m *Metadata) TrashFileByStorageKey(ctx context.Context, drivePublicID, userPublicID, storageKey string) (string, error) {
	var itemPublicID string
	err := m.pool.QueryRow(ctx, `
		WITH RECURSIVE actor AS (
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
	return m.setItemTrashed(ctx, drivePublicID, userPublicID, itemPublicID, trashed, "")
}

func (m *Metadata) SetItemTrashedIdempotent(ctx context.Context, drivePublicID, userPublicID, itemPublicID string, trashed bool, idempotencyKey string) error {
	return m.setItemTrashed(ctx, drivePublicID, userPublicID, itemPublicID, trashed, idempotencyKey)
}

func (m *Metadata) setItemTrashed(ctx context.Context, drivePublicID, userPublicID, itemPublicID string, trashed bool, idempotencyKey string) error {
	if !trashed {
		return m.restoreItem(ctx, drivePublicID, userPublicID, itemPublicID, idempotencyKey)
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin item trash: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'`, userPublicID).Scan(&actorID); err != nil {
		return ErrItemNotFound
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(itemPublicID + "\x00true"))
		replayed, err := claimMutationRequest(ctx, tx, actorID, idempotencyKey, "item.trash", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return err
	}
	if access.Rank < 3 {
		return ErrPermissionDenied
	}
	var driveMatches bool
	var memberRank int
	if err := tx.QueryRow(ctx, `
		SELECT drive.public_id::text = $3,
			COALESCE(CASE member.role
				WHEN 'editor' THEN 3 WHEN 'manager' THEN 4 WHEN 'owner' THEN 5 ELSE 0 END, 0)
		FROM drive.drive_space drive
		LEFT JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = $2
		WHERE drive.id = $1
	`, access.DriveID, access.UserID, drivePublicID).Scan(&driveMatches, &memberRank); err != nil || !driveMatches {
		return ErrItemNotFound
	}
	if memberRank < 3 {
		return ErrPermissionDenied
	}
	command, err := tx.Exec(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM drive.item WHERE id = $1 AND parent_id IS NOT NULL AND trashed_at IS NULL
			UNION ALL
			SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		)
		UPDATE drive.item SET trashed_at = now(), trashed_by_user_id = $2,
			purge_after = now() + interval '30 days'
		WHERE id IN (SELECT id FROM subtree)
	`, access.ItemID, access.UserID)
	if err != nil {
		return fmt.Errorf("trash item subtree: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
		VALUES ($1, $2, $3, 'item.trashed')
	`, access.DriveID, access.ItemID, access.UserID); err != nil {
		return fmt.Errorf("record item trash: %w", err)
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, access.UserID, idempotencyKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit item trash: %w", err)
	}
	return nil
}

func (m *Metadata) restoreItem(ctx context.Context, drivePublicID, userPublicID, itemPublicID, idempotencyKey string) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin item restore: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'`, userPublicID).Scan(&actorID); err != nil {
		return ErrItemNotFound
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(itemPublicID + "\x00false"))
		replayed, err := claimMutationRequest(ctx, tx, actorID, idempotencyKey, "item.restore", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	var driveID, itemID, originalParentID int64
	var itemName string
	if err := lockItemBlobLifecycle(ctx, tx, itemPublicID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		SELECT drive.id, actor.id, item.id, item.parent_id, item.name
		FROM drive.drive_space drive
		JOIN drive.user_account actor ON actor.public_id = $2::uuid AND actor.status = 'active'
		JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
		JOIN drive.item item ON item.drive_id = drive.id AND item.public_id = $3::uuid
		WHERE drive.public_id = $1::uuid AND item.parent_id IS NOT NULL
			AND item.trashed_at IS NOT NULL AND member.role IN ('owner', 'manager', 'editor')
		FOR UPDATE OF item
	`, drivePublicID, userPublicID, itemPublicID).Scan(&driveID, &actorID, &itemID, &originalParentID, &itemName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return fmt.Errorf("lock item restore: %w", err)
	}
	var deletionStarted bool
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, kind FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id, child.kind FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		)
		SELECT EXISTS (
			SELECT 1 FROM subtree item WHERE item.kind = 'file'
				AND EXISTS (SELECT 1 FROM drive.file_version version WHERE version.item_id = item.id AND version.state IN ('deleting', 'deleted'))
				AND NOT EXISTS (SELECT 1 FROM drive.file_version version WHERE version.item_id = item.id AND version.state = 'ready' AND version.is_current)
		)
	`, itemID).Scan(&deletionStarted); err != nil {
		return fmt.Errorf("check restore deletion boundary: %w", err)
	}
	if deletionStarted {
		return ErrDeletionStarted
	}
	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		)
		DELETE FROM drive.blob_deletion_job job USING drive.file_version version
		WHERE job.file_version_id = version.id AND version.item_id IN (SELECT id FROM subtree)
			AND version.state = 'ready' AND job.status IN ('queued', 'failed')
	`, itemID); err != nil {
		return fmt.Errorf("cancel restored subtree deletion: %w", err)
	}
	destinationParentID := originalParentID
	var parentAvailable bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM drive.item WHERE id = $1 AND drive_id = $2 AND kind = 'folder' AND trashed_at IS NULL)
	`, originalParentID, driveID).Scan(&parentAvailable); err != nil {
		return fmt.Errorf("check restore parent: %w", err)
	}
	fellBackToRoot := !parentAvailable
	if fellBackToRoot {
		if err := tx.QueryRow(ctx, `SELECT id FROM drive.item WHERE drive_id = $1 AND parent_id IS NULL`, driveID).Scan(&destinationParentID); err != nil {
			return fmt.Errorf("resolve restore root: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("%d:%d", driveID, destinationParentID)); err != nil {
		return fmt.Errorf("lock restore namespace: %w", err)
	}
	restoredName, err := reserveAvailableName(ctx, tx, driveID, destinationParentID, itemName)
	if err != nil {
		return fmt.Errorf("resolve restore name: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		)
		UPDATE drive.item
		SET trashed_at = NULL, trashed_by_user_id = NULL, purge_after = NULL,
			parent_id = CASE WHEN id = $1 THEN $2 ELSE parent_id END,
			name = CASE WHEN id = $1 THEN $3 ELSE name END
		WHERE id IN (SELECT id FROM subtree)
	`, itemID, destinationParentID, restoredName); err != nil {
		return fmt.Errorf("restore item subtree: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, 'item.restored', jsonb_build_object(
			'root_fallback', $4::boolean, 'renamed', $5::boolean, 'restored_name', $6::text
		))
	`, driveID, itemID, actorID, fellBackToRoot, restoredName != itemName, restoredName); err != nil {
		return fmt.Errorf("record item restore: %w", err)
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, actorID, idempotencyKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit item restore: %w", err)
	}
	return nil
}

func (m *Metadata) QueueItemForPermanentDeletion(ctx context.Context, drivePublicID, userPublicID, itemPublicID string) error {
	return m.queueItemForPermanentDeletion(ctx, drivePublicID, userPublicID, itemPublicID, "")
}

func (m *Metadata) QueueItemForPermanentDeletionIdempotent(ctx context.Context, drivePublicID, userPublicID, itemPublicID, idempotencyKey string) error {
	return m.queueItemForPermanentDeletion(ctx, drivePublicID, userPublicID, itemPublicID, idempotencyKey)
}

func (m *Metadata) queueItemForPermanentDeletion(ctx context.Context, drivePublicID, userPublicID, itemPublicID, idempotencyKey string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin permanent deletion request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	var recentlyAuthenticated bool
	if err := tx.QueryRow(ctx, `
		SELECT id, COALESCE(last_login_at > now() - interval '15 minutes', false)
		FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID).Scan(&actorID, &recentlyAuthenticated); err != nil {
		return ErrRecentAuthenticationRequired
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(drivePublicID + "\x00" + itemPublicID))
		replayed, err := claimMutationRequest(ctx, tx, actorID, idempotencyKey, "item.permanent_delete", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	if !recentlyAuthenticated {
		return ErrRecentAuthenticationRequired
	}
	var itemID, driveID int64
	if err := tx.QueryRow(ctx, `
		SELECT item.id, item.drive_id
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = $3
		WHERE drive.public_id = $1::uuid AND item.public_id = $2::uuid
			AND item.parent_id IS NOT NULL AND item.trashed_at IS NOT NULL
			AND member.role IN ('owner', 'manager')
		FOR UPDATE OF item
	`, drivePublicID, itemPublicID, actorID).Scan(&itemID, &driveID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrItemNotFound
		}
		return fmt.Errorf("authorize permanent deletion: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id FROM drive.item child JOIN subtree parent ON child.parent_id = parent.id
		), updated AS (
			UPDATE drive.item item SET purge_after = now()
			WHERE item.id IN (SELECT id FROM subtree) RETURNING item.id
		)
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
		SELECT $2, updated.id, $3, 'item.permanent_delete_requested' FROM updated
	`, itemID, driveID, actorID); err != nil {
		return fmt.Errorf("queue permanent deletion: %w", err)
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, actorID, idempotencyKey); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit permanent deletion request: %w", err)
	}
	return nil
}

func (m *Metadata) QueueAllTrashForPermanentDeletion(ctx context.Context, drivePublicID, userPublicID string) (int64, error) {
	return m.queueAllTrashForPermanentDeletion(ctx, drivePublicID, userPublicID, "")
}

func (m *Metadata) QueueAllTrashForPermanentDeletionIdempotent(ctx context.Context, drivePublicID, userPublicID, idempotencyKey string) (int64, error) {
	return m.queueAllTrashForPermanentDeletion(ctx, drivePublicID, userPublicID, idempotencyKey)
}

func (m *Metadata) queueAllTrashForPermanentDeletion(ctx context.Context, drivePublicID, userPublicID, idempotencyKey string) (int64, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin empty trash request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	var recentlyAuthenticated bool
	if err := tx.QueryRow(ctx, `
		SELECT id, COALESCE(last_login_at > now() - interval '15 minutes', false)
		FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID).Scan(&actorID, &recentlyAuthenticated); err != nil {
		return 0, ErrRecentAuthenticationRequired
	}
	requestHash := sha256.Sum256([]byte(drivePublicID))
	if idempotencyKey != "" {
		response, replayed, err := claimMutationRequestResult(ctx, tx, actorID, idempotencyKey, "trash.empty", requestHash[:])
		if err != nil {
			return 0, err
		}
		if replayed {
			var result struct {
				Count int64 `json:"count"`
			}
			if err := json.Unmarshal(response, &result); err != nil {
				return 0, fmt.Errorf("decode empty trash result: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return 0, fmt.Errorf("commit empty trash replay: %w", err)
			}
			return result.Count, nil
		}
	}
	if !recentlyAuthenticated {
		return 0, ErrRecentAuthenticationRequired
	}
	var driveID int64
	if err := tx.QueryRow(ctx, `
		SELECT drive.id FROM drive.drive_space drive
		JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = $2
		WHERE drive.public_id = $1::uuid AND member.role IN ('owner', 'manager')
	`, drivePublicID, actorID).Scan(&driveID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrItemNotFound
		}
		return 0, fmt.Errorf("authorize empty trash: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE drive.item SET purge_after = now()
		WHERE drive_id = $1 AND trashed_at IS NOT NULL
	`, driveID)
	if err != nil {
		return 0, fmt.Errorf("queue empty trash: %w", err)
	}
	count := command.RowsAffected()
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, actor_user_id, event_type, details)
		VALUES ($1, $2, 'trash.empty_requested', jsonb_build_object('item_count', $3::bigint))
	`, driveID, actorID, count); err != nil {
		return 0, fmt.Errorf("record empty trash request: %w", err)
	}
	if idempotencyKey != "" {
		response, err := json.Marshal(struct {
			Count int64 `json:"count"`
		}{count})
		if err != nil {
			return 0, fmt.Errorf("encode empty trash result: %w", err)
		}
		if err := completeMutationRequestWithResponse(ctx, tx, actorID, idempotencyKey, response); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit empty trash request: %w", err)
	}
	return count, nil
}

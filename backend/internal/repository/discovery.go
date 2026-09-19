package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"surajdrive/backend/internal/model"
	pagecursor "surajdrive/backend/internal/pagination"
)

const (
	UserViewRecent  = "recent"
	UserViewStarred = "starred"
	UserViewStorage = "storage"
)

type StorageSummary struct {
	QuotaBytes     int64 `json:"quota_bytes"`
	CommittedBytes int64 `json:"committed_bytes"`
	ReservedBytes  int64 `json:"reserved_bytes"`
	TrashBytes     int64 `json:"trash_bytes"`
	VersionBytes   int64 `json:"version_bytes"`
	ObjectCount    int64 `json:"object_count"`
}

func (m *Metadata) ListUserViewCursor(ctx context.Context, drivePublicID, userPublicID, view string, after *pagecursor.Position, limit int) (model.ListResponse, *pagecursor.Position, error) {
	positionExpression := "(extract(epoch FROM state.last_opened_at) * 1000000)::bigint"
	statePredicate := "state.last_opened_at IS NOT NULL"
	itemPredicate := "item.kind IN ('folder', 'file', 'shortcut')"
	if view == UserViewStarred {
		positionExpression = "(extract(epoch FROM state.starred_at) * 1000000)::bigint"
		statePredicate = "state.starred_at IS NOT NULL"
	} else if view == UserViewStorage {
		positionExpression = "COALESCE(version.size_bytes, 0)"
		statePredicate = "TRUE"
		itemPredicate = "item.kind = 'file' AND version.id IS NOT NULL"
	} else if view != UserViewRecent {
		return model.ListResponse{}, nil, fmt.Errorf("unsupported item view")
	}

	hasCursor := after != nil
	positionValue := int64(0)
	positionID := int64(0)
	if after != nil {
		positionValue = after.Time
		positionID = after.ID
	}
	query := fmt.Sprintf(`
		WITH RECURSIVE actor AS (
			SELECT id FROM drive.user_account WHERE public_id = $2::uuid AND status = 'active'
		), permission_tree AS (
			SELECT permission.item_id AS id
			FROM drive.item_permission permission, actor
			WHERE permission.grantee_user_id = actor.id
				AND (permission.expires_at IS NULL OR permission.expires_at > now())
			UNION
			SELECT child.id FROM drive.item child JOIN permission_tree parent ON child.parent_id = parent.id
			WHERE child.trashed_at IS NULL
		)
		SELECT item.id, item.public_id::text, item.kind, item.name,
			COALESCE(version.storage_key, ''), COALESCE(version.size_bytes, 0),
			COALESCE(version.source_modified_at, version.created_at, item.updated_at),
			COALESCE(version.mime_type, ''), COALESCE(version.storage_etag, ''),
			state.starred_at, state.last_opened_at, member.user_id IS NULL AS shared_item,
			COALESCE(item.folder_color, ''), %s AS position_value
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		CROSS JOIN actor
		LEFT JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
		LEFT JOIN drive.user_item_state state ON state.item_id = item.id AND state.user_id = actor.id
		LEFT JOIN drive.file_version version
			ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE drive.status = 'active'
			AND (member.user_id IS NOT NULL OR item.id IN (SELECT id FROM permission_tree))
			AND (NOT $7::boolean OR drive.public_id = $1::uuid)
			AND item.parent_id IS NOT NULL AND item.trashed_at IS NULL
			AND %s AND %s
			AND (item.kind IN ('folder', 'shortcut') OR version.id IS NOT NULL)
			AND (NOT $3::boolean OR %s < $4 OR (%s = $4 AND item.id < $5))
		ORDER BY position_value DESC, item.id DESC
		LIMIT $6
	`, positionExpression, statePredicate, itemPredicate, positionExpression, positionExpression)
	rows, err := m.pool.Query(ctx, query, drivePublicID, userPublicID, hasCursor, positionValue, positionID, limit+1, view == UserViewStorage)
	if err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("list %s items: %w", view, err)
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
			file        model.FileObject
			starredAt   *time.Time
			openedAt    *time.Time
			shared      bool
			folderColor string
			value       int64
		)
		if err := rows.Scan(
			&internalID, &publicID, &kind, &name, &file.Key, &file.Size,
			&file.LastModified, &file.ContentType, &file.ETag,
			&starredAt, &openedAt, &shared, &folderColor, &value,
		); err != nil {
			return model.ListResponse{}, nil, fmt.Errorf("scan %s item: %w", view, err)
		}
		positions = append(positions, pagecursor.Position{Time: value, ID: internalID})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt, Shared: shared, FolderColor: folderColor})
		} else if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name, StarredAt: starredAt, LastOpenedAt: openedAt, Shared: shared})
		} else {
			file.ID = publicID
			file.Name = name
			file.StarredAt = starredAt
			file.LastOpenedAt = openedAt
			file.Shared = shared
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("iterate %s items: %w", view, err)
	}
	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.ListResponse{
		Folders:   folders,
		Files:     files,
		Shortcuts: shortcuts,
		Pagination: model.Pagination{
			Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore,
		},
	}, next, nil
}

func (m *Metadata) SetItemStarred(ctx context.Context, drivePublicID, userPublicID, itemPublicID string, starred bool) error {
	return m.setItemStarred(ctx, userPublicID, itemPublicID, starred, "")
}

func (m *Metadata) SetItemStarredIdempotent(ctx context.Context, userPublicID, itemPublicID string, starred bool, idempotencyKey string) error {
	return m.setItemStarred(ctx, userPublicID, itemPublicID, starred, idempotencyKey)
}

func (m *Metadata) setItemStarred(ctx context.Context, userPublicID, itemPublicID string, starred bool, idempotencyKey string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin star update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return err
	}
	if idempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%t", itemPublicID, starred)))
		replayed, err := claimMutationRequest(ctx, tx, access.UserID, idempotencyKey, "item.star", requestHash[:])
		if err != nil {
			return err
		}
		if replayed {
			return tx.Commit(ctx)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.user_item_state (user_id, item_id, starred_at)
		VALUES ($1, $2, CASE WHEN $3 THEN now() ELSE NULL END)
		ON CONFLICT (user_id, item_id) DO UPDATE
		SET starred_at = CASE WHEN $3 THEN now() ELSE NULL END
	`, access.UserID, access.ItemID, starred); err != nil {
		return fmt.Errorf("update starred state: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
		VALUES ($1, $2, $3, CASE WHEN $4 THEN 'item.starred' ELSE 'item.unstarred' END)
	`, access.DriveID, access.ItemID, access.UserID, starred); err != nil {
		return fmt.Errorf("record starred state: %w", err)
	}
	if idempotencyKey != "" {
		if err := completeMutationRequest(ctx, tx, access.UserID, idempotencyKey); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (m *Metadata) MarkItemOpened(ctx context.Context, drivePublicID, userPublicID, itemPublicID string) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin item opened update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, userPublicID, itemPublicID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.user_item_state (user_id, item_id, last_opened_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id, item_id) DO UPDATE SET last_opened_at = now()
	`, access.UserID, access.ItemID); err != nil {
		return fmt.Errorf("mark item opened: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
		VALUES ($1, $2, $3, 'item.opened')
	`, access.DriveID, access.ItemID, access.UserID); err != nil {
		return fmt.Errorf("record item opened: %w", err)
	}
	return tx.Commit(ctx)
}

func (m *Metadata) GetStorageSummary(ctx context.Context, drivePublicID, userPublicID string) (StorageSummary, error) {
	var summary StorageSummary
	err := m.pool.QueryRow(ctx, `
		WITH selected_drive AS (
			SELECT drive.id, actor.storage_quota_bytes
			FROM drive.drive_space drive
			JOIN drive.user_account actor ON actor.public_id = $2::uuid AND actor.status = 'active'
			JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
			WHERE drive.public_id = $1::uuid AND drive.status = 'active'
		), versions AS (
			SELECT COALESCE(sum(`+chargedVersionBytes+`) FILTER (WHERE NOT COALESCE(`+activeReservation+`, false)), 0)::bigint AS committed,
				COALESCE(sum(`+chargedVersionBytes+`) FILTER (WHERE `+activeReservation+`), 0)::bigint AS reserved,
				COALESCE(sum(version.size_bytes) FILTER (WHERE version.state = 'ready' AND NOT version.is_current AND item.trashed_at IS NULL), 0)::bigint AS history,
				COALESCE(sum(version.size_bytes) FILTER (WHERE item.trashed_at IS NOT NULL), 0)::bigint AS trash,
				count(*) FILTER (WHERE version.is_current)::bigint AS objects
			FROM selected_drive drive
			JOIN drive.item item ON item.drive_id = drive.id
			JOIN drive.file_version version ON version.item_id = item.id AND version.state <> 'deleted'
			LEFT JOIN drive.upload_session session ON session.file_version_id = version.id
		)
		SELECT drive.storage_quota_bytes, versions.committed, versions.reserved,
			versions.trash, versions.history, versions.objects
		FROM selected_drive drive CROSS JOIN versions
	`, drivePublicID, userPublicID).Scan(
		&summary.QuotaBytes, &summary.CommittedBytes, &summary.ReservedBytes,
		&summary.TrashBytes, &summary.VersionBytes, &summary.ObjectCount,
	)
	if err != nil {
		return StorageSummary{}, fmt.Errorf("get storage summary: %w", err)
	}
	return summary, nil
}

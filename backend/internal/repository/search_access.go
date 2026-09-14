package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"surajdrive/backend/internal/model"
	pagecursor "surajdrive/backend/internal/pagination"
)

func (m *Metadata) SearchAccessibleCursor(ctx context.Context, userPublicID, query string, after *pagecursor.Position, limit int) (model.SearchResponse, *pagecursor.Position, error) {
	return m.AdvancedSearchAccessibleCursor(ctx, userPublicID, query, SearchFilters{}, after, limit)
}

type SearchFilters struct {
	Type           string
	Owner          string
	LocationItemID string
	Shared         string
	Starred        string
	Trash          string
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
}

func (m *Metadata) AdvancedSearchAccessibleCursor(ctx context.Context, userPublicID, query string, filters SearchFilters, after *pagecursor.Position, limit int) (model.SearchResponse, *pagecursor.Position, error) {
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return model.SearchResponse{}, nil, fmt.Errorf("query is required")
	}
	hasCursor := after != nil
	position := pagecursor.Position{}
	if after != nil {
		position = *after
	}
	rows, err := m.pool.Query(ctx, `
		WITH RECURSIVE actor AS (
			SELECT id, primary_email FROM drive.user_account WHERE public_id = $1::uuid AND status = 'active'
		), permission_tree AS (
			SELECT permission.item_id AS id
			FROM drive.item_permission permission, actor
			WHERE permission.grantee_user_id = actor.id
				AND (permission.expires_at IS NULL OR permission.expires_at > now())
			UNION
			SELECT child.id FROM drive.item child JOIN permission_tree parent ON child.parent_id = parent.id
		), location_tree AS (
			SELECT location.id FROM drive.item location, actor
			WHERE NULLIF($9, '')::uuid IS NOT NULL AND location.public_id = NULLIF($9, '')::uuid
				AND (EXISTS (SELECT 1 FROM drive.drive_member member WHERE member.drive_id = location.drive_id AND member.user_id = actor.id)
					OR location.id IN (SELECT id FROM permission_tree))
			UNION ALL
			SELECT child.id FROM drive.item child JOIN location_tree parent ON child.parent_id = parent.id
		), matches AS (
			SELECT item.id, item.public_id::text AS public_id, item.kind, item.name,
				lower(item.name) AS sort_name, similarity(item.name, $2)::double precision AS score,
				COALESCE(version.size_bytes, 0) AS size_bytes,
				COALESCE(version.source_modified_at, version.created_at, item.updated_at) AS last_modified,
				COALESCE(version.mime_type, '') AS mime_type, COALESCE(version.storage_etag, '') AS storage_etag,
				member.user_id IS NULL AS shared_item, state.starred_at, COALESCE(item.folder_color, '') AS folder_color
			FROM drive.item item
			JOIN drive.drive_space drive ON drive.id = item.drive_id AND drive.status = 'active'
			CROSS JOIN actor
			JOIN drive.user_account owner_account ON owner_account.id = item.owner_user_id
			LEFT JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = actor.id
			LEFT JOIN drive.user_item_state state ON state.item_id = item.id AND state.user_id = actor.id
			LEFT JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
			WHERE item.parent_id IS NOT NULL
				AND (member.user_id IS NOT NULL OR item.id IN (SELECT id FROM permission_tree))
				AND item.kind IN ('folder', 'file', 'shortcut') AND (item.kind IN ('folder', 'shortcut') OR version.id IS NOT NULL)
				AND item.name ILIKE '%' || $2 || '%'
				AND ($8 = '' OR ($8 = 'folder' AND item.kind = 'folder') OR ($8 = 'file' AND item.kind = 'file')
					OR ($8 = 'image' AND version.mime_type LIKE 'image/%') OR ($8 = 'video' AND version.mime_type LIKE 'video/%')
					OR ($8 = 'audio' AND version.mime_type LIKE 'audio/%') OR ($8 = 'pdf' AND version.mime_type = 'application/pdf')
					OR ($8 = 'archive' AND version.mime_type IN ('application/zip', 'application/x-tar', 'application/gzip', 'application/x-7z-compressed')))
				AND ($10 = '' OR ($10 = 'me' AND owner_account.id = actor.id) OR lower(owner_account.primary_email) = lower($10))
				AND ($9 = '' OR item.id IN (SELECT id FROM location_tree))
				AND ($11 = 'all' OR ($11 = 'yes' AND member.user_id IS NULL) OR ($11 = 'no' AND member.user_id IS NOT NULL))
				AND ($12 = 'all' OR ($12 = 'yes' AND state.starred_at IS NOT NULL) OR ($12 = 'no' AND state.starred_at IS NULL))
				AND ($13 = 'all' OR ($13 = 'only' AND item.trashed_at IS NOT NULL) OR ($13 = 'exclude' AND item.trashed_at IS NULL))
				AND ($14::timestamptz IS NULL OR COALESCE(version.source_modified_at, version.created_at, item.updated_at) >= $14)
				AND ($15::timestamptz IS NULL OR COALESCE(version.source_modified_at, version.created_at, item.updated_at) <= $15)
		)
		SELECT id, public_id, kind, name, sort_name, score,
			size_bytes, last_modified, mime_type, storage_etag, shared_item, folder_color
		FROM matches
		WHERE NOT $3::boolean OR score < $4
			OR (score = $4 AND (sort_name > $5 OR (sort_name = $5 AND id > $6)))
		ORDER BY score DESC, sort_name, id
		LIMIT $7
	`, userPublicID, trimmedQuery, hasCursor, position.Score, position.Name, position.ID, limit+1,
		filters.Type, filters.LocationItemID, filters.Owner, normalizedTriState(filters.Shared),
		normalizedTriState(filters.Starred), normalizedTrashState(filters.Trash), filters.ModifiedAfter, filters.ModifiedBefore)
	if err != nil {
		return model.SearchResponse{}, nil, fmt.Errorf("search accessible items: %w", err)
	}
	defer rows.Close()
	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var internalID int64
		var publicID, kind, name, sortName string
		var score float64
		var shared bool
		var folderColor string
		var file model.FileObject
		if err := rows.Scan(
			&internalID, &publicID, &kind, &name, &sortName, &score,
			&file.Size, &file.LastModified, &file.ContentType, &file.ETag, &shared, &folderColor,
		); err != nil {
			return model.SearchResponse{}, nil, fmt.Errorf("scan accessible search result: %w", err)
		}
		positions = append(positions, pagecursor.Position{Score: score, Name: sortName, ID: internalID})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{ID: publicID, Name: name, Shared: shared, FolderColor: folderColor})
		} else if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name, Shared: shared})
		} else {
			file.ID, file.Name, file.Shared = publicID, name, shared
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.SearchResponse{}, nil, fmt.Errorf("iterate accessible search results: %w", err)
	}
	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.SearchResponse{
		Query: trimmedQuery, Folders: folders, Results: files, Shortcuts: shortcuts,
		Pagination: model.Pagination{Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore},
	}, next, nil
}

func normalizedTriState(value string) string {
	if value == "yes" || value == "no" {
		return value
	}
	return "all"
}

func normalizedTrashState(value string) string {
	if value == "only" || value == "all" {
		return value
	}
	return "exclude"
}

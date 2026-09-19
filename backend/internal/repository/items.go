package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/model"
	pagecursor "surajdrive/backend/internal/pagination"
)

func (m *Metadata) ListDriveCursor(ctx context.Context, drivePublicID, prefix string, after *pagecursor.Position, limit int) (model.ListResponse, *pagecursor.Position, error) {
	driveID, parentID, normalizedPrefix, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return model.ListResponse{}, nil, err
	}
	var currentFolderID string
	if err := m.pool.QueryRow(ctx, `SELECT public_id::text FROM drive.item WHERE id = $1`, parentID).Scan(&currentFolderID); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("load current folder id: %w", err)
	}

	hasCursor := after != nil
	position := pagecursor.Position{}
	if after != nil {
		position = *after
	}
	rows, err := m.pool.Query(ctx, `
		WITH children AS (
			SELECT i.id, i.public_id::text AS public_id, i.kind, i.name,
				lower(i.name) AS sort_name,
				CASE WHEN i.kind = 'folder' THEN 0 ELSE 1 END AS item_group,
				COALESCE(fv.storage_key, '') AS storage_key,
				COALESCE(fv.size_bytes, 0) AS size_bytes,
				COALESCE(fv.source_modified_at, fv.created_at, i.updated_at) AS last_modified,
				COALESCE(fv.mime_type, '') AS mime_type,
				COALESCE(fv.storage_etag, '') AS storage_etag,
				COALESCE(i.folder_color, '') AS folder_color
			FROM drive.item i
			LEFT JOIN drive.file_version fv
				ON fv.item_id = i.id AND fv.is_current AND fv.state = 'ready'
			WHERE i.drive_id = $1 AND i.parent_id = $2
				AND i.trashed_at IS NULL AND i.kind IN ('folder', 'file', 'shortcut')
				AND (i.kind IN ('folder', 'shortcut') OR fv.id IS NOT NULL)
		)
		SELECT id, public_id, kind, name, sort_name, item_group,
			storage_key, size_bytes, last_modified, mime_type, storage_etag, folder_color
		FROM children
		WHERE NOT $3::boolean
			OR item_group > $4
			OR (item_group = $4 AND (
				sort_name > $5
				OR (sort_name = $5 AND id > $6)
			))
		ORDER BY item_group, sort_name, id
		LIMIT $7
	`, driveID, parentID, hasCursor, position.Group, position.Name, position.ID, limit+1)
	if err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("list drive page: %w", err)
	}
	defer rows.Close()

	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var (
			id          int64
			publicID    string
			kind        string
			name        string
			sortName    string
			group       int
			storageKey  string
			size        int64
			file        model.FileObject
			folderColor string
		)
		if err := rows.Scan(
			&id, &publicID, &kind, &name, &sortName, &group,
			&storageKey, &size, &file.LastModified,
			&file.ContentType, &file.ETag, &folderColor,
		); err != nil {
			return model.ListResponse{}, nil, fmt.Errorf("scan drive page: %w", err)
		}
		positions = append(positions, pagecursor.Position{Group: group, Name: sortName, ID: id})
		if len(positions) > limit {
			continue
		}
		if kind == "folder" {
			folders = append(folders, model.FolderEntry{
				ID: publicID, Name: name, Prefix: normalizedPrefix + name + "/", FolderColor: folderColor,
			})
			continue
		}
		if kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: publicID, Name: name})
			continue
		}
		file.ID = publicID
		file.Key = storageKey
		file.Name = name
		file.Size = size
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return model.ListResponse{}, nil, fmt.Errorf("iterate drive page: %w", err)
	}

	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	returned := len(folders) + len(files) + len(shortcuts)
	return model.ListResponse{
		CurrentFolderID: currentFolderID,
		Prefix:          normalizedPrefix,
		Folders:         folders,
		Files:           files,
		Shortcuts:       shortcuts,
		Pagination: model.Pagination{
			Limit: limit, Returned: returned, HasMore: hasMore,
		},
	}, next, nil
}

func (m *Metadata) ListDrive(ctx context.Context, drivePublicID, prefix string, offset, limit int) (model.ListResponse, error) {
	driveID, parentID, normalizedPrefix, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return model.ListResponse{}, err
	}
	var currentFolderID string
	if err := m.pool.QueryRow(ctx, `SELECT public_id::text FROM drive.item WHERE id = $1`, parentID).Scan(&currentFolderID); err != nil {
		return model.ListResponse{}, fmt.Errorf("load current folder id: %w", err)
	}

	folders := make([]model.FolderEntry, 0)
	folderRows, err := m.pool.Query(ctx, `
		SELECT public_id::text, name, COALESCE(folder_color, '')
		FROM drive.item
		WHERE drive_id = $1 AND parent_id = $2 AND kind = 'folder' AND trashed_at IS NULL
		ORDER BY lower(name), id
	`, driveID, parentID)
	if err != nil {
		return model.ListResponse{}, fmt.Errorf("list folders: %w", err)
	}
	for folderRows.Next() {
		var folder model.FolderEntry
		if err := folderRows.Scan(&folder.ID, &folder.Name, &folder.FolderColor); err != nil {
			folderRows.Close()
			return model.ListResponse{}, fmt.Errorf("scan folder: %w", err)
		}
		folder.Prefix = normalizedPrefix + folder.Name + "/"
		folders = append(folders, folder)
	}
	if err := folderRows.Err(); err != nil {
		folderRows.Close()
		return model.ListResponse{}, fmt.Errorf("iterate folders: %w", err)
	}
	folderRows.Close()

	files := make([]model.FileObject, 0)
	fileRows, err := m.pool.Query(ctx, `
		SELECT i.public_id::text, fv.storage_key, i.name, fv.size_bytes,
			COALESCE(fv.source_modified_at, fv.created_at), fv.mime_type, fv.storage_etag
		FROM drive.item i
		JOIN drive.file_version fv ON fv.item_id = i.id AND fv.is_current AND fv.state = 'ready'
		WHERE i.drive_id = $1 AND i.parent_id = $2 AND i.kind = 'file' AND i.trashed_at IS NULL
		ORDER BY lower(i.name), i.id
	`, driveID, parentID)
	if err != nil {
		return model.ListResponse{}, fmt.Errorf("list files: %w", err)
	}
	for fileRows.Next() {
		var file model.FileObject
		if err := fileRows.Scan(
			&file.ID,
			&file.Key,
			&file.Name,
			&file.Size,
			&file.LastModified,
			&file.ContentType,
			&file.ETag,
		); err != nil {
			fileRows.Close()
			return model.ListResponse{}, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, file)
	}
	if err := fileRows.Err(); err != nil {
		fileRows.Close()
		return model.ListResponse{}, fmt.Errorf("iterate files: %w", err)
	}
	fileRows.Close()

	pagedFolders, pagedFiles, pagination := paginateMetadata(folders, files, offset, limit)
	return model.ListResponse{
		CurrentFolderID: currentFolderID,
		Prefix:          normalizedPrefix,
		Folders:         pagedFolders,
		Files:           pagedFiles,
		Pagination:      pagination,
	}, nil
}

func (m *Metadata) resolveFolderByPublicID(ctx context.Context, drivePublicID, folderPublicID string) (int64, int64, error) {
	var driveID, folderID int64
	err := m.pool.QueryRow(ctx, `
		SELECT drive.id, folder.id
		FROM drive.drive_space drive
		JOIN drive.item folder ON folder.drive_id = drive.id
		WHERE drive.public_id = $1::uuid AND drive.status = 'active'
			AND folder.public_id::text = $2 AND folder.kind = 'folder'
			AND folder.trashed_at IS NULL
	`, drivePublicID, folderPublicID).Scan(&driveID, &folderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrItemNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("resolve destination folder: %w", err)
	}
	return driveID, folderID, nil
}

func (m *Metadata) SearchDrive(ctx context.Context, drivePublicID, prefix, query string, offset, limit int) (model.SearchResponse, error) {
	_, parentID, normalizedPrefix, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return model.SearchResponse{}, err
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return model.SearchResponse{}, fmt.Errorf("query is required")
	}

	rows, err := m.pool.Query(ctx, `
		WITH RECURSIVE descendants AS (
			SELECT id FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id
			FROM drive.item child
			JOIN descendants parent ON child.parent_id = parent.id
			WHERE child.trashed_at IS NULL
		)
		SELECT i.public_id::text, fv.storage_key, i.name, fv.size_bytes,
			COALESCE(fv.source_modified_at, fv.created_at), fv.mime_type, fv.storage_etag,
			count(*) OVER ()
		FROM descendants tree
		JOIN drive.item i ON i.id = tree.id
		JOIN drive.file_version fv ON fv.item_id = i.id AND fv.is_current AND fv.state = 'ready'
		WHERE i.kind = 'file' AND i.trashed_at IS NULL AND i.name ILIKE '%' || $2 || '%'
		ORDER BY similarity(i.name, $2) DESC, lower(i.name), i.id
		OFFSET $3 LIMIT $4
	`, parentID, trimmedQuery, offset, limit)
	if err != nil {
		return model.SearchResponse{}, fmt.Errorf("search drive: %w", err)
	}
	defer rows.Close()

	files := make([]model.FileObject, 0)
	total := 0
	for rows.Next() {
		var file model.FileObject
		if err := rows.Scan(
			&file.ID,
			&file.Key,
			&file.Name,
			&file.Size,
			&file.LastModified,
			&file.ContentType,
			&file.ETag,
			&total,
		); err != nil {
			return model.SearchResponse{}, fmt.Errorf("scan search result: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return model.SearchResponse{}, fmt.Errorf("iterate search results: %w", err)
	}

	return model.SearchResponse{
		Query:      trimmedQuery,
		Prefix:     normalizedPrefix,
		Results:    files,
		Pagination: metadataPagination(offset, limit, total, len(files)),
	}, nil
}

func (m *Metadata) SearchDriveCursor(ctx context.Context, drivePublicID, prefix, query string, after *pagecursor.Position, limit int) (model.SearchResponse, *pagecursor.Position, error) {
	_, parentID, normalizedPrefix, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return model.SearchResponse{}, nil, err
	}
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
		WITH RECURSIVE descendants AS (
			SELECT id, ''::text AS relative_path FROM drive.item WHERE id = $1
			UNION ALL
			SELECT child.id,
				CASE WHEN parent.relative_path = '' THEN child.name
					ELSE parent.relative_path || '/' || child.name END
			FROM drive.item child
			JOIN descendants parent ON child.parent_id = parent.id
			WHERE child.trashed_at IS NULL
		), matches AS (
			SELECT i.id, i.public_id::text AS public_id, i.kind, tree.relative_path,
				COALESCE(fv.storage_key, '') AS storage_key, i.name,
				lower(i.name) AS sort_name, similarity(i.name, $2)::double precision AS score,
				COALESCE(fv.size_bytes, 0) AS size_bytes,
				COALESCE(fv.source_modified_at, fv.created_at, i.updated_at) AS last_modified,
				COALESCE(fv.mime_type, '') AS mime_type, COALESCE(fv.storage_etag, '') AS storage_etag,
				COALESCE(i.folder_color, '') AS folder_color
			FROM descendants tree
			JOIN drive.item i ON i.id = tree.id
			LEFT JOIN drive.file_version fv ON fv.item_id = i.id AND fv.is_current AND fv.state = 'ready'
			WHERE i.id <> $1 AND i.kind IN ('folder', 'file', 'shortcut') AND i.trashed_at IS NULL
				AND (i.kind IN ('folder', 'shortcut') OR fv.id IS NOT NULL)
				AND i.name ILIKE '%' || $2 || '%'
		)
		SELECT id, public_id, kind, relative_path, storage_key, name, sort_name, score,
			size_bytes, last_modified, mime_type, storage_etag, folder_color
		FROM matches
		WHERE NOT $3::boolean
			OR score < $4
			OR (score = $4 AND (
				sort_name > $5
				OR (sort_name = $5 AND id > $6)
			))
		ORDER BY score DESC, sort_name, id
		LIMIT $7
	`, parentID, trimmedQuery, hasCursor, position.Score, position.Name, position.ID, limit+1)
	if err != nil {
		return model.SearchResponse{}, nil, fmt.Errorf("search drive page: %w", err)
	}
	defer rows.Close()

	folders := make([]model.FolderEntry, 0, limit)
	files := make([]model.FileObject, 0, limit)
	shortcuts := make([]model.ShortcutEntry, 0, limit)
	positions := make([]pagecursor.Position, 0, limit+1)
	for rows.Next() {
		var (
			id           int64
			kind         string
			relativePath string
			sortName     string
			score        float64
			file         model.FileObject
			folderColor  string
		)
		if err := rows.Scan(
			&id, &file.ID, &kind, &relativePath, &file.Key, &file.Name, &sortName, &score,
			&file.Size, &file.LastModified, &file.ContentType, &file.ETag, &folderColor,
		); err != nil {
			return model.SearchResponse{}, nil, fmt.Errorf("scan search page: %w", err)
		}
		positions = append(positions, pagecursor.Position{Name: sortName, ID: id, Score: score})
		if len(positions) <= limit && kind == "folder" {
			folders = append(folders, model.FolderEntry{
				ID: file.ID, Name: file.Name, Prefix: strings.Trim(relativePath, "/") + "/", FolderColor: folderColor,
			})
		} else if len(positions) <= limit && kind == "shortcut" {
			shortcuts = append(shortcuts, model.ShortcutEntry{ID: file.ID, Name: file.Name})
		} else if len(positions) <= limit {
			files = append(files, file)
		}
	}
	if err := rows.Err(); err != nil {
		return model.SearchResponse{}, nil, fmt.Errorf("iterate search page: %w", err)
	}

	hasMore := len(positions) > limit
	var next *pagecursor.Position
	if hasMore {
		last := positions[limit-1]
		next = &last
	}
	return model.SearchResponse{
		Query: trimmedQuery, Prefix: normalizedPrefix, Folders: folders, Results: files, Shortcuts: shortcuts,
		Pagination: model.Pagination{Limit: limit, Returned: len(folders) + len(files) + len(shortcuts), HasMore: hasMore},
	}, next, nil
}

func (m *Metadata) resolveFolder(ctx context.Context, drivePublicID, prefix string) (int64, int64, string, error) {
	var driveID, folderID int64
	if err := m.pool.QueryRow(ctx, `
		SELECT d.id, root.id
		FROM drive.drive_space d
		JOIN drive.item root ON root.drive_id = d.id AND root.parent_id IS NULL
		WHERE d.public_id = $1::uuid AND d.status = 'active'
	`, drivePublicID).Scan(&driveID, &folderID); err != nil {
		return 0, 0, "", fmt.Errorf("resolve drive root: %w", err)
	}

	trimmedPrefix := strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/ ")
	if trimmedPrefix == "" {
		return driveID, folderID, "", nil
	}
	segments := splitLegacyKey(trimmedPrefix, false)
	if len(segments) == 0 {
		return 0, 0, "", fmt.Errorf("invalid folder prefix")
	}
	for _, segment := range segments {
		err := m.pool.QueryRow(ctx, `
			SELECT id
			FROM drive.item
			WHERE drive_id = $1 AND parent_id = $2 AND kind = 'folder'
				AND name = $3 AND trashed_at IS NULL
			ORDER BY id
			LIMIT 1
		`, driveID, folderID, segment).Scan(&folderID)
		if err != nil {
			if err == pgx.ErrNoRows {
				return 0, 0, "", fmt.Errorf("folder not found")
			}
			return 0, 0, "", fmt.Errorf("resolve folder %q: %w", segment, err)
		}
	}
	return driveID, folderID, strings.Join(segments, "/") + "/", nil
}

func paginateMetadata(folders []model.FolderEntry, files []model.FileObject, offset, limit int) ([]model.FolderEntry, []model.FileObject, model.Pagination) {
	total := len(folders) + len(files)
	start := min(offset, total)
	end := min(start+limit, total)
	folderStart := min(start, len(folders))
	folderEnd := min(end, len(folders))
	fileStart := max(0, start-len(folders))
	fileEnd := min(max(0, end-len(folders)), len(files))
	pagedFolders := folders[folderStart:folderEnd]
	pagedFiles := files[fileStart:fileEnd]
	return pagedFolders, pagedFiles, metadataPagination(offset, limit, total, len(pagedFolders)+len(pagedFiles))
}

func metadataPagination(offset, limit, total, returned int) model.Pagination {
	hasMore := offset+returned < total
	var nextOffset *int
	if hasMore {
		next := offset + returned
		nextOffset = &next
	}
	return model.Pagination{
		Offset: offset, Limit: limit, Returned: returned, Total: total,
		HasMore: hasMore, NextOffset: nextOffset,
	}
}

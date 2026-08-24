package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"surajdrive/backend/internal/model"
)

func (m *Metadata) ListDrive(ctx context.Context, drivePublicID, prefix string, offset, limit int) (model.ListResponse, error) {
	driveID, parentID, normalizedPrefix, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return model.ListResponse{}, err
	}

	folders := make([]model.FolderEntry, 0)
	folderRows, err := m.pool.Query(ctx, `
		SELECT public_id::text, name
		FROM drive.item
		WHERE drive_id = $1 AND parent_id = $2 AND kind = 'folder' AND trashed_at IS NULL
		ORDER BY lower(name), id
	`, driveID, parentID)
	if err != nil {
		return model.ListResponse{}, fmt.Errorf("list folders: %w", err)
	}
	for folderRows.Next() {
		var folder model.FolderEntry
		if err := folderRows.Scan(&folder.ID, &folder.Name); err != nil {
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
		Prefix:     normalizedPrefix,
		Folders:    pagedFolders,
		Files:      pagedFiles,
		Pagination: pagination,
	}, nil
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

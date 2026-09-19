package repository

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LegacyObject struct {
	Bucket       string
	Key          string
	ETag         string
	SizeBytes    int64
	MIMEType     string
	LastModified time.Time
	FolderMarker bool
}

func (m *Metadata) ImportLegacyObject(ctx context.Context, drivePublicID string, object LegacyObject) (bool, error) {
	return m.recordStoredObject(ctx, drivePublicID, object, true)
}

func (m *Metadata) RecordStoredObject(ctx context.Context, drivePublicID string, object LegacyObject) (bool, error) {
	return m.recordStoredObject(ctx, drivePublicID, object, false)
}

func (m *Metadata) recordStoredObject(ctx context.Context, drivePublicID string, object LegacyObject, legacy bool) (bool, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin legacy import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if legacy {
		var alreadyImported bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM drive.legacy_import_record
				WHERE legacy_bucket = $1 AND legacy_key = $2 AND legacy_etag = $3
					AND status IN ('imported', 'skipped')
			)
		`, object.Bucket, object.Key, object.ETag).Scan(&alreadyImported); err != nil {
			return false, fmt.Errorf("check legacy import: %w", err)
		}
		if alreadyImported {
			return false, nil
		}
	}

	var driveID, ownerUserID, rootItemID int64
	if err := tx.QueryRow(ctx, `
		SELECT d.id, d.owner_user_id, root.id
		FROM drive.drive_space d
		JOIN drive.item root ON root.drive_id = d.id AND root.parent_id IS NULL
		WHERE d.public_id = $1::uuid AND d.kind = 'personal' AND d.status = 'active'
		FOR UPDATE OF d
	`, drivePublicID).Scan(&driveID, &ownerUserID, &rootItemID); err != nil {
		return false, fmt.Errorf("load drive for legacy import: %w", err)
	}

	segments := splitLegacyKey(object.Key, object.FolderMarker)
	if len(segments) == 0 {
		return false, fmt.Errorf("legacy object key %q has no importable path", object.Key)
	}
	parentID := rootItemID
	folderCount := len(segments) - 1
	if object.FolderMarker {
		folderCount = len(segments)
	}
	for _, folderName := range segments[:folderCount] {
		parentID, err = ensureLegacyFolder(ctx, tx, driveID, ownerUserID, parentID, folderName)
		if err != nil {
			return false, err
		}
	}

	if object.FolderMarker {
		if legacy {
			if _, err := tx.Exec(ctx, `
				INSERT INTO drive.legacy_import_record (
					drive_id, legacy_bucket, legacy_key, legacy_etag, item_id, status
				)
				VALUES ($1, $2, $3, $4, $5, 'skipped')
				ON CONFLICT (legacy_bucket, legacy_key, legacy_etag) DO UPDATE
				SET item_id = EXCLUDED.item_id, status = 'skipped', last_error = NULL
			`, driveID, object.Bucket, object.Key, object.ETag, parentID); err != nil {
				return false, fmt.Errorf("record legacy folder marker: %w", err)
			}
		} else if _, err := tx.Exec(ctx, `
			INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
			VALUES ($1, $2, $3, 'folder.created')
		`, driveID, parentID, ownerUserID); err != nil {
			return false, fmt.Errorf("record folder activity: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit legacy folder import: %w", err)
		}
		return true, nil
	}

	fileName := segments[len(segments)-1]
	var itemID, fileVersionID int64
	err = tx.QueryRow(ctx, `
		SELECT fv.item_id, fv.id
		FROM drive.file_version fv
		JOIN drive.item i ON i.id = fv.item_id
		WHERE fv.storage_bucket = $1 AND fv.storage_key = $2 AND i.drive_id = $3
		FOR UPDATE OF fv
	`, object.Bucket, object.Key, driveID).Scan(&itemID, &fileVersionID)
	switch {
	case err == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE drive.item SET parent_id = $2, name = $3 WHERE id = $1
		`, itemID, parentID, fileName); err != nil {
			return false, fmt.Errorf("update imported item path: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE drive.file_version
			SET storage_etag = $2, size_bytes = $3, mime_type = $4,
				source_modified_at = $5, state = 'ready', ready_at = now(),
				is_current = true, legacy_object = legacy_object OR $6
			WHERE id = $1
		`, fileVersionID, object.ETag, object.SizeBytes, object.MIMEType, object.LastModified, legacy); err != nil {
			return false, fmt.Errorf("refresh imported file metadata: %w", err)
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
			VALUES ($1, $2, 'file', $3, $4)
			RETURNING id
		`, driveID, parentID, fileName, ownerUserID).Scan(&itemID); err != nil {
			return false, fmt.Errorf("create imported file item: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO drive.file_version (
				item_id, version_number, state, storage_bucket, storage_key,
				storage_etag, size_bytes, mime_type, source_modified_at,
				created_by_user_id, is_current, legacy_object, ready_at
			)
			VALUES ($1, 1, 'ready', $2, $3, $4, $5, $6, $7, $8, true, $9, now())
			RETURNING id
		`, itemID, object.Bucket, object.Key, object.ETag, object.SizeBytes, object.MIMEType, object.LastModified, ownerUserID, legacy).Scan(&fileVersionID); err != nil {
			return false, fmt.Errorf("create imported file version: %w", err)
		}
	default:
		return false, fmt.Errorf("find existing imported file: %w", err)
	}

	if legacy {
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.legacy_import_record (
				drive_id, legacy_bucket, legacy_key, legacy_etag,
				item_id, file_version_id, status
			)
			VALUES ($1, $2, $3, $4, $5, $6, 'imported')
			ON CONFLICT (legacy_bucket, legacy_key, legacy_etag) DO UPDATE
			SET item_id = EXCLUDED.item_id, file_version_id = EXCLUDED.file_version_id,
				status = 'imported', last_error = NULL
		`, driveID, object.Bucket, object.Key, object.ETag, itemID, fileVersionID); err != nil {
			return false, fmt.Errorf("record legacy file import: %w", err)
		}
	}
	eventType := "file.created"
	if legacy {
		eventType = "legacy.imported"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, $4, $5, jsonb_build_object('storage_key', $6::text))
	`, driveID, itemID, fileVersionID, ownerUserID, eventType, object.Key); err != nil {
		return false, fmt.Errorf("record legacy import activity: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit legacy file import: %w", err)
	}
	return true, nil
}

func ensureLegacyFolder(ctx context.Context, tx pgx.Tx, driveID, ownerUserID, parentID int64, name string) (int64, error) {
	var folderID int64
	err := tx.QueryRow(ctx, `
		SELECT id
		FROM drive.item
		WHERE drive_id = $1 AND parent_id = $2 AND kind = 'folder' AND name = $3
		ORDER BY id
		LIMIT 1
	`, driveID, parentID, name).Scan(&folderID)
	if err == nil {
		return folderID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find imported folder %q: %w", name, err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
		VALUES ($1, $2, 'folder', $3, $4)
		RETURNING id
	`, driveID, parentID, name, ownerUserID).Scan(&folderID); err != nil {
		return 0, fmt.Errorf("create imported folder %q: %w", name, err)
	}
	return folderID, nil
}

func splitLegacyKey(key string, folderMarker bool) []string {
	cleaned := strings.Trim(strings.ReplaceAll(key, "\\", "/"), "/")
	if folderMarker {
		cleaned = strings.TrimSuffix(cleaned, "/.keep")
	}
	if cleaned == "" || cleaned == "." || path.Clean(cleaned) != cleaned {
		return nil
	}
	segments := strings.Split(cleaned, "/")
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			return nil
		}
	}
	return segments
}

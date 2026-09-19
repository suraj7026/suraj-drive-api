package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"surajdrive/backend/internal/validation"
)

var (
	ErrItemNotFound        = errors.New("item not found")
	ErrInvalidMove         = errors.New("item cannot be moved to that folder")
	ErrInvalidItemUpdate   = errors.New("invalid item update")
	ErrNameConflict        = errors.New("an item with that name already exists")
	ErrIdempotencyConflict = errors.New("idempotency key was already used for a different request")
)

type ItemDetails struct {
	ID          string `json:"id"`
	DriveID     string `json:"drive_id"`
	ParentID    string `json:"parent_id,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	FolderColor string `json:"folder_color,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	VersionID   string `json:"version_id,omitempty"`
	Trashed     bool   `json:"trashed"`
}

func (m *Metadata) EnsureFolder(ctx context.Context, drivePublicID, userPublicID, prefix, name string) (ItemDetails, error) {
	if err := validation.ItemName(name); err != nil {
		return ItemDetails{}, err
	}
	if err := validation.ItemPath(prefix, true); err != nil {
		return ItemDetails{}, err
	}
	driveID, parentID, _, err := m.resolveFolder(ctx, drivePublicID, prefix)
	if err != nil {
		return ItemDetails{}, err
	}
	return m.ensureFolderInParent(ctx, drivePublicID, userPublicID, driveID, parentID, name)
}

func (m *Metadata) EnsureFolderByID(ctx context.Context, drivePublicID, userPublicID, parentPublicID, name string) (ItemDetails, error) {
	if err := validation.ItemName(name); err != nil {
		return ItemDetails{}, err
	}
	driveID, parentID, err := m.resolveFolderByPublicID(ctx, drivePublicID, parentPublicID)
	if err != nil {
		return ItemDetails{}, err
	}
	return m.ensureFolderInParent(ctx, drivePublicID, userPublicID, driveID, parentID, name)
}

func (m *Metadata) EnsureFolderByIDIdempotent(ctx context.Context, drivePublicID, userPublicID, parentPublicID, name, idempotencyKey string) (ItemDetails, error) {
	if err := validation.ItemName(name); err != nil {
		return ItemDetails{}, err
	}
	requestPayload, err := json.Marshal(struct {
		DriveID  string `json:"drive_id"`
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}{drivePublicID, parentPublicID, name})
	if err != nil {
		return ItemDetails{}, fmt.Errorf("encode folder creation request: %w", err)
	}
	requestHash := sha256.Sum256(requestPayload)
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ItemDetails{}, fmt.Errorf("begin idempotent folder creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM drive.user_account
		WHERE public_id = $1::uuid AND status = 'active'
	`, userPublicID).Scan(&actorID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ItemDetails{}, ErrItemNotFound
		}
		return ItemDetails{}, fmt.Errorf("load folder creation actor: %w", err)
	}
	response, replayed, err := claimMutationRequestResult(ctx, tx, actorID, idempotencyKey, "folder.create", requestHash[:])
	if err != nil {
		return ItemDetails{}, err
	}
	if replayed {
		var item ItemDetails
		if err := json.Unmarshal(response, &item); err != nil {
			return ItemDetails{}, fmt.Errorf("decode folder creation result: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ItemDetails{}, fmt.Errorf("commit folder creation replay: %w", err)
		}
		return item, nil
	}
	var driveID, parentID int64
	if err := tx.QueryRow(ctx, `
		SELECT drive.id, parent.id
		FROM drive.drive_space drive
		JOIN drive.item parent ON parent.drive_id = drive.id
		JOIN drive.drive_member member ON member.drive_id = drive.id AND member.user_id = $3
		WHERE drive.public_id = $1::uuid AND drive.status = 'active'
			AND parent.public_id = $2::uuid AND parent.kind = 'folder' AND parent.trashed_at IS NULL
			AND member.role IN ('owner', 'manager', 'editor')
	`, drivePublicID, parentPublicID, actorID).Scan(&driveID, &parentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ItemDetails{}, ErrItemNotFound
		}
		return ItemDetails{}, fmt.Errorf("authorize folder creation: %w", err)
	}
	item, err := ensureFolderInParentTx(ctx, tx, drivePublicID, driveID, parentID, actorID, name)
	if err != nil {
		return ItemDetails{}, err
	}
	response, err = json.Marshal(item)
	if err != nil {
		return ItemDetails{}, fmt.Errorf("encode folder creation result: %w", err)
	}
	if err := completeMutationRequestWithResponse(ctx, tx, actorID, idempotencyKey, response); err != nil {
		return ItemDetails{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ItemDetails{}, fmt.Errorf("commit idempotent folder creation: %w", err)
	}
	return item, nil
}

func (m *Metadata) ensureFolderInParent(ctx context.Context, drivePublicID, userPublicID string, driveID, parentID int64, name string) (ItemDetails, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ItemDetails{}, fmt.Errorf("begin folder creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actorID int64
	if err := tx.QueryRow(ctx, `
		SELECT actor.id
		FROM drive.user_account actor
		JOIN drive.drive_member member ON member.user_id = actor.id AND member.drive_id = $2
		WHERE actor.public_id = $1::uuid AND actor.status = 'active'
			AND member.role IN ('owner', 'manager', 'editor')
	`, userPublicID, driveID).Scan(&actorID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ItemDetails{}, ErrItemNotFound
		}
		return ItemDetails{}, fmt.Errorf("authorize folder creation: %w", err)
	}
	item, err := ensureFolderInParentTx(ctx, tx, drivePublicID, driveID, parentID, actorID, name)
	if err != nil {
		return ItemDetails{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ItemDetails{}, fmt.Errorf("commit folder creation: %w", err)
	}
	return item, nil
}

func ensureFolderInParentTx(ctx context.Context, tx pgx.Tx, drivePublicID string, driveID, parentID, actorID int64, name string) (ItemDetails, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("%d:%d", driveID, parentID)); err != nil {
		return ItemDetails{}, fmt.Errorf("lock folder namespace: %w", err)
	}
	var item ItemDetails
	err := tx.QueryRow(ctx, `
		SELECT public_id::text, kind, name FROM drive.item
		WHERE drive_id = $1 AND parent_id = $2 AND lower(name) = lower($3) AND trashed_at IS NULL
		FOR UPDATE
	`, driveID, parentID, name).Scan(&item.ID, &item.Kind, &item.Name)
	if err == nil {
		if item.Kind != "folder" {
			return ItemDetails{}, ErrNameConflict
		}
		item.DriveID = drivePublicID
		return item, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ItemDetails{}, fmt.Errorf("find existing folder: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
		VALUES ($1, $2, 'folder', $3, $4)
		RETURNING public_id::text, name
	`, driveID, parentID, name, actorID).Scan(&item.ID, &item.Name); err != nil {
		return ItemDetails{}, fmt.Errorf("create folder metadata: %w", err)
	}
	item.DriveID, item.Kind = drivePublicID, "folder"
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type)
		SELECT $1, id, $3, 'folder.created' FROM drive.item WHERE public_id = $2::uuid
	`, driveID, item.ID, actorID); err != nil {
		return ItemDetails{}, fmt.Errorf("record folder creation: %w", err)
	}
	return item, nil
}

type UpdateItemInput struct {
	DrivePublicID  string
	UserPublicID   string
	ItemPublicID   string
	Name           *string
	ParentPublicID *string
	Description    *string
	FolderColor    *string
	IdempotencyKey string
}

func (m *Metadata) GetItem(ctx context.Context, drivePublicID, itemPublicID string) (ItemDetails, error) {
	var item ItemDetails
	err := m.pool.QueryRow(ctx, `
		SELECT item.public_id::text, drive.public_id::text,
			COALESCE(parent.public_id::text, ''), item.kind, item.name,
			COALESCE(item.description, ''), COALESCE(item.folder_color, ''), COALESCE(version.mime_type, ''),
			COALESCE(version.size_bytes, 0), COALESCE(version.public_id::text, ''),
			item.trashed_at IS NOT NULL
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		LEFT JOIN drive.item parent ON parent.id = item.parent_id
		LEFT JOIN drive.file_version version
			ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE drive.public_id = $1::uuid AND item.public_id = $2::uuid
	`, drivePublicID, itemPublicID).Scan(
		&item.ID, &item.DriveID, &item.ParentID, &item.Kind, &item.Name,
		&item.Description, &item.FolderColor, &item.MIMEType, &item.SizeBytes, &item.VersionID, &item.Trashed,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemDetails{}, ErrItemNotFound
	}
	if err != nil {
		return ItemDetails{}, fmt.Errorf("get item: %w", err)
	}
	return item, nil
}

func (m *Metadata) GetAccessibleItem(ctx context.Context, userPublicID, itemPublicID string) (ItemDetails, error) {
	access, err := loadCommentAccess(ctx, m.pool, userPublicID, itemPublicID)
	if err != nil {
		if errors.Is(err, ErrPermissionDenied) {
			return ItemDetails{}, ErrItemNotFound
		}
		return ItemDetails{}, err
	}
	var item ItemDetails
	err = m.pool.QueryRow(ctx, `
		SELECT item.public_id::text, drive.public_id::text,
			COALESCE(parent.public_id::text, ''), item.kind, item.name,
			COALESCE(item.description, ''), COALESCE(item.folder_color, ''), COALESCE(version.mime_type, ''),
			COALESCE(version.size_bytes, 0), COALESCE(version.public_id::text, ''),
			item.trashed_at IS NOT NULL
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		LEFT JOIN drive.item parent ON parent.id = item.parent_id
		LEFT JOIN drive.file_version version
			ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE item.id = $1
	`, access.ItemID).Scan(
		&item.ID, &item.DriveID, &item.ParentID, &item.Kind, &item.Name,
		&item.Description, &item.FolderColor, &item.MIMEType, &item.SizeBytes, &item.VersionID, &item.Trashed,
	)
	if err != nil {
		return ItemDetails{}, fmt.Errorf("get accessible item: %w", err)
	}
	return item, nil
}

func (m *Metadata) UpdateItem(ctx context.Context, input UpdateItemInput) (ItemDetails, error) {
	if input.Name == nil && input.ParentPublicID == nil && input.Description == nil && input.FolderColor == nil {
		return ItemDetails{}, fmt.Errorf("%w: at least one item field is required", ErrInvalidItemUpdate)
	}
	if input.Name != nil {
		if err := validation.ItemName(*input.Name); err != nil {
			return ItemDetails{}, err
		}
	}
	if input.FolderColor != nil && !validFolderColor(*input.FolderColor) {
		return ItemDetails{}, fmt.Errorf("%w: unsupported folder color", ErrInvalidItemUpdate)
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 {
		return ItemDetails{}, fmt.Errorf("%w: idempotency_key must contain 1 to 128 characters", ErrInvalidItemUpdate)
	}
	requestHash, err := itemUpdateRequestHash(input)
	if err != nil {
		return ItemDetails{}, fmt.Errorf("hash item update request: %w", err)
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ItemDetails{}, fmt.Errorf("begin item update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access, err := loadCommentAccess(ctx, tx, input.UserPublicID, input.ItemPublicID)
	if err != nil {
		return ItemDetails{}, err
	}
	if access.Rank < 3 {
		return ItemDetails{}, ErrPermissionDenied
	}
	driveID, actorID, itemID := access.DriveID, access.UserID, access.ItemID
	claimed, err := tx.Exec(ctx, `
		INSERT INTO drive.mutation_request (user_id, idempotency_key, operation, request_hash)
		VALUES ($1, $2, 'item.update', $3)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
	`, actorID, input.IdempotencyKey, requestHash[:])
	if err != nil {
		return ItemDetails{}, fmt.Errorf("claim item update idempotency key: %w", err)
	}
	if claimed.RowsAffected() == 0 {
		var operation string
		var existingHash, response []byte
		if err := tx.QueryRow(ctx, `
			SELECT operation, request_hash, response FROM drive.mutation_request
			WHERE user_id = $1 AND idempotency_key = $2
			FOR UPDATE
		`, actorID, input.IdempotencyKey).Scan(&operation, &existingHash, &response); err != nil {
			return ItemDetails{}, fmt.Errorf("load item update idempotency result: %w", err)
		}
		if operation != "item.update" || !bytes.Equal(existingHash, requestHash[:]) {
			return ItemDetails{}, ErrIdempotencyConflict
		}
		if len(response) == 0 {
			return ItemDetails{}, fmt.Errorf("item update idempotency result is incomplete")
		}
		var cached ItemDetails
		if err := json.Unmarshal(response, &cached); err != nil {
			return ItemDetails{}, fmt.Errorf("decode item update idempotency result: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ItemDetails{}, fmt.Errorf("commit cached item update: %w", err)
		}
		return cached, nil
	}
	var currentParentID int64
	var kind string
	if err := tx.QueryRow(ctx, `
		SELECT item.parent_id, item.kind FROM drive.item item
		WHERE item.id = $1 AND item.parent_id IS NOT NULL AND item.trashed_at IS NULL
		FOR UPDATE
	`, itemID).Scan(&currentParentID, &kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ItemDetails{}, ErrItemNotFound
		}
		return ItemDetails{}, fmt.Errorf("lock item: %w", err)
	}
	if input.FolderColor != nil && kind != "folder" {
		return ItemDetails{}, fmt.Errorf("%w: folder_color is only valid for folders", ErrInvalidItemUpdate)
	}
	destinationParentID := currentParentID
	if input.ParentPublicID != nil {
		var destinationPublicID string
		if *input.ParentPublicID == "" {
			if err := tx.QueryRow(ctx, `
				SELECT public_id::text FROM drive.item WHERE drive_id = $1 AND parent_id IS NULL
			`, driveID).Scan(&destinationPublicID); err != nil {
				return ItemDetails{}, fmt.Errorf("resolve drive root: %w", err)
			}
		} else {
			destinationPublicID = *input.ParentPublicID
		}
		destinationAccess, err := loadCommentAccess(ctx, tx, input.UserPublicID, destinationPublicID)
		if err != nil || destinationAccess.Rank < 3 || destinationAccess.DriveID != driveID {
			return ItemDetails{}, ErrInvalidMove
		}
		if err := tx.QueryRow(ctx, `
			SELECT id FROM drive.item WHERE id = $1 AND kind = 'folder' AND trashed_at IS NULL
		`, destinationAccess.ItemID).Scan(&destinationParentID); err != nil {
			return ItemDetails{}, ErrInvalidMove
		}
		if destinationParentID == itemID {
			return ItemDetails{}, ErrInvalidMove
		}
		if kind == "folder" {
			var createsCycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE ancestors AS (
					SELECT id, parent_id FROM drive.item WHERE id = $1
					UNION ALL
					SELECT parent.id, parent.parent_id
					FROM drive.item parent JOIN ancestors child ON child.parent_id = parent.id
				)
				SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $2)
			`, destinationParentID, itemID).Scan(&createsCycle); err != nil {
				return ItemDetails{}, fmt.Errorf("validate item move: %w", err)
			}
			if createsCycle {
				return ItemDetails{}, ErrInvalidMove
			}
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("%d:%d", driveID, destinationParentID)); err != nil {
		return ItemDetails{}, fmt.Errorf("lock item namespace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.item
		SET name = COALESCE($2::text, name), parent_id = $3,
			description = CASE WHEN $4::boolean THEN NULLIF($5, '') ELSE description END,
			folder_color = CASE WHEN $6::boolean THEN NULLIF($7, '') ELSE folder_color END
		WHERE id = $1
	`, itemID, input.Name, destinationParentID, input.Description != nil, stringValue(input.Description), input.FolderColor != nil, stringValue(input.FolderColor)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ItemDetails{}, ErrNameConflict
		}
		return ItemDetails{}, fmt.Errorf("update item: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		VALUES ($1, $2, $3, 'item.updated', jsonb_build_object(
			'name_changed', $4::boolean, 'parent_changed', $5::boolean, 'description_changed', $6::boolean,
			'folder_color_changed', $7::boolean
		))
	`, driveID, itemID, actorID, input.Name != nil, input.ParentPublicID != nil, input.Description != nil, input.FolderColor != nil); err != nil {
		return ItemDetails{}, fmt.Errorf("record item update: %w", err)
	}
	var updated ItemDetails
	if err := tx.QueryRow(ctx, `
		SELECT item.public_id::text, drive.public_id::text,
			COALESCE(parent.public_id::text, ''), item.kind, item.name,
			COALESCE(item.description, ''), COALESCE(item.folder_color, ''), COALESCE(version.mime_type, ''),
			COALESCE(version.size_bytes, 0), COALESCE(version.public_id::text, ''),
			item.trashed_at IS NOT NULL
		FROM drive.item item
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		LEFT JOIN drive.item parent ON parent.id = item.parent_id
		LEFT JOIN drive.file_version version ON version.item_id = item.id AND version.is_current AND version.state = 'ready'
		WHERE item.id = $1
	`, itemID).Scan(
		&updated.ID, &updated.DriveID, &updated.ParentID, &updated.Kind, &updated.Name,
		&updated.Description, &updated.FolderColor, &updated.MIMEType, &updated.SizeBytes, &updated.VersionID, &updated.Trashed,
	); err != nil {
		return ItemDetails{}, fmt.Errorf("load updated item: %w", err)
	}
	response, err := json.Marshal(updated)
	if err != nil {
		return ItemDetails{}, fmt.Errorf("encode item update result: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.mutation_request SET response = $3::jsonb, completed_at = now()
		WHERE user_id = $1 AND idempotency_key = $2
	`, actorID, input.IdempotencyKey, response); err != nil {
		return ItemDetails{}, fmt.Errorf("complete item update idempotency request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ItemDetails{}, fmt.Errorf("commit item update: %w", err)
	}
	return updated, nil
}

func itemUpdateRequestHash(input UpdateItemInput) ([32]byte, error) {
	payload, err := json.Marshal(struct {
		ItemID      string  `json:"item_id"`
		Name        *string `json:"name"`
		ParentID    *string `json:"parent_id"`
		Description *string `json:"description"`
		FolderColor *string `json:"folder_color"`
	}{input.ItemPublicID, input.Name, input.ParentPublicID, input.Description, input.FolderColor})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func validFolderColor(value string) bool {
	switch value {
	case "", "red", "orange", "yellow", "green", "blue", "purple", "gray":
		return true
	default:
		return false
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

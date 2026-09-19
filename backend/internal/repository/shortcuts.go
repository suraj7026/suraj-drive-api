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

type CreateShortcutInput struct {
	UserPublicID   string
	TargetPublicID string
	ParentPublicID string
	Name           string
	IdempotencyKey string
}

type ShortcutResolution struct {
	ShortcutID string       `json:"shortcut_id"`
	Broken     bool         `json:"broken"`
	Target     *ItemDetails `json:"target,omitempty"`
}

func (m *Metadata) CreateShortcut(ctx context.Context, input CreateShortcutInput) (ItemDetails, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := validation.ItemName(input.Name); err != nil {
		return ItemDetails{}, err
	}
	if input.ParentPublicID == "" || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 {
		return ItemDetails{}, fmt.Errorf("%w: parent_id and a 1 to 128 character idempotency_key are required", ErrInvalidItemUpdate)
	}
	payload, _ := json.Marshal(struct {
		TargetID string `json:"target_id"`
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}{input.TargetPublicID, input.ParentPublicID, input.Name})
	requestHash := sha256.Sum256(payload)

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ItemDetails{}, fmt.Errorf("begin shortcut creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	targetAccess, err := loadCommentAccess(ctx, tx, input.UserPublicID, input.TargetPublicID)
	if err != nil {
		return ItemDetails{}, err
	}
	parentAccess, err := loadCommentAccess(ctx, tx, input.UserPublicID, input.ParentPublicID)
	if err != nil || parentAccess.Rank < 3 {
		return ItemDetails{}, ErrPermissionDenied
	}
	var parentKind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM drive.item WHERE id = $1 AND trashed_at IS NULL`, parentAccess.ItemID).Scan(&parentKind); err != nil || parentKind != "folder" {
		return ItemDetails{}, ErrInvalidMove
	}

	claimed, err := tx.Exec(ctx, `
		INSERT INTO drive.mutation_request (user_id, idempotency_key, operation, request_hash)
		VALUES ($1, $2, 'shortcut.create', $3)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
	`, targetAccess.UserID, input.IdempotencyKey, requestHash[:])
	if err != nil {
		return ItemDetails{}, fmt.Errorf("claim shortcut idempotency key: %w", err)
	}
	if claimed.RowsAffected() == 0 {
		var operation string
		var existingHash, response []byte
		if err := tx.QueryRow(ctx, `
			SELECT operation, request_hash, response FROM drive.mutation_request
			WHERE user_id = $1 AND idempotency_key = $2 FOR UPDATE
		`, targetAccess.UserID, input.IdempotencyKey).Scan(&operation, &existingHash, &response); err != nil {
			return ItemDetails{}, fmt.Errorf("load shortcut idempotency result: %w", err)
		}
		if operation != "shortcut.create" || !bytes.Equal(existingHash, requestHash[:]) {
			return ItemDetails{}, ErrIdempotencyConflict
		}
		var cached ItemDetails
		if len(response) == 0 || json.Unmarshal(response, &cached) != nil {
			return ItemDetails{}, fmt.Errorf("shortcut idempotency result is incomplete")
		}
		if err := tx.Commit(ctx); err != nil {
			return ItemDetails{}, err
		}
		return cached, nil
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fmt.Sprintf("%d:%d", parentAccess.DriveID, parentAccess.ItemID)); err != nil {
		return ItemDetails{}, fmt.Errorf("lock shortcut namespace: %w", err)
	}
	var shortcut ItemDetails
	err = tx.QueryRow(ctx, `
		INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id, shortcut_target_item_id)
		VALUES ($1, $2, 'shortcut', $3, $4, $5)
		RETURNING public_id::text, name
	`, parentAccess.DriveID, parentAccess.ItemID, input.Name, targetAccess.UserID, targetAccess.ItemID).Scan(&shortcut.ID, &shortcut.Name)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ItemDetails{}, ErrNameConflict
		}
		return ItemDetails{}, fmt.Errorf("create shortcut: %w", err)
	}
	shortcut.Kind = "shortcut"
	if err := tx.QueryRow(ctx, `SELECT public_id::text FROM drive.drive_space WHERE id = $1`, parentAccess.DriveID).Scan(&shortcut.DriveID); err != nil {
		return ItemDetails{}, fmt.Errorf("load shortcut drive: %w", err)
	}
	shortcut.ParentID = input.ParentPublicID
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, actor_user_id, event_type, details)
		SELECT drive_id, id, $2, 'shortcut.created', jsonb_build_object('target_item_id', $3::text)
		FROM drive.item WHERE public_id = $1::uuid
	`, shortcut.ID, targetAccess.UserID, input.TargetPublicID); err != nil {
		return ItemDetails{}, fmt.Errorf("record shortcut creation: %w", err)
	}
	response, _ := json.Marshal(shortcut)
	if _, err := tx.Exec(ctx, `
		UPDATE drive.mutation_request SET response = $3::jsonb, completed_at = now()
		WHERE user_id = $1 AND idempotency_key = $2
	`, targetAccess.UserID, input.IdempotencyKey, response); err != nil {
		return ItemDetails{}, fmt.Errorf("complete shortcut idempotency request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ItemDetails{}, fmt.Errorf("commit shortcut creation: %w", err)
	}
	return shortcut, nil
}

func (m *Metadata) ResolveShortcut(ctx context.Context, userPublicID, shortcutPublicID string) (ShortcutResolution, error) {
	shortcutAccess, err := loadCommentAccess(ctx, m.pool, userPublicID, shortcutPublicID)
	if err != nil {
		return ShortcutResolution{}, err
	}
	var targetPublicID string
	if err := m.pool.QueryRow(ctx, `
		SELECT target.public_id::text FROM drive.item shortcut
		JOIN drive.item target ON target.id = shortcut.shortcut_target_item_id
		WHERE shortcut.id = $1 AND shortcut.kind = 'shortcut' AND shortcut.trashed_at IS NULL
	`, shortcutAccess.ItemID).Scan(&targetPublicID); errors.Is(err, pgx.ErrNoRows) {
		return ShortcutResolution{}, ErrItemNotFound
	} else if err != nil {
		return ShortcutResolution{}, fmt.Errorf("load shortcut target: %w", err)
	}
	target, err := m.GetAccessibleItem(ctx, userPublicID, targetPublicID)
	if errors.Is(err, ErrItemNotFound) || errors.Is(err, ErrPermissionDenied) || target.Trashed {
		return ShortcutResolution{ShortcutID: shortcutPublicID, Broken: true}, nil
	}
	if err != nil {
		return ShortcutResolution{}, err
	}
	return ShortcutResolution{ShortcutID: shortcutPublicID, Target: &target}, nil
}

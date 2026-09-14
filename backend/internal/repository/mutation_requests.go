package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func claimMutationRequest(ctx context.Context, tx pgx.Tx, userID int64, key, operation string, requestHash []byte) (bool, error) {
	_, replayed, err := claimMutationRequestResult(ctx, tx, userID, key, operation, requestHash)
	return replayed, err
}

func claimMutationRequestResult(ctx context.Context, tx pgx.Tx, userID int64, key, operation string, requestHash []byte) ([]byte, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return nil, false, fmt.Errorf("%w: idempotency_key must contain 1 to 128 characters", ErrInvalidItemUpdate)
	}
	claimed, err := tx.Exec(ctx, `
		INSERT INTO drive.mutation_request (user_id, idempotency_key, operation, request_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
	`, userID, key, operation, requestHash)
	if err != nil {
		return nil, false, fmt.Errorf("claim %s idempotency key: %w", operation, err)
	}
	if claimed.RowsAffected() == 1 {
		return nil, false, nil
	}
	var existingOperation string
	var existingHash, response []byte
	if err := tx.QueryRow(ctx, `
		SELECT operation, request_hash, response FROM drive.mutation_request
		WHERE user_id = $1 AND idempotency_key = $2 FOR UPDATE
	`, userID, key).Scan(&existingOperation, &existingHash, &response); err != nil {
		return nil, false, fmt.Errorf("load %s idempotency result: %w", operation, err)
	}
	if existingOperation != operation || !bytes.Equal(existingHash, requestHash) {
		return nil, false, ErrIdempotencyConflict
	}
	if len(response) == 0 {
		return nil, false, fmt.Errorf("%s idempotency result is incomplete", operation)
	}
	return response, true, nil
}

func completeMutationRequest(ctx context.Context, tx pgx.Tx, userID int64, key string) error {
	return completeMutationRequestWithResponse(ctx, tx, userID, key, []byte(`{"ok":true}`))
}

func completeMutationRequestWithResponse(ctx context.Context, tx pgx.Tx, userID int64, key string, response []byte) error {
	command, err := tx.Exec(ctx, `
		UPDATE drive.mutation_request SET response = $3::jsonb, completed_at = now()
		WHERE user_id = $1 AND idempotency_key = $2 AND response IS NULL
	`, userID, strings.TrimSpace(key), response)
	if err != nil {
		return fmt.Errorf("complete mutation idempotency request: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("mutation idempotency request was not completed")
	}
	return nil
}

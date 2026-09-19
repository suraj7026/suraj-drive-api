package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type NotificationJob struct {
	ID          string
	Recipient   string
	Type        string
	Payload     json.RawMessage
	Attempt     int
	MaxAttempts int
}

func (m *Metadata) ClaimNotificationJob(ctx context.Context, lease time.Duration) (*NotificationJob, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin notification claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE drive.notification_outbox
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			lease_expires_at = NULL, run_after = now(), updated_at = now(),
			last_error = COALESCE(last_error, 'worker lease expired')
		WHERE status = 'processing' AND lease_expires_at <= now()
	`); err != nil {
		return nil, fmt.Errorf("recover notification leases: %w", err)
	}
	var internalID int64
	var job NotificationJob
	err = tx.QueryRow(ctx, `
		SELECT outbox.id, outbox.public_id::text,
			COALESCE(NULLIF(outbox.recipient_email, ''), recipient.primary_email),
			outbox.notification_type, outbox.payload, outbox.attempts + 1, outbox.max_attempts
		FROM drive.notification_outbox outbox
		LEFT JOIN drive.user_account recipient ON recipient.id = outbox.recipient_user_id
		WHERE outbox.status = 'queued' AND outbox.run_after <= now()
			AND outbox.attempts < outbox.max_attempts
		ORDER BY outbox.run_after, outbox.id
		FOR UPDATE OF outbox SKIP LOCKED
		LIMIT 1
	`).Scan(&internalID, &job.ID, &job.Recipient, &job.Type, &job.Payload, &job.Attempt, &job.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("select notification job: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.notification_outbox
		SET status = 'processing', attempts = attempts + 1,
			lease_expires_at = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = $1
	`, internalID, max(1, int(lease.Seconds()))); err != nil {
		return nil, fmt.Errorf("lease notification job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit notification claim: %w", err)
	}
	return &job, nil
}

func (m *Metadata) CompleteNotificationJob(ctx context.Context, jobPublicID string, attempt int) error {
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.notification_outbox
		SET status = 'delivered', delivered_at = now(), lease_expires_at = NULL,
			last_error = NULL, updated_at = now()
		WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $2 AND lease_expires_at > clock_timestamp()
	`, jobPublicID, attempt)
	if err != nil {
		return fmt.Errorf("complete notification job: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrJobLeaseLost
	}
	return nil
}

func (m *Metadata) FailNotificationJob(ctx context.Context, jobPublicID string, attempt int, failure error) error {
	message := "notification delivery failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.notification_outbox
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			run_after = CASE WHEN attempts >= max_attempts THEN run_after
				ELSE now() + LEAST(attempts * attempts * interval '1 minute', interval '1 hour') END,
			lease_expires_at = NULL, last_error = $2, updated_at = now()
		WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $3 AND lease_expires_at > clock_timestamp()
	`, jobPublicID, message, attempt)
	if err != nil {
		return fmt.Errorf("fail notification job: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrJobLeaseLost
	}
	return nil
}

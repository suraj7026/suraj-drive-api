package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type UploadCompletionJob struct {
	ID             string
	Attempt        int
	Bucket         string
	StagingKey     string
	FinalKey       string
	MinIOUploadID  string
	UploadMode     string
	PartSize       int64
	ExpectedSize   int64
	ExpectedSHA256 []byte
	MIMEType       string
	DrivePublicID  string
	UserPublicID   string
	UploadID       string
}

type UploadStagingCleanup struct {
	JobID, Bucket, Key string
}

func (m *Metadata) NextUploadStagingCleanup(ctx context.Context) (*UploadStagingCleanup, error) {
	var cleanup UploadStagingCleanup
	err := m.pool.QueryRow(ctx, `
		SELECT job.public_id::text, drive.storage_bucket, session.reserved_storage_key
		FROM drive.upload_completion_job job
		JOIN drive.upload_session session ON session.id = job.upload_session_id
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		WHERE job.status = 'succeeded' AND job.staging_cleaned_at IS NULL
			AND job.staging_cleanup_after <= now()
		ORDER BY job.staging_cleanup_after, job.id LIMIT 1
	`).Scan(&cleanup.JobID, &cleanup.Bucket, &cleanup.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load upload staging cleanup: %w", err)
	}
	return &cleanup, nil
}

func (m *Metadata) CompleteUploadStagingCleanup(ctx context.Context, jobPublicID string) error {
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.upload_completion_job SET staging_cleaned_at = now(), updated_at = now()
		WHERE public_id = $1::uuid AND status = 'succeeded' AND staging_cleanup_after <= now()
			AND staging_cleaned_at IS NULL
	`, jobPublicID)
	if err != nil {
		return fmt.Errorf("complete upload staging cleanup: %w", err)
	}
	if command.RowsAffected() > 1 {
		return fmt.Errorf("upload staging cleanup updated multiple jobs")
	}
	return nil
}

func (m *Metadata) QueueUploadCompletion(ctx context.Context, drivePublicID, userPublicID, uploadID string, expectedSHA256 []byte) (UploadReservation, error) {
	if len(expectedSHA256) != 32 {
		return UploadReservation{}, ErrUploadIntegrity
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadReservation{}, fmt.Errorf("begin upload completion request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var reservation UploadReservation
	var sessionID int64
	var storedSHA []byte
	var versionState, scanStatus string
	err = tx.QueryRow(ctx, `
		SELECT session.id, session.public_id::text, session.reserved_storage_key, item.name,
			session.expected_size_bytes, session.mime_type, session.status, session.expires_at,
			session.upload_mode, COALESCE(session.part_size_bytes, 0),
			COALESCE(session.expected_sha256, ''::bytea), version.state,
			COALESCE(scan.status, '')
		FROM drive.upload_session session
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account owner ON owner.id = session.user_id
		JOIN drive.item item ON item.id = session.item_id
		JOIN drive.file_version version ON version.id = session.file_version_id
		LEFT JOIN drive.malware_scan_job scan ON scan.file_version_id = version.id
		WHERE session.public_id = $1::uuid AND drive.public_id = $2::uuid
			AND owner.public_id = $3::uuid
		FOR UPDATE OF session
	`, uploadID, drivePublicID, userPublicID).Scan(
		&sessionID, &reservation.ID, &reservation.StorageKey, &reservation.Name,
		&reservation.ExpectedSize, &reservation.MIMEType, &reservation.Status,
		&reservation.ExpiresAt, &reservation.UploadMode, &reservation.PartSize, &storedSHA,
		&versionState, &scanStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UploadReservation{}, ErrUploadNotFound
	}
	if err != nil {
		return UploadReservation{}, fmt.Errorf("lock upload completion request: %w", err)
	}
	if len(storedSHA) != 0 && !bytes.Equal(storedSHA, expectedSHA256) {
		return UploadReservation{}, ErrUploadIntegrity
	}
	if reservation.Status == "completed" || reservation.Status == "completing" {
		reservation.ExpectedSHA256 = fmt.Sprintf("%x", storedSHA)
		reservation.ProcessingStage = uploadProcessingStage(reservation.Status, versionState, scanStatus)
		if err := tx.Commit(ctx); err != nil {
			return UploadReservation{}, fmt.Errorf("commit upload completion lookup: %w", err)
		}
		return reservation, nil
	}
	if reservation.Status != "initiated" && reservation.Status != "uploading" {
		return UploadReservation{}, ErrUploadState
	}
	if !reservation.ExpiresAt.After(time.Now()) {
		return UploadReservation{}, ErrUploadState
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.upload_session
		SET status = 'completing', expected_sha256 = $2, updated_at = now()
		WHERE id = $1
	`, sessionID, expectedSHA256); err != nil {
		return UploadReservation{}, fmt.Errorf("mark upload completing: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.upload_completion_job (upload_session_id)
		VALUES ($1)
		ON CONFLICT (upload_session_id) DO UPDATE
		SET status = CASE WHEN drive.upload_completion_job.status = 'succeeded' THEN drive.upload_completion_job.status ELSE 'queued' END,
			run_after = now(), lease_expires_at = NULL, last_error = NULL, updated_at = now()
	`, sessionID); err != nil {
		return UploadReservation{}, fmt.Errorf("queue upload completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return UploadReservation{}, fmt.Errorf("commit upload completion request: %w", err)
	}
	reservation.Status = "completing"
	reservation.ProcessingStage = "verifying"
	reservation.ExpectedSHA256 = fmt.Sprintf("%x", expectedSHA256)
	return reservation, nil
}

func uploadProcessingStage(sessionStatus, versionState, scanStatus string) string {
	if sessionStatus == "completing" {
		return "verifying"
	}
	if versionState == "ready" {
		return "ready"
	}
	if versionState == "quarantined" && (scanStatus == "queued" || scanStatus == "processing") {
		return "scanning"
	}
	if versionState == "failed" || versionState == "deleted" || scanStatus == "infected" || scanStatus == "failed" {
		return "failed"
	}
	return "verifying"
}

func (m *Metadata) ClaimUploadCompletionJob(ctx context.Context, lease time.Duration) (*UploadCompletionJob, error) {
	if _, err := m.pool.Exec(ctx, `
		WITH expired AS (
			UPDATE drive.upload_completion_job
			SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
				lease_expires_at = NULL, run_after = now(),
				last_error = COALESCE(last_error, 'worker lease expired'), updated_at = now()
			WHERE status = 'processing' AND lease_expires_at <= now()
			RETURNING upload_session_id, status
		), terminal AS (
			UPDATE drive.upload_session session
			SET status = 'failed', failure_code = 'completion_failed',
				failure_message = 'upload verification exhausted all retries', updated_at = now()
			FROM expired WHERE expired.upload_session_id = session.id AND expired.status = 'failed'
			RETURNING session.file_version_id
		), failed_versions AS (
			UPDATE drive.file_version version SET state = 'failed', is_current = false
			FROM terminal WHERE terminal.file_version_id = version.id AND version.state = 'pending'
			RETURNING version.id
		)
		INSERT INTO drive.blob_deletion_job (file_version_id)
		SELECT id FROM failed_versions ON CONFLICT (file_version_id) DO NOTHING
	`); err != nil {
		return nil, fmt.Errorf("recover upload completion leases: %w", err)
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var job UploadCompletionJob
	var internalID int64
	err = tx.QueryRow(ctx, `
		SELECT job.id, job.public_id::text, job.attempts + 1,
			drive.storage_bucket, session.reserved_storage_key,
			COALESCE(session.minio_upload_id, ''), session.upload_mode,
			COALESCE(session.part_size_bytes, 0), session.expected_size_bytes,
			session.expected_sha256, session.mime_type,
			drive.public_id::text, owner.public_id::text, session.public_id::text,
			'.objects/' || item.public_id::text || '/' || version.public_id::text ||
				'/attempt-' || (job.attempts + 1)::text || '/content'
		FROM drive.upload_completion_job job
		JOIN drive.upload_session session ON session.id = job.upload_session_id
		JOIN drive.file_version version ON version.id = session.file_version_id
		JOIN drive.item item ON item.id = session.item_id
		JOIN drive.drive_space drive ON drive.id = session.drive_id
		JOIN drive.user_account owner ON owner.id = session.user_id
		WHERE job.status = 'queued' AND job.run_after <= now() AND job.attempts < job.max_attempts
			AND session.status = 'completing' AND version.state = 'pending'
		ORDER BY job.run_after, job.id
		FOR UPDATE OF job, session, version SKIP LOCKED
		LIMIT 1
	`).Scan(&internalID, &job.ID, &job.Attempt, &job.Bucket, &job.StagingKey,
		&job.MinIOUploadID, &job.UploadMode, &job.PartSize, &job.ExpectedSize,
		&job.ExpectedSHA256, &job.MIMEType, &job.DrivePublicID, &job.UserPublicID,
		&job.UploadID, &job.FinalKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim upload completion: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.upload_completion_job
		SET status = 'processing', attempts = attempts + 1, final_storage_key = $2,
			lease_expires_at = now() + make_interval(secs => $3), updated_at = now()
		WHERE id = $1
	`, internalID, job.FinalKey, max(1, int(lease.Seconds()))); err != nil {
		return nil, fmt.Errorf("lease upload completion: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit upload completion claim: %w", err)
	}
	return &job, nil
}

func (m *Metadata) FailUploadCompletionJob(ctx context.Context, jobPublicID string, attempt int, failure error) error {
	message := "upload completion failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID, versionID int64
	var terminal bool
	err = tx.QueryRow(ctx, `
		UPDATE drive.upload_completion_job
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			run_after = CASE WHEN attempts >= max_attempts THEN run_after
				ELSE now() + LEAST(attempts * attempts * interval '10 seconds', interval '5 minutes') END,
			lease_expires_at = NULL, last_error = $3, updated_at = now()
		WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $2
			AND lease_expires_at > clock_timestamp()
		RETURNING upload_session_id, status = 'failed'
	`, jobPublicID, attempt, message).Scan(&sessionID, &terminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrJobLeaseLost
	}
	if err != nil {
		return fmt.Errorf("fail upload completion: %w", err)
	}
	if terminal {
		if err := tx.QueryRow(ctx, `
			UPDATE drive.upload_session SET status = 'failed', failure_code = 'completion_failed',
				failure_message = $2, updated_at = now() WHERE id = $1
			RETURNING file_version_id
		`, sessionID, message).Scan(&versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET state = 'failed', is_current = false WHERE id = $1 AND state = 'pending'`, versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO drive.blob_deletion_job (file_version_id) VALUES ($1) ON CONFLICT (file_version_id) DO NOTHING`, versionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

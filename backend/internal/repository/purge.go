package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type BlobDeletionJob struct {
	ID            string
	VersionID     string
	Bucket        string
	StorageKey    string
	StagingKey    string
	MinIOUploadID string
	Attempt       int
	MaxAttempts   int
}

type ArtifactLocation struct {
	Bucket string
	Key    string
}

func (m *Metadata) QueueExpiredTrash(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	commandTag, err := m.pool.Exec(ctx, `
		WITH candidates AS (
			SELECT version.id
			FROM drive.file_version version
			JOIN drive.item item ON item.id = version.item_id
			WHERE item.trashed_at IS NOT NULL AND item.purge_after <= now()
				AND version.state = 'ready' AND NOT version.keep_forever
				AND NOT EXISTS (SELECT 1 FROM drive.blob_deletion_job job WHERE job.file_version_id = version.id)
			ORDER BY item.purge_after, version.id
			LIMIT $1
		)
		INSERT INTO drive.blob_deletion_job (file_version_id)
		SELECT id FROM candidates
		ON CONFLICT (file_version_id) DO NOTHING
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("queue expired trash: %w", err)
	}
	return commandTag.RowsAffected(), nil
}

func (m *Metadata) ClaimBlobDeletionJob(ctx context.Context, lease time.Duration) (*BlobDeletionJob, error) {
	if _, err := m.pool.Exec(ctx, `
		UPDATE drive.blob_deletion_job
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			lease_expires_at = NULL, run_after = now(),
			last_error = COALESCE(last_error, 'worker lease expired')
		WHERE status = 'processing' AND lease_expires_at <= now()
	`); err != nil {
		return nil, fmt.Errorf("recover deletion leases: %w", err)
	}
	rows, err := m.pool.Query(ctx, `
		SELECT job.id, item.drive_id
		FROM drive.blob_deletion_job job
		JOIN drive.file_version version ON version.id = job.file_version_id
		JOIN drive.item item ON item.id = version.item_id
		WHERE job.status = 'queued' AND job.run_after <= now() AND job.attempts < job.max_attempts
		ORDER BY job.run_after, job.id LIMIT 100
	`)
	if err != nil {
		return nil, fmt.Errorf("list deletion candidates: %w", err)
	}
	type candidate struct{ jobID, driveID int64 }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.jobID, &c.driveID); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		job, err := m.claimBlobDeletionCandidate(ctx, c.jobID, c.driveID, lease)
		if err != nil || job != nil {
			return job, err
		}
	}
	return nil, nil
}

func (m *Metadata) claimBlobDeletionCandidate(ctx context.Context, candidateID, driveID int64, lease time.Duration) (*BlobDeletionJob, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('blob-lifecycle:' || $1::bigint::text, 0))`, driveID).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, nil
	}
	var internalID int64
	var job BlobDeletionJob
	var eligible bool
	err = tx.QueryRow(ctx, `
		SELECT job.id, job.public_id::text, version.public_id::text,
			version.storage_bucket, version.storage_key, job.attempts + 1, job.max_attempts,
			COALESCE(session.minio_upload_id, ''), COALESCE(session.reserved_storage_key, ''),
			(version.state IN ('failed', 'deleting')
			 OR (version.state = 'quarantined' AND EXISTS (
				SELECT 1 FROM drive.malware_scan_job scan WHERE scan.file_version_id = version.id AND scan.status IN ('infected', 'failed')))
			 OR (version.state = 'ready' AND item.trashed_at IS NOT NULL AND item.purge_after <= now() AND NOT version.keep_forever))
		FROM drive.blob_deletion_job job
		JOIN drive.file_version version ON version.id = job.file_version_id
		JOIN drive.item item ON item.id = version.item_id
		LEFT JOIN drive.upload_session session ON session.file_version_id = version.id
		WHERE job.id = $1 AND job.status = 'queued' AND job.run_after <= now() AND job.attempts < job.max_attempts
		FOR UPDATE OF item, version, job SKIP LOCKED
	`, candidateID).Scan(&internalID, &job.ID, &job.VersionID, &job.Bucket, &job.StorageKey, &job.Attempt, &job.MaxAttempts, &job.MinIOUploadID, &job.StagingKey, &eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select deletion job: %w", err)
	}
	if !eligible {
		if _, err := tx.Exec(ctx, `DELETE FROM drive.blob_deletion_job WHERE id = $1`, internalID); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE drive.file_version SET state = 'deleting', is_current = false WHERE public_id = $1::uuid`, job.VersionID); err != nil {
		return nil, fmt.Errorf("mark deletion boundary: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.blob_deletion_job
		SET status = 'processing', attempts = attempts + 1,
			lease_expires_at = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = $1
	`, internalID, max(1, int(lease.Seconds()))); err != nil {
		return nil, fmt.Errorf("lease deletion job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit deletion claim: %w", err)
	}
	return &job, nil
}

func (m *Metadata) PreviewArtifactsForVersion(ctx context.Context, versionPublicID string) ([]ArtifactLocation, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT artifact.storage_bucket, artifact.storage_key
		FROM drive.preview_artifact artifact
		JOIN drive.file_version version ON version.id = artifact.file_version_id
		WHERE version.public_id = $1::uuid
	`, versionPublicID)
	if err != nil {
		return nil, fmt.Errorf("list preview artifacts for purge: %w", err)
	}
	defer rows.Close()
	artifacts := make([]ArtifactLocation, 0)
	for rows.Next() {
		var artifact ArtifactLocation
		if err := rows.Scan(&artifact.Bucket, &artifact.Key); err != nil {
			return nil, fmt.Errorf("scan preview artifact for purge: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate preview artifacts for purge: %w", err)
	}
	return artifacts, nil
}

func (m *Metadata) CompleteBlobDeletionJob(ctx context.Context, jobPublicID string, attempt int) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin deletion completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobID, versionID, itemID, driveID int64
	var bucket, key string
	if err := tx.QueryRow(ctx, `
		SELECT job.id, version.id, item.id, item.drive_id,
			version.storage_bucket, version.storage_key
		FROM drive.blob_deletion_job job
		JOIN drive.file_version version ON version.id = job.file_version_id
		JOIN drive.item item ON item.id = version.item_id
		WHERE job.public_id = $1::uuid AND job.status = 'processing' AND job.attempts = $2
			AND job.lease_expires_at > clock_timestamp() AND version.state = 'deleting'
		FOR UPDATE OF job, version
	`, jobPublicID, attempt).Scan(&jobID, &versionID, &itemID, &driveID, &bucket, &key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrJobLeaseLost
		}
		return fmt.Errorf("lock deletion completion: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drive.preview_artifact WHERE file_version_id = $1`, versionID); err != nil {
		return fmt.Errorf("delete preview artifact metadata: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.file_version
		SET state = 'deleted', is_current = false
		WHERE id = $1
	`, versionID); err != nil {
		return fmt.Errorf("tombstone file version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.blob_deletion_job
		SET status = 'succeeded', deleted_at = now(), lease_expires_at = NULL,
			last_error = NULL, updated_at = now()
		WHERE id = $1
	`, jobID); err != nil {
		return fmt.Errorf("complete deletion job: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.activity_event (drive_id, item_id, file_version_id, event_type, details)
		VALUES ($1, $2, $3, 'file.purged', jsonb_build_object('storage_bucket', $4::text, 'storage_key', $5::text))
	`, driveID, itemID, versionID, bucket, key); err != nil {
		return fmt.Errorf("record purge activity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit deletion completion: %w", err)
	}
	return nil
}

func (m *Metadata) FailBlobDeletionJob(ctx context.Context, jobPublicID string, attempt int, failure error) error {
	message := "blob deletion failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.blob_deletion_job
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			run_after = CASE WHEN attempts >= max_attempts THEN run_after
				ELSE now() + LEAST(attempts * attempts * interval '1 minute', interval '1 hour') END,
			lease_expires_at = NULL, last_error = $2, updated_at = now()
		WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $3 AND lease_expires_at > clock_timestamp()
	`, jobPublicID, message, attempt)
	if err != nil {
		return fmt.Errorf("fail deletion job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrJobLeaseLost
	}
	return nil
}

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const HEICPreviewProfile = "heic-jpeg-2048"

var ErrPreviewSourceNotFound = errors.New("preview source not found")

type PreviewRequest struct {
	JobID          string
	Status         string
	VersionID      string
	SourceBucket   string
	SourceKey      string
	SourceSize     int64
	SourceMIMEType string
	ArtifactKey    string
	LastError      string
}

type PreviewJob struct {
	ID             string
	VersionID      string
	Profile        string
	SourceBucket   string
	SourceKey      string
	SourceSize     int64
	SourceMIMEType string
	ArtifactKey    string
	Attempt        int
	MaxAttempts    int
}

type PreviewArtifactInput struct {
	JobID     string
	Attempt   int
	Bucket    string
	Key       string
	ETag      string
	SizeBytes int64
	MIMEType  string
}

func (m *Metadata) GetOrQueuePreview(ctx context.Context, drivePublicID, storageKey, profile string) (PreviewRequest, error) {
	if strings.TrimSpace(profile) == "" {
		return PreviewRequest{}, fmt.Errorf("preview profile is required")
	}
	var versionInternalID int64
	var request PreviewRequest
	if err := m.pool.QueryRow(ctx, `
		SELECT version.id, version.public_id::text, version.storage_bucket,
			version.storage_key, version.size_bytes, version.mime_type
		FROM drive.file_version version
		JOIN drive.item item ON item.id = version.item_id
		JOIN drive.drive_space drive ON drive.id = item.drive_id
		WHERE drive.public_id = $1::uuid AND version.storage_key = $2
			AND version.is_current AND version.state = 'ready' AND item.trashed_at IS NULL
	`, drivePublicID, storageKey).Scan(
		&versionInternalID, &request.VersionID, &request.SourceBucket,
		&request.SourceKey, &request.SourceSize, &request.SourceMIMEType,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PreviewRequest{}, ErrPreviewSourceNotFound
		}
		return PreviewRequest{}, fmt.Errorf("load preview source: %w", err)
	}
	if _, err := m.pool.Exec(ctx, `
		INSERT INTO drive.preview_job (file_version_id, profile)
		VALUES ($1, $2)
		ON CONFLICT (file_version_id, profile) DO NOTHING
	`, versionInternalID, profile); err != nil {
		return PreviewRequest{}, fmt.Errorf("queue preview: %w", err)
	}
	if err := m.pool.QueryRow(ctx, `
		SELECT job.public_id::text, job.status, COALESCE(job.last_error, ''),
			COALESCE(artifact.storage_key, '')
		FROM drive.preview_job job
		LEFT JOIN drive.preview_artifact artifact
			ON artifact.file_version_id = job.file_version_id AND artifact.profile = job.profile
		WHERE job.file_version_id = $1 AND job.profile = $2
	`, versionInternalID, profile).Scan(&request.JobID, &request.Status, &request.LastError, &request.ArtifactKey); err != nil {
		return PreviewRequest{}, fmt.Errorf("load preview state: %w", err)
	}
	return request, nil
}

func (m *Metadata) ClaimPreviewJob(ctx context.Context, lease time.Duration) (*PreviewJob, error) {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin preview job claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE drive.preview_job
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END, lease_expires_at = NULL,
			run_after = now(), last_error = COALESCE(last_error, 'worker lease expired')
		WHERE status = 'processing' AND lease_expires_at <= now()
	`); err != nil {
		return nil, fmt.Errorf("recover preview leases: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.preview_job job SET status = 'failed', lease_expires_at = NULL, last_error = 'source is unavailable'
		FROM drive.file_version version WHERE version.id = job.file_version_id AND version.state <> 'ready'
			AND job.status IN ('queued', 'processing')
	`); err != nil {
		return nil, fmt.Errorf("retire unavailable previews: %w", err)
	}

	var jobInternalID int64
	var job PreviewJob
	err = tx.QueryRow(ctx, `
		SELECT job.id, job.public_id::text, version.public_id::text, job.profile,
			version.storage_bucket, version.storage_key, version.size_bytes,
			version.mime_type, job.attempts + 1, job.max_attempts
		FROM drive.preview_job job
		JOIN drive.file_version version ON version.id = job.file_version_id AND version.state = 'ready'
		WHERE job.status = 'queued' AND job.run_after <= now() AND job.attempts < job.max_attempts
		ORDER BY job.run_after, job.id
		FOR UPDATE OF job SKIP LOCKED
		LIMIT 1
	`).Scan(
		&jobInternalID, &job.ID, &job.VersionID, &job.Profile,
		&job.SourceBucket, &job.SourceKey, &job.SourceSize,
		&job.SourceMIMEType, &job.Attempt, &job.MaxAttempts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("select preview job: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.preview_job
		SET status = 'processing', attempts = attempts + 1,
			lease_expires_at = now() + make_interval(secs => $2), updated_at = now()
		WHERE id = $1
	`, jobInternalID, int(lease.Seconds())); err != nil {
		return nil, fmt.Errorf("lease preview job: %w", err)
	}
	job.ArtifactKey = previewArtifactKey(job.VersionID, job.Profile, job.Attempt)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit preview job claim: %w", err)
	}
	return &job, nil
}

func (m *Metadata) CompletePreviewJob(ctx context.Context, input PreviewArtifactInput) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin preview completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var itemPublicID string
	if err := tx.QueryRow(ctx, `SELECT item.public_id::text FROM drive.preview_job job JOIN drive.file_version version ON version.id = job.file_version_id JOIN drive.item item ON item.id = version.item_id WHERE job.public_id = $1::uuid`, input.JobID).Scan(&itemPublicID); err != nil {
		return ErrJobLeaseLost
	}
	if err := lockItemBlobLifecycle(ctx, tx, itemPublicID); err != nil {
		return err
	}
	var jobID, versionID int64
	var profile, versionPublicID, bucket string
	if err := tx.QueryRow(ctx, `
		SELECT job.id, job.file_version_id, job.profile, version.public_id::text, version.storage_bucket
		FROM drive.preview_job job JOIN drive.file_version version ON version.id = job.file_version_id
		WHERE job.public_id = $1::uuid AND job.status = 'processing' AND job.attempts = $2
			AND job.lease_expires_at > clock_timestamp() AND version.state = 'ready'
		FOR UPDATE OF job, version
	`, input.JobID, input.Attempt).Scan(&jobID, &versionID, &profile, &versionPublicID, &bucket); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrJobLeaseLost
		}
		return fmt.Errorf("lock preview completion: %w", err)
	}
	if input.Key != previewArtifactKey(versionPublicID, profile, input.Attempt) || input.Bucket != bucket {
		return fmt.Errorf("preview artifact does not match its claim")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO drive.preview_artifact (
			file_version_id, profile, storage_bucket, storage_key,
			storage_etag, size_bytes, mime_type
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (file_version_id, profile) DO UPDATE
		SET storage_bucket = EXCLUDED.storage_bucket,
			storage_key = EXCLUDED.storage_key,
			storage_etag = EXCLUDED.storage_etag,
			size_bytes = EXCLUDED.size_bytes,
			mime_type = EXCLUDED.mime_type,
			created_at = now()
	`, versionID, profile, input.Bucket, input.Key, input.ETag, input.SizeBytes, input.MIMEType); err != nil {
		return fmt.Errorf("record preview artifact: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE drive.preview_job
		SET status = 'succeeded', lease_expires_at = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1
	`, jobID); err != nil {
		return fmt.Errorf("complete preview job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit preview completion: %w", err)
	}
	return nil
}

func (m *Metadata) FailPreviewJob(ctx context.Context, jobID string, attempt int, failure error) error {
	message := "preview generation failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	command, err := m.pool.Exec(ctx, `
		UPDATE drive.preview_job
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'queued' END,
			run_after = CASE WHEN attempts >= max_attempts THEN run_after ELSE now() + (attempts * interval '30 seconds') END,
			lease_expires_at = NULL, last_error = $2, updated_at = now()
		WHERE public_id = $1::uuid AND status = 'processing' AND attempts = $3 AND lease_expires_at > clock_timestamp()
	`, jobID, message, attempt)
	if err != nil {
		return fmt.Errorf("fail preview job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrJobLeaseLost
	}
	return nil
}

func previewArtifactKey(versionID, profile string, attempt int) string {
	return fmt.Sprintf(".previews/%s/%s/attempt-%d.jpg", versionID, profile, attempt)
}

package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUploadCompletionUsesFencedImmutableAttemptKeys(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.ExpectedSHA256 = make([]byte, 32)
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reservation.StorageKey, ".uploads/") || strings.HasPrefix(reservation.FinalStorageKey, reservation.StorageKey) {
		t.Fatalf("upload capability was not isolated from final storage: %+v", reservation)
	}
	queued, err := repo.QueueUploadCompletion(ctx, input.DrivePublicID, input.UserPublicID, reservation.ID, input.ExpectedSHA256)
	if err != nil || queued.Status != "completing" {
		t.Fatalf("queue completion: %+v %v", queued, err)
	}
	wrongDigest := make([]byte, 32)
	wrongDigest[0] = 1
	if _, err := repo.QueueUploadCompletion(ctx, input.DrivePublicID, input.UserPublicID, reservation.ID, wrongDigest); !errors.Is(err, ErrUploadIntegrity) {
		t.Fatalf("changed completion digest accepted: %v", err)
	}
	first, err := repo.ClaimUploadCompletionJob(ctx, time.Minute)
	if err != nil || first == nil || first.Attempt != 1 {
		t.Fatalf("claim first completion: %+v %v", first, err)
	}
	if !strings.Contains(first.FinalKey, "/attempt-1/") || first.StagingKey != reservation.StorageKey {
		t.Fatalf("unexpected first storage identities: %+v", first)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_completion_job SET lease_expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimUploadCompletionJob(ctx, time.Minute)
	if err != nil || second == nil || second.Attempt != 2 || second.FinalKey == first.FinalKey {
		t.Fatalf("claim replacement completion: %+v %v", second, err)
	}
	completed, err := repo.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: input.DrivePublicID, UserPublicID: input.UserPublicID, UploadID: reservation.ID,
		ETag: "sealed-etag", SizeBytes: input.SizeBytes, MIMEType: input.MIMEType,
		LastModified: time.Now(), SHA256: input.ExpectedSHA256, StorageKey: second.FinalKey,
		CompletionJobID: second.ID, CompletionAttempt: second.Attempt,
	})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete replacement claim: %+v %v", completed, err)
	}
	var publishedKey, jobStatus string
	if err := repo.pool.QueryRow(ctx, `
		SELECT version.storage_key, job.status
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		JOIN drive.upload_completion_job job ON job.upload_session_id = session.id
		WHERE session.public_id = $1::uuid
	`, reservation.ID).Scan(&publishedKey, &jobStatus); err != nil {
		t.Fatal(err)
	}
	if publishedKey != second.FinalKey || jobStatus != "succeeded" {
		t.Fatalf("wrong attempt published: key=%s status=%s", publishedKey, jobStatus)
	}
	if _, err := repo.CompleteUpload(ctx, CompleteUploadInput{
		DrivePublicID: input.DrivePublicID, UserPublicID: input.UserPublicID, UploadID: reservation.ID,
		ETag: "stale-etag", SizeBytes: input.SizeBytes, MIMEType: input.MIMEType,
		LastModified: time.Now(), SHA256: input.ExpectedSHA256, StorageKey: first.FinalKey,
		CompletionJobID: first.ID, CompletionAttempt: first.Attempt,
	}); err != nil {
		t.Fatalf("completed replay should remain idempotent: %v", err)
	}
	if err := repo.pool.QueryRow(ctx, `SELECT storage_key FROM drive.file_version WHERE storage_key = $1`, second.FinalKey).Scan(&publishedKey); err != nil {
		t.Fatalf("stale completion changed published bytes: %v", err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_completion_job SET staging_cleanup_after = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	cleanup, err := repo.NextUploadStagingCleanup(ctx)
	if err != nil || cleanup == nil || cleanup.Key != reservation.StorageKey {
		t.Fatalf("missing delayed staging cleanup: %+v %v", cleanup, err)
	}
	if err := repo.CompleteUploadStagingCleanup(ctx, cleanup.JobID); err != nil {
		t.Fatal(err)
	}
	if cleanup, err = repo.NextUploadStagingCleanup(ctx); err != nil || cleanup != nil {
		t.Fatalf("completed staging cleanup repeated: %+v %v", cleanup, err)
	}
}

func TestUploadCompletionTerminalFailureQueuesCleanup(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.ExpectedSHA256 = make([]byte, 32)
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueUploadCompletion(ctx, input.DrivePublicID, input.UserPublicID, reservation.ID, input.ExpectedSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_completion_job SET max_attempts = 1`); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimUploadCompletionJob(ctx, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim completion: %+v %v", job, err)
	}
	if err := repo.FailUploadCompletionJob(ctx, job.ID, job.Attempt, errors.New("bad bytes")); err != nil {
		t.Fatal(err)
	}
	var sessionStatus, versionState string
	var cleanupCount int
	if err := repo.pool.QueryRow(ctx, `
		SELECT session.status, version.state,
			(SELECT count(*) FROM drive.blob_deletion_job deletion WHERE deletion.file_version_id = version.id)
		FROM drive.upload_session session
		JOIN drive.file_version version ON version.id = session.file_version_id
		WHERE session.public_id = $1::uuid
	`, reservation.ID).Scan(&sessionStatus, &versionState, &cleanupCount); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "failed" || versionState != "failed" || cleanupCount != 1 {
		t.Fatalf("terminal completion not cleaned up: session=%s version=%s jobs=%d", sessionStatus, versionState, cleanupCount)
	}
}

package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

const (
	jobTimeout = 30 * time.Minute
	jobLease   = 31 * time.Minute
)

type Worker struct {
	metadata           *repository.Metadata
	store              *storage.MinIOClient
	requireMalwareScan bool
	workers            int
}

func NewWorker(metadata *repository.Metadata, store *storage.MinIOClient, requireMalwareScan bool, workers int) *Worker {
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	return &Worker{metadata: metadata, store: store, requireMalwareScan: requireMalwareScan, workers: workers}
}

func (w *Worker) Run(ctx context.Context) {
	for index := 0; index < w.workers; index++ {
		go w.loop(ctx, index)
	}
}

func (w *Worker) loop(ctx context.Context, workerID int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		worked, err := w.processNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error().Err(err).Int("worker_id", workerID).Msg("upload completion worker failed")
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processNext(ctx context.Context) (bool, error) {
	cleanup, err := w.metadata.NextUploadStagingCleanup(ctx)
	if err != nil {
		return false, err
	}
	if cleanup != nil {
		cleanupCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := w.deleteObject(cleanupCtx, cleanup.Bucket, cleanup.Key); err != nil {
			return true, fmt.Errorf("delete expired upload staging object: %w", err)
		}
		return true, w.metadata.CompleteUploadStagingCleanup(ctx, cleanup.JobID)
	}
	job, err := w.metadata.ClaimUploadCompletionJob(ctx, jobLease)
	if err != nil || job == nil {
		return false, err
	}
	workCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	if err := w.process(workCtx, job); err != nil {
		_ = w.deleteObject(context.WithoutCancel(ctx), job.Bucket, job.FinalKey)
		if failErr := w.metadata.FailUploadCompletionJob(ctx, job.ID, job.Attempt, err); failErr != nil {
			return true, fmt.Errorf("finalize upload: %v; record failure: %w", err, failErr)
		}
	}
	return true, nil
}

func (w *Worker) process(ctx context.Context, job *repository.UploadCompletionJob) error {
	staged, err := w.store.StatLegacyObject(ctx, job.Bucket, job.StagingKey)
	if errors.Is(err, storage.ErrObjectNotFound) && job.UploadMode == "multipart" {
		if err := w.completeMultipart(ctx, job); err != nil {
			return err
		}
		staged, err = w.store.StatLegacyObject(ctx, job.Bucket, job.StagingKey)
	}
	if err != nil {
		return fmt.Errorf("stat staged upload: %w", err)
	}
	if staged.Size != job.ExpectedSize {
		return repository.ErrUploadSizeMismatch
	}
	detectedMIMEType, err := w.store.DetectObjectContentType(ctx, job.Bucket, job.StagingKey)
	if err != nil {
		return fmt.Errorf("detect staged upload content type: %w", err)
	}
	if err := w.store.CopyObjectBetweenBucketsIfMatchWithContentType(ctx, job.Bucket, job.StagingKey, job.Bucket, job.FinalKey, staged.ETag, detectedMIMEType); err != nil {
		return fmt.Errorf("seal staged upload: %w", err)
	}
	sealed, err := w.store.StatLegacyObject(ctx, job.Bucket, job.FinalKey)
	if err != nil {
		return fmt.Errorf("stat sealed upload: %w", err)
	}
	if sealed.Size != job.ExpectedSize {
		return repository.ErrUploadSizeMismatch
	}
	digest, err := w.store.SHA256Object(ctx, job.Bucket, job.FinalKey)
	if err != nil {
		return fmt.Errorf("hash sealed upload: %w", err)
	}
	if !bytes.Equal(digest, job.ExpectedSHA256) {
		return repository.ErrUploadIntegrity
	}
	_, err = w.metadata.CompleteUpload(ctx, repository.CompleteUploadInput{
		DrivePublicID: job.DrivePublicID, UserPublicID: job.UserPublicID, UploadID: job.UploadID,
		ETag: sealed.ETag, SizeBytes: sealed.Size, MIMEType: detectedMIMEType,
		LastModified: sealed.LastModified, SHA256: digest, RequireMalwareScan: w.requireMalwareScan,
		StorageKey: job.FinalKey, CompletionJobID: job.ID, CompletionAttempt: job.Attempt,
	})
	if err != nil {
		return err
	}
	if err := w.deleteObject(ctx, job.Bucket, job.StagingKey); err != nil {
		log.Warn().Err(err).Str("upload_id", job.UploadID).Msg("sealed upload staging cleanup deferred")
	}
	return nil
}

func (w *Worker) completeMultipart(ctx context.Context, job *repository.UploadCompletionJob) error {
	if job.MinIOUploadID == "" || job.PartSize <= 0 {
		return repository.ErrUploadState
	}
	parts, err := w.store.ListMultipartParts(ctx, job.Bucket, job.StagingKey, job.MinIOUploadID)
	if err != nil {
		return fmt.Errorf("list multipart staging parts: %w", err)
	}
	expectedParts := int((job.ExpectedSize + job.PartSize - 1) / job.PartSize)
	if job.ExpectedSize == 0 || len(parts) != expectedParts {
		return repository.ErrUploadSizeMismatch
	}
	var total int64
	for index, part := range parts {
		expectedSize := job.PartSize
		if index == expectedParts-1 {
			expectedSize = job.ExpectedSize - int64(index)*job.PartSize
		}
		if part.Number != index+1 || part.Size != expectedSize {
			return repository.ErrUploadSizeMismatch
		}
		total += part.Size
	}
	if total != job.ExpectedSize {
		return repository.ErrUploadSizeMismatch
	}
	if err := w.store.CompleteMultipartUpload(ctx, job.Bucket, job.StagingKey, job.MinIOUploadID, job.MIMEType, parts); err != nil {
		return fmt.Errorf("assemble staged multipart upload: %w", err)
	}
	return nil
}

func (w *Worker) deleteObject(ctx context.Context, bucket, key string) error {
	err := w.store.DeleteObject(ctx, bucket, key)
	if errors.Is(err, storage.ErrObjectNotFound) {
		return nil
	}
	return err
}

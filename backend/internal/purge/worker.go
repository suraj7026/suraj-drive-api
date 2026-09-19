package purge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

const deletionLease = 2 * time.Minute

type Worker struct {
	metadata *repository.Metadata
	store    *storage.MinIOClient
}

func NewWorker(metadata *repository.Metadata, store *storage.MinIOClient) *Worker {
	return &Worker{metadata: metadata, store: store}
}

func (w *Worker) Run(ctx context.Context) {
	go w.loop(ctx)
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if _, err := w.metadata.QueueExpiredTrash(ctx, 200); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("failed to queue expired trash")
		}
		for processed := 0; processed < 50; processed++ {
			worked, err := w.processNext(ctx)
			if err != nil && ctx.Err() == nil {
				log.Error().Err(err).Msg("blob purge worker failed")
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processNext(ctx context.Context) (bool, error) {
	job, err := w.metadata.ClaimBlobDeletionJob(ctx, deletionLease)
	if err != nil || job == nil {
		return false, err
	}
	workCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if job.MinIOUploadID != "" {
		err = w.store.AbortMultipartUpload(workCtx, job.Bucket, job.StorageKey, job.MinIOUploadID)
		if storage.IsMultipartUploadNotFound(err) {
			err = nil
		}
	}
	var artifacts []repository.ArtifactLocation
	if err == nil {
		artifacts, err = w.metadata.PreviewArtifactsForVersion(workCtx, job.VersionID)
	}
	if err == nil {
		for _, artifact := range artifacts {
			deleteErr := w.store.DeleteObject(workCtx, artifact.Bucket, artifact.Key)
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrObjectNotFound) {
				err = fmt.Errorf("delete preview artifact: %w", deleteErr)
				break
			}
		}
	}
	if err == nil {
		if job.StagingKey != "" && job.StagingKey != job.StorageKey {
			deleteErr := w.store.DeleteObject(workCtx, job.Bucket, job.StagingKey)
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrObjectNotFound) {
				err = fmt.Errorf("delete upload staging blob: %w", deleteErr)
			}
		}
	}
	if err == nil {
		deleteErr := w.store.DeleteObject(workCtx, job.Bucket, job.StorageKey)
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrObjectNotFound) {
			err = fmt.Errorf("delete source blob: %w", deleteErr)
		}
	}
	if err != nil {
		if failErr := w.metadata.FailBlobDeletionJob(ctx, job.ID, job.Attempt, err); failErr != nil {
			return true, fmt.Errorf("purge failed: %v; record failure: %w", err, failErr)
		}
		return true, nil
	}
	if err := w.metadata.CompleteBlobDeletionJob(ctx, job.ID, job.Attempt); err != nil {
		return true, err
	}
	return true, nil
}

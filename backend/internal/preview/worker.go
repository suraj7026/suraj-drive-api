package preview

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/imageconv"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

const (
	maxHEICSourceBytes = 100 << 20
	jobTimeout         = 2 * time.Minute
	jobLease           = 3 * time.Minute
)

type Worker struct {
	metadata *repository.Metadata
	store    *storage.MinIOClient
	workers  int
}

func NewWorker(metadata *repository.Metadata, store *storage.MinIOClient, workers int) *Worker {
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}
	return &Worker{metadata: metadata, store: store, workers: workers}
}

func (w *Worker) Run(ctx context.Context) {
	for workerID := 0; workerID < w.workers; workerID++ {
		go w.runOne(ctx, workerID)
	}
}

func (w *Worker) runOne(ctx context.Context, workerID int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		worked, err := w.processNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error().Err(err).Int("worker_id", workerID).Msg("preview worker iteration failed")
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
	job, err := w.metadata.ClaimPreviewJob(ctx, jobLease)
	if err != nil || job == nil {
		return false, err
	}
	jobContext, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	if err := w.process(jobContext, job); err != nil {
		if failErr := w.metadata.FailPreviewJob(ctx, job.ID, job.Attempt, err); failErr != nil {
			return true, fmt.Errorf("preview failed: %v; record failure: %w", err, failErr)
		}
		return true, nil
	}
	return true, nil
}

func (w *Worker) process(ctx context.Context, job *repository.PreviewJob) error {
	if job.Profile != repository.HEICPreviewProfile {
		return fmt.Errorf("unsupported preview profile %q", job.Profile)
	}
	if job.SourceSize <= 0 || job.SourceSize > maxHEICSourceBytes {
		return fmt.Errorf("HEIC source exceeds the %d byte preview limit", maxHEICSourceBytes)
	}
	source, err := w.store.GetObject(ctx, job.SourceBucket, job.SourceKey)
	if err != nil {
		return fmt.Errorf("read preview source: %w", err)
	}
	if int64(len(source)) != job.SourceSize {
		return fmt.Errorf("preview source size changed")
	}
	jpeg, err := imageconv.HEICToJPEG(source)
	if err != nil {
		return err
	}
	if err := w.store.PutObject(ctx, job.SourceBucket, job.ArtifactKey, "image/jpeg", bytes.NewReader(jpeg), int64(len(jpeg))); err != nil {
		return fmt.Errorf("store preview artifact: %w", err)
	}
	artifact, err := w.store.StatLegacyObject(ctx, job.SourceBucket, job.ArtifactKey)
	if err != nil {
		return fmt.Errorf("stat preview artifact: %w", err)
	}
	if err := w.metadata.CompletePreviewJob(ctx, repository.PreviewArtifactInput{
		JobID: job.ID, Attempt: job.Attempt, Bucket: job.SourceBucket, Key: job.ArtifactKey,
		ETag: artifact.ETag, SizeBytes: artifact.Size, MIMEType: "image/jpeg",
	}); err != nil {
		return err
	}
	return nil
}

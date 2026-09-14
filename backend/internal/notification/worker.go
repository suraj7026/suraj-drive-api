package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"surajdrive/backend/internal/repository"
)

type Worker struct {
	metadata  *repository.Metadata
	sender    Sender
	publicURL string
	timeout   time.Duration
	workers   int
}

func NewWorker(metadata *repository.Metadata, sender Sender, publicURL string, timeout time.Duration, workers int) *Worker {
	if workers < 1 {
		workers = 1
	}
	return &Worker{metadata: metadata, sender: sender, publicURL: publicURL, timeout: timeout, workers: workers}
}

func (w *Worker) Run(ctx context.Context) {
	for index := 0; index < w.workers; index++ {
		go w.loop(ctx)
	}
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		worked, err := w.processNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("notification worker failed")
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
	job, err := w.metadata.ClaimNotificationJob(ctx, w.timeout+time.Minute)
	if err != nil || job == nil {
		return false, err
	}
	message, err := Render(*job, w.publicURL)
	if err == nil {
		deliveryContext, cancel := context.WithTimeout(ctx, w.timeout)
		err = w.sender.Send(deliveryContext, message)
		cancel()
	}
	if err != nil {
		if failErr := w.metadata.FailNotificationJob(ctx, job.ID, job.Attempt, err); failErr != nil {
			return true, fmt.Errorf("notification failed: %v; record failure: %w", err, failErr)
		}
		return true, nil
	}
	if err := w.metadata.CompleteNotificationJob(ctx, job.ID, job.Attempt); err != nil {
		return true, err
	}
	return true, nil
}

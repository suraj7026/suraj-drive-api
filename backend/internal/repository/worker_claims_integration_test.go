package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testWorkerClaim struct {
	id      string
	attempt int
	key     string
}
type testFencedWorker struct {
	table    string
	claim    func() (*testWorkerClaim, error)
	complete func(*testWorkerClaim) error
	fail     func(*testWorkerClaim) error
}

func fencedWorkerFixture(t *testing.T, kind string) (*Metadata, testFencedWorker) {
	t.Helper()
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	if kind == "notification" {
		if _, err := repo.pool.Exec(ctx, `INSERT INTO drive.notification_outbox (recipient_email, notification_type) VALUES ('recipient@example.test', 'test')`); err != nil {
			t.Fatal(err)
		}
		return repo, testFencedWorker{
			table: "drive.notification_outbox",
			claim: func() (*testWorkerClaim, error) {
				job, err := repo.ClaimNotificationJob(ctx, time.Minute)
				if job == nil {
					return nil, err
				}
				return &testWorkerClaim{id: job.ID, attempt: job.Attempt}, err
			},
			complete: func(job *testWorkerClaim) error { return repo.CompleteNotificationJob(ctx, job.id, job.attempt) },
			fail: func(job *testWorkerClaim) error {
				return repo.FailNotificationJob(ctx, job.id, job.attempt, errors.New("late worker"))
			},
		}
	}
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	completeLifecycleUpload(t, repo, input, reservation, kind == "malware")
	if kind == "malware" {
		return repo, testFencedWorker{
			table: "drive.malware_scan_job",
			claim: func() (*testWorkerClaim, error) {
				job, err := repo.ClaimMalwareScanJob(ctx, time.Minute)
				if job == nil {
					return nil, err
				}
				return &testWorkerClaim{id: job.ID, attempt: job.Attempt}, err
			},
			complete: func(job *testWorkerClaim) error {
				return repo.CompleteMalwareScanJob(ctx, job.id, job.attempt, "test", true, "")
			},
			fail: func(job *testWorkerClaim) error {
				return repo.FailMalwareScanJob(ctx, job.id, job.attempt, errors.New("late worker"))
			},
		}
	}
	if _, err := repo.GetOrQueuePreview(ctx, input.DrivePublicID, reservation.FinalStorageKey, HEICPreviewProfile); err != nil {
		t.Fatal(err)
	}
	return repo, testFencedWorker{
		table: "drive.preview_job",
		claim: func() (*testWorkerClaim, error) {
			job, err := repo.ClaimPreviewJob(ctx, time.Minute)
			if job == nil {
				return nil, err
			}
			return &testWorkerClaim{id: job.ID, attempt: job.Attempt, key: job.ArtifactKey}, err
		},
		complete: func(job *testWorkerClaim) error {
			return repo.CompletePreviewJob(ctx, PreviewArtifactInput{JobID: job.id, Attempt: job.attempt, Bucket: input.Bucket, Key: job.key, ETag: "test", SizeBytes: 10, MIMEType: "image/jpeg"})
		},
		fail: func(job *testWorkerClaim) error {
			return repo.FailPreviewJob(ctx, job.id, job.attempt, errors.New("late worker"))
		},
	}
}

func TestWorkersRejectStaleClaims(t *testing.T) {
	for _, kind := range []string{"preview", "malware", "notification"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repo, worker := fencedWorkerFixture(t, kind)
			first, err := worker.claim()
			if err != nil || first == nil {
				t.Fatalf("first claim: %+v %v", first, err)
			}
			if _, err := repo.pool.Exec(ctx, "UPDATE "+worker.table+" SET lease_expires_at = now() - interval '1 minute'"); err != nil {
				t.Fatal(err)
			}
			if err := worker.complete(first); !errors.Is(err, ErrJobLeaseLost) {
				t.Fatalf("expired completion accepted: %v", err)
			}
			second, err := worker.claim()
			if err != nil || second == nil || second.attempt != first.attempt+1 {
				t.Fatalf("replacement claim: %+v %v", second, err)
			}
			if kind == "preview" && first.key == second.key {
				t.Fatal("stale preview can overwrite the current attempt's artifact")
			}
			if err := worker.complete(first); !errors.Is(err, ErrJobLeaseLost) {
				t.Fatalf("stale completion accepted: %v", err)
			}
			if err := worker.fail(first); !errors.Is(err, ErrJobLeaseLost) {
				t.Fatalf("stale failure accepted: %v", err)
			}
			if err := worker.complete(second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkersPersistExhaustedLeaseRecovery(t *testing.T) {
	for _, kind := range []string{"preview", "malware", "notification"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repo, worker := fencedWorkerFixture(t, kind)
			if _, err := repo.pool.Exec(ctx, "UPDATE "+worker.table+" SET max_attempts = 1"); err != nil {
				t.Fatal(err)
			}
			first, err := worker.claim()
			if err != nil || first == nil {
				t.Fatalf("first claim: %+v %v", first, err)
			}
			if _, err := repo.pool.Exec(ctx, "UPDATE "+worker.table+" SET lease_expires_at = now() - interval '1 minute'"); err != nil {
				t.Fatal(err)
			}
			next, err := worker.claim()
			if err != nil || next != nil {
				t.Fatalf("exhausted job reclaimed: %+v %v", next, err)
			}
			var status string
			if err := repo.pool.QueryRow(ctx, "SELECT status FROM "+worker.table).Scan(&status); err != nil || status != "failed" {
				t.Fatalf("exhausted recovery not committed: %s %v", status, err)
			}
		})
	}
}

func TestPreviewCannotPublishAfterPurgeClaim(t *testing.T) {
	ctx := context.Background()
	repo, worker := fencedWorkerFixture(t, "preview")
	preview, err := worker.claim()
	if err != nil || preview == nil {
		t.Fatalf("claim preview: %+v %v", preview, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.item SET trashed_at = now() - interval '31 days', purge_after = now() - interval '1 day' WHERE kind = 'file'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueExpiredTrash(ctx, 100); err != nil {
		t.Fatal(err)
	}
	deletion, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || deletion == nil {
		t.Fatalf("claim deletion: %+v %v", deletion, err)
	}
	if err := worker.complete(preview); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("preview published after purge boundary: %v", err)
	}
	if _, err := worker.claim(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := repo.pool.QueryRow(ctx, `SELECT status FROM drive.preview_job`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("unavailable preview remains active: %s %v", status, err)
	}
}

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	drivemigrations "surajdrive/backend/internal/database/migrations"
)

func lifecycleRepository(t *testing.T) (*Metadata, ReserveUploadInput) {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_ADMIN_URL is not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("drive_lifecycle_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	config, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	db := stdlib.OpenDB(*config.ConnConfig)
	goose.SetBaseFS(drivemigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := NewMetadata(pool)
	principal, err := repo.ProvisionGoogleAccount(ctx, GoogleAccount{
		Subject: "lifecycle", Email: "lifecycle@example.test", EmailVerified: true,
		Name: "Lifecycle", StorageBucket: "drive-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE drive.user_account SET storage_quota_bytes = 100`); err != nil {
		t.Fatal(err)
	}
	return repo, ReserveUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		Bucket: principal.StorageBucket, Name: "sample.txt", SizeBytes: 60,
		MIMEType: "text/plain", IdempotencyKey: "first", TTL: time.Hour,
	}
}

func completeLifecycleUpload(t *testing.T, repo *Metadata, input ReserveUploadInput, reservation UploadReservation, scan bool) {
	t.Helper()
	_, err := repo.CompleteUpload(context.Background(), CompleteUploadInput{
		DrivePublicID: input.DrivePublicID, UserPublicID: input.UserPublicID, UploadID: reservation.ID,
		ETag: "test-etag", SizeBytes: input.SizeBytes, MIMEType: input.MIMEType,
		LastModified: time.Now(), SHA256: make([]byte, 32), RequireMalwareScan: scan,
		StorageKey: reservation.FinalStorageKey,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGoogleLoginPreservesDisabledAccountState(t *testing.T) {
	for _, status := range []string{"suspended", "deleted"} {
		for _, subject := range []string{"lifecycle", "replacement-google-identity"} {
			t.Run(status+"/"+subject, func(t *testing.T) {
				ctx := context.Background()
				repo, _ := lifecycleRepository(t)
				if _, err := repo.pool.Exec(ctx, `UPDATE drive.user_account SET status = $1`, status); err != nil {
					t.Fatal(err)
				}
				principal, err := repo.ProvisionGoogleAccount(ctx, GoogleAccount{
					Subject: subject, Email: "lifecycle@example.test", EmailVerified: true,
					Name: "Lifecycle", StorageBucket: "drive-lifecycle",
				})
				if !errors.Is(err, ErrAccountUnavailable) || principal != nil {
					t.Fatalf("disabled account was provisioned: %+v %v", principal, err)
				}
				var actual string
				if err := repo.pool.QueryRow(ctx, `SELECT status FROM drive.user_account`).Scan(&actual); err != nil || actual != status {
					t.Fatalf("login changed account status: %s %v", actual, err)
				}
			})
		}
	}
}

func TestUploadQuotaRetainedThroughCleanup(t *testing.T) {
	for _, state := range []string{"pending", "expired", "aborted", "quarantined", "ready"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			repo, input := lifecycleRepository(t)
			reservation, err := repo.ReserveUpload(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "expired":
				if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_session SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'`); err != nil {
					t.Fatal(err)
				}
			case "aborted":
				if err := repo.AbortUpload(ctx, input.DrivePublicID, input.UserPublicID, reservation.ID); err != nil {
					t.Fatal(err)
				}
			case "quarantined", "ready":
				completeLifecycleUpload(t, repo, input, reservation, state == "quarantined")
			}
			summary, err := repo.GetStorageSummary(ctx, input.DrivePublicID, input.UserPublicID)
			if err != nil || summary.CommittedBytes+summary.ReservedBytes != 60 {
				t.Fatalf("lost or double-counted storage: %+v, %v", summary, err)
			}
			input.IdempotencyKey = "second"
			if _, err := repo.ReserveUpload(ctx, input); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("quota bypass in %s: %v", state, err)
			}
			if state == "expired" {
				if _, err := repo.CompleteUpload(ctx, CompleteUploadInput{
					DrivePublicID: input.DrivePublicID, UserPublicID: input.UserPublicID,
					UploadID: reservation.ID, SHA256: make([]byte, 32),
				}); !errors.Is(err, ErrUploadState) {
					t.Fatalf("expired upload was completable: %v", err)
				}
				if err := repo.MarkUploadExpired(ctx, reservation.ID); err != nil {
					t.Fatal(err)
				}
			}
			if state == "expired" || state == "aborted" {
				job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
				if err != nil || job == nil {
					t.Fatalf("missing cleanup: %+v %v", job, err)
				}
				if err := repo.CompleteBlobDeletionJob(ctx, job.ID, job.Attempt); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.ReserveUpload(ctx, input); err != nil {
					t.Fatalf("deleted upload still consumes quota: %v", err)
				}
			}
		})
	}
}

func TestTerminalMalwareJobsQueueCleanup(t *testing.T) {
	for _, outcome := range []string{"infected", "failed", "lease_expired"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			repo, input := lifecycleRepository(t)
			reservation, err := repo.ReserveUpload(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			completeLifecycleUpload(t, repo, input, reservation, true)
			if _, err := repo.pool.Exec(ctx, `UPDATE drive.malware_scan_job SET max_attempts = 1`); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimMalwareScanJob(ctx, time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim scan: %+v %v", job, err)
			}
			switch outcome {
			case "infected":
				err = repo.CompleteMalwareScanJob(ctx, job.ID, job.Attempt, "test", false, "Eicar-Test-Signature")
			case "failed":
				err = repo.FailMalwareScanJob(ctx, job.ID, job.Attempt, errors.New("scanner unavailable"))
			case "lease_expired":
				if _, err := repo.pool.Exec(ctx, `UPDATE drive.malware_scan_job SET lease_expires_at = now() - interval '1 minute'`); err != nil {
					t.Fatal(err)
				}
				var next *MalwareScanJob
				next, err = repo.ClaimMalwareScanJob(ctx, time.Minute)
				if next != nil {
					t.Fatal("exhausted job was reclaimed")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			deletion, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
			if err != nil || deletion == nil || deletion.VersionID != job.VersionID {
				t.Fatalf("terminal scan did not queue cleanup: %+v %v", deletion, err)
			}
			summary, err := repo.GetStorageSummary(ctx, input.DrivePublicID, input.UserPublicID)
			if err != nil || summary.CommittedBytes != 60 {
				t.Fatalf("quota released before blob deletion: %+v %v", summary, err)
			}
		})
	}
}

func TestUploadExpiryRechecksStateBeforeSchedulingDeletion(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_session SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListExpiredUploads(ctx, 50)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("expiry candidates: %+v %v", candidates, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.upload_session SET expires_at = now() + interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkUploadExpired(ctx, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	completeLifecycleUpload(t, repo, input, reservation, true)
	if err := repo.MarkUploadExpired(ctx, candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := repo.pool.QueryRow(ctx, `SELECT status FROM drive.upload_session`).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("stale expiry changed upload: %s %v", status, err)
	}
	job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || job != nil {
		t.Fatalf("stale expiry queued destructive work: %+v %v", job, err)
	}
}

func TestDeletionLeaseExhaustionIsDurable(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AbortUpload(ctx, input.DrivePublicID, input.UserPublicID, reservation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.blob_deletion_job SET max_attempts = 1`); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim deletion: %+v %v", job, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.blob_deletion_job SET lease_expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || job != nil {
		t.Fatalf("exhausted deletion reclaimed: %+v %v", job, err)
	}
	var status string
	if err := repo.pool.QueryRow(ctx, `SELECT status FROM drive.blob_deletion_job`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("exhausted lease recovery rolled back: %s %v", status, err)
	}
}

func TestExpiredTrashQueueMakesProgressPastExistingJobs(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.SizeBytes = 20
	for i := 0; i < 3; i++ {
		input.IdempotencyKey = fmt.Sprintf("upload-%d", i)
		reservation, err := repo.ReserveUpload(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		completeLifecycleUpload(t, repo, input, reservation, false)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.item SET trashed_at = now() - interval '31 days', purge_after = now() - interval '1 day' WHERE kind = 'file'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		queued, err := repo.QueueExpiredTrash(ctx, 1)
		if err != nil || queued != 1 {
			t.Fatalf("queue stalled behind existing job at batch %d: %d %v", i, queued, err)
		}
	}
}

func TestCleanRevisionReadableWhileNewerRevisionQuarantined(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.SizeBytes = 20
	first, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	completeLifecycleUpload(t, repo, input, first, true)
	input.IdempotencyKey = "second"
	input.ConflictMode = "new_version"
	second, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	completeLifecycleUpload(t, repo, input, second, true)
	job, err := repo.ClaimMalwareScanJob(ctx, time.Minute)
	if err != nil || job == nil || job.StorageKey != first.FinalStorageKey {
		t.Fatalf("claim first revision scan: %+v %v", job, err)
	}
	if err := repo.CompleteMalwareScanJob(ctx, job.ID, job.Attempt, "test", true, ""); err != nil {
		t.Fatal(err)
	}
	var current bool
	if err := repo.pool.QueryRow(ctx, `SELECT is_current FROM drive.file_version WHERE public_id = $1::uuid`, job.VersionID).Scan(&current); err != nil || !current {
		t.Fatalf("clean revision unavailable while newer scan pending: %v %v", current, err)
	}
	job, err = repo.ClaimMalwareScanJob(ctx, time.Minute)
	if err != nil || job == nil || job.StorageKey != second.FinalStorageKey {
		t.Fatalf("claim second revision: %+v %v", job, err)
	}
	if err := repo.CompleteMalwareScanJob(ctx, job.ID, job.Attempt, "test", false, "Eicar-Test-Signature"); err != nil {
		t.Fatal(err)
	}
	listing, _, err := repo.ListDriveCursor(ctx, input.DrivePublicID, "", nil, 50)
	if err != nil || len(listing.Files) != 1 || listing.Files[0].Key != first.FinalStorageKey {
		t.Fatalf("infected revision hid clean file: %+v %v", listing, err)
	}
}

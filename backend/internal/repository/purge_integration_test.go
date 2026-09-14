package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func purgeFixture(t *testing.T, repo *Metadata, input ReserveUploadInput, folder bool) (itemID, versionID, restoreID string) {
	t.Helper()
	ctx := context.Background()
	reservation, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	completeLifecycleUpload(t, repo, input, reservation, false)
	if err := repo.pool.QueryRow(ctx, `SELECT item.public_id::text, version.public_id::text FROM drive.file_version version JOIN drive.item item ON item.id = version.item_id WHERE version.storage_key = $1`, reservation.FinalStorageKey).Scan(&itemID, &versionID); err != nil {
		t.Fatal(err)
	}
	restoreID = itemID
	if folder {
		if err := repo.pool.QueryRow(ctx, `INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
			SELECT drive_id, id, 'folder', 'Recovery folder', owner_user_id FROM drive.item WHERE parent_id IS NULL
			RETURNING public_id::text`).Scan(&restoreID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.pool.Exec(ctx, `UPDATE drive.item SET parent_id = (SELECT id FROM drive.item WHERE public_id = $2::uuid) WHERE public_id = $1::uuid`, itemID, restoreID); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, restoreID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.item SET trashed_at = now() - interval '31 days', purge_after = now() - interval '1 day' WHERE trashed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueExpiredTrash(ctx, 100); err != nil {
		t.Fatal(err)
	}
	return itemID, versionID, restoreID
}

func TestRestoreCancelsUnstartedDeletion(t *testing.T) {
	for _, folder := range []bool{false, true} {
		t.Run(fmt.Sprintf("folder=%t", folder), func(t *testing.T) {
			ctx := context.Background()
			repo, input := lifecycleRepository(t)
			_, versionID, restoreID := purgeFixture(t, repo, input, folder)
			if err := repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, restoreID, false); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
			if err != nil || job != nil {
				t.Fatalf("restored file can be deleted: %+v %v", job, err)
			}
			var state string
			var current bool
			if err := repo.pool.QueryRow(ctx, `SELECT state, is_current FROM drive.file_version WHERE public_id = $1::uuid`, versionID).Scan(&state, &current); err != nil || state != "ready" || !current {
				t.Fatalf("restored bytes unavailable: %s %v %v", state, current, err)
			}
		})
	}
}

func TestRestoreRejectsStartedOrCompletedDeletion(t *testing.T) {
	for _, folder := range []bool{false, true} {
		t.Run(fmt.Sprintf("folder=%t", folder), func(t *testing.T) {
			ctx := context.Background()
			repo, input := lifecycleRepository(t)
			itemID, versionID, restoreID := purgeFixture(t, repo, input, folder)
			job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim deletion: %+v %v", job, err)
			}
			assertCannotRestore := func() {
				t.Helper()
				if err := repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, restoreID, false); !errors.Is(err, ErrDeletionStarted) {
					t.Fatalf("restore crossed deletion boundary: %v", err)
				}
				if err := repo.SetVersionKeepForever(ctx, input.UserPublicID, itemID, versionID, true); err == nil {
					t.Fatal("retained a deleting version")
				}
				if err := repo.RestoreFileVersion(ctx, input.UserPublicID, itemID, versionID); err == nil {
					t.Fatal("restored a deleting version")
				}
			}
			assertCannotRestore()
			if err := repo.FailBlobDeletionJob(ctx, job.ID, job.Attempt, errors.New("temporary storage failure")); err != nil {
				t.Fatal(err)
			}
			assertCannotRestore()
			if _, err := repo.pool.Exec(ctx, `UPDATE drive.blob_deletion_job SET run_after = now()`); err != nil {
				t.Fatal(err)
			}
			job, err = repo.ClaimBlobDeletionJob(ctx, time.Minute)
			if err != nil || job == nil {
				t.Fatalf("retry deletion: %+v %v", job, err)
			}
			if err := repo.CompleteBlobDeletionJob(ctx, job.ID, job.Attempt); err != nil {
				t.Fatal(err)
			}
			assertCannotRestore()
		})
	}
}

func TestDeletionClaimsRecheckRestoredAndRetainedVersions(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	itemID, versionID, restoreID := purgeFixture(t, repo, input, false)
	if err := repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, restoreID, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a stale QueueExpiredTrash snapshot inserting after restore committed.
	if _, err := repo.pool.Exec(ctx, `INSERT INTO drive.blob_deletion_job (file_version_id) SELECT id FROM drive.file_version WHERE public_id = $1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || job != nil {
		t.Fatalf("stale queued job can delete active item: %+v %v", job, err)
	}
	if err := repo.SetVersionKeepForever(ctx, input.UserPublicID, itemID, versionID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, itemID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.item SET trashed_at = now() - interval '31 days', purge_after = now() - interval '1 day' WHERE trashed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pool.Exec(ctx, `INSERT INTO drive.blob_deletion_job (file_version_id) SELECT id FROM drive.file_version WHERE public_id = $1::uuid`, versionID); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || job != nil {
		t.Fatalf("stale queued job can delete retained version: %+v %v", job, err)
	}
}

func TestDeletionRejectsExpiredWorkerAttempt(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	purgeFixture(t, repo, input, false)
	first, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || first == nil {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE drive.blob_deletion_job SET lease_expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteBlobDeletionJob(ctx, first.ID, first.Attempt); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("expired completion accepted: %v", err)
	}
	second, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
	if err != nil || second == nil || second.Attempt != first.Attempt+1 {
		t.Fatalf("replacement claim: %+v %v", second, err)
	}
	if err := repo.CompleteBlobDeletionJob(ctx, first.ID, first.Attempt); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("stale completion accepted: %v", err)
	}
	if err := repo.FailBlobDeletionJob(ctx, first.ID, first.Attempt, errors.New("late error")); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("stale failure accepted: %v", err)
	}
	if err := repo.CompleteBlobDeletionJob(ctx, second.ID, second.Attempt); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRacesDeletionClaim(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.SizeBytes = 1
	for i := 0; i < 12; i++ {
		input.IdempotencyKey = fmt.Sprintf("race-%d", i)
		_, versionID, restoreID := purgeFixture(t, repo, input, false)
		start := make(chan struct{})
		restored := make(chan error, 1)
		type claimResult struct {
			job *BlobDeletionJob
			err error
		}
		claimed := make(chan claimResult, 1)
		go func() {
			<-start
			restored <- repo.SetItemTrashed(ctx, input.DrivePublicID, input.UserPublicID, restoreID, false)
		}()
		go func() {
			<-start
			job, err := repo.ClaimBlobDeletionJob(ctx, time.Minute)
			claimed <- claimResult{job, err}
		}()
		close(start)
		restoreErr, claim := <-restored, <-claimed
		if claim.err != nil {
			t.Fatal(claim.err)
		}
		if claim.job != nil {
			if claim.job.VersionID != versionID || !errors.Is(restoreErr, ErrDeletionStarted) {
				t.Fatalf("restore and deletion both won: %+v %v", claim.job, restoreErr)
			}
			if err := repo.CompleteBlobDeletionJob(ctx, claim.job.ID, claim.job.Attempt); err != nil {
				t.Fatal(err)
			}
		} else if restoreErr != nil {
			t.Fatalf("restore failed without deletion claim: %v", restoreErr)
		}
	}
}

func TestSchemaVersionReadableWithoutLedgerAccess(t *testing.T) {
	ctx := context.Background()
	repo, _ := lifecycleRepository(t)
	role := pgx.Identifier{fmt.Sprintf("drive_schema_probe_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := repo.pool.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := repo.pool.Exec(ctx, "DROP OWNED BY "+role); err != nil {
			t.Error(err)
		}
		if _, err := repo.pool.Exec(ctx, "DROP ROLE "+role); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.pool.Exec(ctx, "GRANT USAGE ON SCHEMA drive TO "+role); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := tx.QueryRow(ctx, `SELECT drive.schema_version()`).Scan(&version); err != nil || version != 16 {
		t.Fatalf("readiness schema version: %d %v", version, err)
	}
	if _, err := tx.Exec(ctx, `SELECT * FROM public.goose_db_version`); err == nil {
		t.Fatal("probe unexpectedly has raw migration ledger access")
	}
}

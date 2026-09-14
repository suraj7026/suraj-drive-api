package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUploadIdempotencyBindsRequestPayload(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.SourceVersionID = "copy-source-version"
	input.IdempotencyKey = strings.Repeat("k", 200)
	original, err := repo.ReserveUpload(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var destination string
	if err := repo.pool.QueryRow(ctx, `INSERT INTO drive.item (drive_id, parent_id, kind, name, owner_user_id)
		SELECT drive_id, id, 'folder', 'Other destination', owner_user_id FROM drive.item WHERE parent_id IS NULL
		RETURNING public_id::text`).Scan(&destination); err != nil {
		t.Fatal(err)
	}
	for name, modify := range map[string]func(*ReserveUploadInput){
		"name":        func(in *ReserveUploadInput) { in.Name = "different.txt" },
		"size":        func(in *ReserveUploadInput) { in.SizeBytes++ },
		"type":        func(in *ReserveUploadInput) { in.MIMEType = "application/octet-stream" },
		"bucket":      func(in *ReserveUploadInput) { in.Bucket = "different-bucket" },
		"parent":      func(in *ReserveUploadInput) { in.ParentPublicID = destination },
		"mode":        func(in *ReserveUploadInput) { in.UploadMode = "multipart"; in.PartSize = 5 << 20 },
		"conflict":    func(in *ReserveUploadInput) { in.ConflictMode = "new_version" },
		"copy source": func(in *ReserveUploadInput) { in.SourceVersionID = "another-copy-source" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			modify(&changed)
			if _, err := repo.ReserveUpload(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed payload reused reservation: %v", err)
			}
		})
	}
	input.TTL = 2 * time.Hour
	replayed, err := repo.ReserveUpload(ctx, input)
	if err != nil || replayed.ID != original.ID || replayed.ExpiresAt != original.ExpiresAt {
		t.Fatalf("retry did not preserve reservation: %+v %v", replayed, err)
	}
}

func TestUploadIdempotencySerializesConflictingRequests(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"one.txt", "two.txt"} {
		request := input
		request.Name = name
		go func() { <-start; _, err := repo.ReserveUpload(ctx, request); results <- err }()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrIdempotencyConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("conflicting concurrent requests: successes=%d conflicts=%d", succeeded, conflicted)
	}
	var count int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM drive.upload_session`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate upload reservations: %d %v", count, err)
	}
}

func TestUploadIdempotencyPreservesAllocatedNameAndRejectsUnboundLegacyKey(t *testing.T) {
	ctx := context.Background()
	repo, input := lifecycleRepository(t)
	input.SizeBytes = 20
	if _, err := repo.ReserveUpload(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = "second"
	second, err := repo.ReserveUpload(ctx, input)
	if err != nil || second.Name == input.Name {
		t.Fatalf("allocate duplicate name: %+v %v", second, err)
	}
	replayed, err := repo.ReserveUpload(ctx, input)
	if err != nil || replayed.Name != second.Name || replayed.ID != second.ID {
		t.Fatalf("allocated name changed on replay: %+v %v", replayed, err)
	}
	if _, err := repo.pool.Exec(ctx, `DELETE FROM drive.mutation_request WHERE operation = 'upload.reserve'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReserveUpload(ctx, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("unbound legacy reservation accepted: %v", err)
	}
}

package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"surajdrive/backend/internal/config"

	"github.com/minio/minio-go/v7"
)

func TestMultipartAgainstMinIO(t *testing.T) {
	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires isolated MinIO via TEST_MINIO_ENDPOINT")
	}
	var cfg config.Config
	cfg.MinIO.Endpoint = endpoint
	cfg.MinIO.AccessKey = os.Getenv("TEST_MINIO_ACCESS_KEY")
	cfg.MinIO.SecretKey = os.Getenv("TEST_MINIO_SECRET_KEY")
	cfg.MinIO.Region = "us-east-1"
	store, err := NewMinIOClient(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	const bucket = "drive-multipart-fixture"
	if err := store.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, key := range []string{"folder/recording.mp4", "folder/conflict.mp4"} {
			_ = store.client.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{})
		}
		_ = store.client.RemoveBucket(context.Background(), bucket)
	})

	const partSize = 5 << 20
	first := bytes.Repeat([]byte{0x42}, partSize)
	last := []byte("last part")
	size := int64(len(first) + len(last))
	begin := func(key, marker string) (string, string) {
		t.Helper()
		resolved, id, err := store.BeginMultipart(ctx, bucket, key, "video/mp4", marker)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.CancelMultipart(context.Background(), bucket, resolved, id) })
		return resolved, id
	}
	put := func(key, id string, number int, signedSize int64, payload []byte, success bool) {
		t.Helper()
		signed, err := store.PresignMultipartPart(ctx, bucket, key, id, number, signedSize)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if (res.StatusCode == http.StatusOK) != success {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("upload part %d: unexpected HTTP %d: %s", number, res.StatusCode, body)
		}
	}

	key, id := begin("folder/recording.mp4", "fixture-a")
	put(key, id, 1, partSize, first[:len(first)-1], false)
	put(key, id, 1, partSize, first, true)
	if err := store.FinishMultipart(ctx, bucket, key, id, "fixture-a", size, partSize); !errors.Is(err, ErrMultipartIncomplete) {
		t.Fatalf("expected incomplete upload, got %v", err)
	}
	put(key, id, 2, int64(len(last)), last, true)
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.FinishMultipart(ctx, bucket, key, id, "fixture-a", size, partSize); err != nil {
			t.Fatalf("complete attempt %d: %v", attempt+1, err)
		}
	}
	got, err := store.GetObject(ctx, bucket, key)
	if err != nil || !bytes.Equal(got, append(first, last...)) {
		t.Fatalf("assembled bytes differ: %v", err)
	}

	key, id = begin("folder/conflict.mp4", "fixture-b")
	put(key, id, 1, partSize, first, true)
	put(key, id, 2, int64(len(last)), last, true)
	if err := store.PutObject(ctx, bucket, key, "video/mp4", bytes.NewReader(last), int64(len(last))); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishMultipart(ctx, bucket, key, id, "fixture-b", size, partSize); !errors.Is(err, ErrMultipartConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	got, err = store.GetObject(ctx, bucket, key)
	if err != nil || !bytes.Equal(got, last) {
		t.Fatalf("existing file changed: %v", err)
	}
}

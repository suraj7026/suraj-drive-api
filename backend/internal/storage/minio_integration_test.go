package storage

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"surajdrive/backend/internal/config"
)

func TestPresignedLengthsAndImmutableSealing(t *testing.T) {
	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT is not set")
	}
	cfg := &config.Config{}
	cfg.MinIO.Endpoint = endpoint
	cfg.MinIO.PublicEndpoint = endpoint
	cfg.MinIO.AccessKey = os.Getenv("TEST_MINIO_ACCESS_KEY")
	cfg.MinIO.SecretKey = os.Getenv("TEST_MINIO_SECRET_KEY")
	cfg.MinIO.BucketPrefix = "drive-test"
	cfg.MinIO.Region = "us-east-1"
	store, err := NewMinIOClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	bucket := "drive-upload-integration"
	if err := store.EnsureBucket(ctx, bucket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.DeletePrefix(context.Background(), bucket, ".uploads/")
		_ = store.DeletePrefix(context.Background(), bucket, ".objects/")
	})

	wrongLengthURL, err := store.PresignedPutURLForSize(ctx, bucket, ".uploads/wrong/content", time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(mustPutRequest(t, wrongLengthURL, "four"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatal("presigned upload accepted a content length other than the reservation")
	}

	stagingKey := ".uploads/seal/content"
	putURL, err := store.PresignedPutURLForSize(ctx, bucket, stagingKey, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	put := func(contents string) {
		t.Helper()
		response, err := http.DefaultClient.Do(mustPutRequest(t, putURL, contents))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			t.Fatalf("presigned upload failed: status=%d body=%s", response.StatusCode, body)
		}
	}
	put("one")
	staged, err := store.StatLegacyObject(ctx, bucket, stagingKey)
	if err != nil {
		t.Fatal(err)
	}
	finalKey := ".objects/item/version/attempt-1/content"
	if err := store.CopyObjectBetweenBucketsIfMatch(ctx, bucket, stagingKey, bucket, finalKey, staged.ETag); err != nil {
		t.Fatal(err)
	}
	put("two")
	sealed, err := store.GetObject(ctx, bucket, finalKey)
	if err != nil || string(sealed) != "one" {
		t.Fatalf("stale upload capability changed sealed bytes: %q %v", sealed, err)
	}
	if err := store.CopyObjectBetweenBucketsIfMatch(ctx, bucket, stagingKey, bucket, finalKey, ""); err == nil {
		t.Fatal("immutable destination was overwritten")
	}
	staleKey := ".objects/item/version/attempt-2/content"
	if err := store.CopyObjectBetweenBucketsIfMatch(ctx, bucket, stagingKey, bucket, staleKey, staged.ETag); err == nil {
		t.Fatal("conditional seal accepted a changed staging ETag")
	}

	mimeStagingKey := ".uploads/mime/content"
	if err := store.PutObject(ctx, bucket, mimeStagingKey, "image/png", strings.NewReader("<html><script>alert(1)</script></html>"), 38); err != nil {
		t.Fatal(err)
	}
	detected, err := store.DetectObjectContentType(ctx, bucket, mimeStagingKey)
	if err != nil || detected != "text/html; charset=utf-8" {
		t.Fatalf("expected byte-detected HTML, got %q err=%v", detected, err)
	}
	mimeStaged, err := store.StatLegacyObject(ctx, bucket, mimeStagingKey)
	if err != nil {
		t.Fatal(err)
	}
	mimeFinalKey := ".objects/item/mime/attempt-1/content"
	if err := store.CopyObjectBetweenBucketsIfMatchWithContentType(ctx, bucket, mimeStagingKey, bucket, mimeFinalKey, mimeStaged.ETag, detected); err != nil {
		t.Fatal(err)
	}
	mimeFinal, err := store.StatLegacyObject(ctx, bucket, mimeFinalKey)
	if err != nil || mimeFinal.ContentType != detected {
		t.Fatalf("sealed object content type was not replaced: object=%+v err=%v", mimeFinal, err)
	}
}

func mustPutRequest(t *testing.T, urlValue, contents string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, urlValue, strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "text/plain")
	return request
}

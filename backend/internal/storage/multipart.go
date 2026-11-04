package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

const multipartMarker = "drive-upload"

var ErrMultipartConflict = fmt.Errorf("destination already exists; select the file again to create a new version")
var ErrMultipartIncomplete = fmt.Errorf("some upload parts are missing or have the wrong size")

func (m *MinIOClient) BeginMultipart(ctx context.Context, bucket, requestedKey, contentType, marker string) (string, string, error) {
	key, err := m.ResolveAvailableKey(ctx, bucket, requestedKey)
	if err != nil {
		return "", "", err
	}
	if isPreviewArtifact(key) || path.Base(key) == ".keep" {
		return "", "", ErrInvalidPath
	}
	core := minio.Core{Client: m.client}
	id, err := core.NewMultipartUpload(ctx, bucket, key, minio.PutObjectOptions{
		ContentType: contentType, UserMetadata: map[string]string{multipartMarker: marker},
	})
	return key, id, err
}

func (m *MinIOClient) PresignMultipartPart(ctx context.Context, bucket, key, id string, number int, size int64) (string, error) {
	params := url.Values{"uploadId": {id}, "partNumber": {strconv.Itoa(number)}}
	headers := http.Header{}
	headers.Set("Content-Length", strconv.FormatInt(size, 10))
	signed, err := m.presignClient.PresignHeader(ctx, http.MethodPut, bucket, key, 15*time.Minute, params, headers)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}

func (m *MinIOClient) FinishMultipart(ctx context.Context, bucket, key, id, marker string, size, partSize int64) error {
	// A previous completion can succeed even when its response is lost.
	info, err := m.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	switch {
	case err == nil && info.Metadata.Get("X-Amz-Meta-"+multipartMarker) == marker && info.Size == size:
		return nil
	case err == nil:
		return ErrMultipartConflict
	case !isExistenceProbeMiss(err):
		return err
	}

	core := minio.Core{Client: m.client}
	partCount := int((size + partSize - 1) / partSize)
	parts := make([]minio.CompletePart, 0, partCount)
	for marker := 0; ; {
		page, err := core.ListObjectParts(ctx, bucket, key, id, marker, 1000)
		if err != nil {
			return err
		}
		for _, part := range page.ObjectParts {
			number := len(parts) + 1
			expectedSize := partSize
			if number == partCount {
				expectedSize = size - int64(number-1)*partSize
			}
			if number > partCount || part.PartNumber != number || part.Size != expectedSize {
				return ErrMultipartIncomplete
			}
			parts = append(parts, minio.CompletePart{PartNumber: number, ETag: strings.Trim(part.ETag, "\"")})
		}
		if !page.IsTruncated {
			break
		}
		marker = page.NextPartNumberMarker
	}
	if len(parts) != partCount {
		return ErrMultipartIncomplete
	}

	var opts minio.PutObjectOptions
	opts.SetMatchETagExcept("*")
	_, err = core.CompleteMultipartUpload(ctx, bucket, key, id, parts, opts)
	if minio.ToErrorResponse(err).Code == "PreconditionFailed" {
		return ErrMultipartConflict
	}
	return err
}

func (m *MinIOClient) CancelMultipart(ctx context.Context, bucket, key, id string) error {
	core := minio.Core{Client: m.client}
	err := core.AbortMultipartUpload(ctx, bucket, key, id)
	if minio.ToErrorResponse(err).Code == "NoSuchUpload" {
		return nil
	}
	return err
}

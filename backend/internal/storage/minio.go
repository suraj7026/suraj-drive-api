package storage

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"surajdrive/backend/internal/config"
	"surajdrive/backend/internal/model"
)

const maxVersionSuffix = 5

var (
	ErrInvalidPath         = errors.New("invalid object path")
	ErrVersionLimitReached = errors.New("maximum of 5 alternate versions already exist for this file")
	ErrObjectNotFound      = errors.New("object not found")
)

type MinIOClient struct {
	client        *minio.Client
	presignClient *minio.Client
	bucketPrefix  string
	region        string
}

func NewMinIOClient(cfg *config.Config) (*MinIOClient, error) {
	client, err := newClient(cfg.MinIO.Endpoint, cfg)
	if err != nil {
		return nil, err
	}

	presignEndpoint := strings.TrimSpace(cfg.MinIO.PublicEndpoint)
	if presignEndpoint == "" {
		presignEndpoint = cfg.MinIO.Endpoint
	}

	presignClient := client
	if presignEndpoint != cfg.MinIO.Endpoint {
		presignClient, err = newClient(presignEndpoint, cfg)
		if err != nil {
			return nil, err
		}
	}

	return &MinIOClient{
		client:        client,
		presignClient: presignClient,
		bucketPrefix:  strings.Trim(strings.ToLower(cfg.MinIO.BucketPrefix), "-"),
		region:        cfg.MinIO.Region,
	}, nil
}

func newClient(endpoint string, cfg *config.Config) (*minio.Client, error) {
	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIO.AccessKey, cfg.MinIO.SecretKey, ""),
		Secure: cfg.MinIO.UseSSL,
		Region: cfg.MinIO.Region,
	})
}

func (m *MinIOClient) BucketNameForSubject(subject string) (string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", fmt.Errorf("google subject is required")
	}

	var cleaned strings.Builder
	for _, r := range strings.ToLower(subject) {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned.WriteRune(r)
		case r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		default:
			cleaned.WriteByte('-')
		}
	}

	suffix := strings.Trim(cleaned.String(), "-")
	if suffix == "" {
		return "", fmt.Errorf("unable to derive bucket name from subject")
	}

	bucket := fmt.Sprintf("%s-%s", m.bucketPrefix, suffix)
	if len(bucket) > 63 {
		sum := sha1.Sum([]byte(subject))
		prefix := m.bucketPrefix
		if len(prefix) > 48 {
			prefix = prefix[:48]
		}
		bucket = fmt.Sprintf("%s-%x", prefix, sum[:6])
	}
	if len(bucket) < 3 {
		return "", fmt.Errorf("bucket name %q is too short", bucket)
	}
	return bucket, nil
}

func (m *MinIOClient) EnsureBucket(ctx context.Context, bucket string) error {
	exists, err := m.client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	err = m.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: m.region})
	if err != nil {
		response := minio.ToErrorResponse(err)
		if response.Code == "BucketAlreadyOwnedByYou" || response.Code == "BucketAlreadyExists" {
			return nil
		}
		return err
	}
	return nil
}

func (m *MinIOClient) ListObjects(ctx context.Context, bucket, prefix string, offset, limit int) (model.ListResponse, error) {
	normalizedPrefix, err := normalizePrefix(prefix, true)
	if err != nil {
		return model.ListResponse{}, err
	}

	folders := make([]model.FolderEntry, 0)
	files := make([]model.FileObject, 0)
	seenFolders := make(map[string]struct{})

	for object := range m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: normalizedPrefix, Recursive: false}) {
		if object.Err != nil {
			return model.ListResponse{}, object.Err
		}

		if object.Key == "" || object.Key == normalizedPrefix {
			continue
		}

		if strings.HasSuffix(object.Key, "/") {
			addFolder(seenFolders, &folders, object.Key)
			continue
		}

		if isKeepObject(object.Key) {
			addFolder(seenFolders, &folders, strings.TrimSuffix(object.Key, ".keep"))
			continue
		}

		files = append(files, toFileObject(object))
	}

	sort.Slice(folders, func(i, j int) bool {
		if folders[i].Name == folders[j].Name {
			return folders[i].Prefix < folders[j].Prefix
		}
		return folders[i].Name < folders[j].Name
	})
	sort.Slice(files, func(i, j int) bool {
		if files[i].Name == files[j].Name {
			return files[i].Key < files[j].Key
		}
		return files[i].Name < files[j].Name
	})

	pagedFolders, pagedFiles, pagination := paginateListing(folders, files, offset, limit)
	return model.ListResponse{
		Prefix:     normalizedPrefix,
		Folders:    pagedFolders,
		Files:      pagedFiles,
		Pagination: pagination,
	}, nil
}

func (m *MinIOClient) PutObject(ctx context.Context, bucket, key, contentType string, reader io.Reader, size int64) error {
	normalizedKey, err := normalizeObjectKey(key)
	if err != nil {
		return err
	}

	_, err = m.client.PutObject(ctx, bucket, normalizedKey, reader, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m *MinIOClient) DeleteObject(ctx context.Context, bucket, key string) error {
	normalizedKey, err := normalizeObjectKey(key)
	if err != nil {
		return err
	}

	exists, err := m.objectExists(ctx, bucket, normalizedKey)
	if err != nil {
		return err
	}
	if !exists {
		return ErrObjectNotFound
	}

	return m.client.RemoveObject(ctx, bucket, normalizedKey, minio.RemoveObjectOptions{})
}

func (m *MinIOClient) DeletePrefix(ctx context.Context, bucket, prefix string) error {
	normalizedPrefix, err := normalizePrefix(prefix, false)
	if err != nil {
		return err
	}

	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		for object := range m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: normalizedPrefix, Recursive: true}) {
			if object.Err == nil {
				objectsCh <- object
			}
		}
	}()

	for removeErr := range m.client.RemoveObjects(ctx, bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if removeErr.Err != nil {
			return removeErr.Err
		}
	}
	return nil
}

func (m *MinIOClient) CopyObject(ctx context.Context, bucket, srcKey, dstKey string) (string, error) {
	normalizedSrc, err := normalizeObjectKey(srcKey)
	if err != nil {
		return "", err
	}

	exists, err := m.objectExists(ctx, bucket, normalizedSrc)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrObjectNotFound
	}

	resolvedDst, err := m.ResolveAvailableKey(ctx, bucket, dstKey)
	if err != nil {
		return "", err
	}

	src := minio.CopySrcOptions{Bucket: bucket, Object: normalizedSrc}
	dst := minio.CopyDestOptions{Bucket: bucket, Object: resolvedDst}
	if _, err := m.client.CopyObject(ctx, dst, src); err != nil {
		return "", err
	}

	return resolvedDst, nil
}

func (m *MinIOClient) PresignedGetURL(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	normalizedKey, err := normalizeObjectKey(key)
	if err != nil {
		return "", err
	}

	exists, err := m.objectExists(ctx, bucket, normalizedKey)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrObjectNotFound
	}

	urlValue, err := m.presignClient.PresignedGetObject(ctx, bucket, normalizedKey, ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return urlValue.String(), nil
}

func (m *MinIOClient) PresignedPutURL(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	normalizedKey, err := normalizeObjectKey(key)
	if err != nil {
		return "", err
	}

	urlValue, err := m.presignClient.PresignedPutObject(ctx, bucket, normalizedKey, ttl)
	if err != nil {
		return "", err
	}
	return urlValue.String(), nil
}

func (m *MinIOClient) CreateFolder(ctx context.Context, bucket, prefix string) error {
	normalizedPrefix, err := normalizePrefix(prefix, false)
	if err != nil {
		return err
	}

	placeholderKey := normalizedPrefix + ".keep"
	_, err = m.client.PutObject(ctx, bucket, placeholderKey, strings.NewReader(""), 0, minio.PutObjectOptions{ContentType: "application/x-directory"})
	return err
}

func (m *MinIOClient) Search(ctx context.Context, bucket, prefix, query string, offset, limit int) (model.SearchResponse, error) {
	normalizedPrefix, err := normalizePrefix(prefix, true)
	if err != nil {
		return model.SearchResponse{}, err
	}

	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return model.SearchResponse{}, fmt.Errorf("query is required")
	}

	matches := make([]model.FileObject, 0)
	needle := strings.ToLower(trimmedQuery)
	for object := range m.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: normalizedPrefix, Recursive: true}) {
		if object.Err != nil {
			return model.SearchResponse{}, object.Err
		}
		if object.Key == "" || strings.HasSuffix(object.Key, "/") || isKeepObject(object.Key) {
			continue
		}
		if strings.Contains(strings.ToLower(object.Key), needle) {
			matches = append(matches, toFileObject(object))
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Key == matches[j].Key {
			return matches[i].LastModified.After(matches[j].LastModified)
		}
		return matches[i].Key < matches[j].Key
	})

	paged, pagination := paginateFiles(matches, offset, limit)
	return model.SearchResponse{
		Query:      trimmedQuery,
		Prefix:     normalizedPrefix,
		Results:    paged,
		Pagination: pagination,
	}, nil
}

func (m *MinIOClient) ResolveAvailableKey(ctx context.Context, bucket, key string) (string, error) {
	normalizedKey, err := normalizeObjectKey(key)
	if err != nil {
		return "", err
	}

	exists, err := m.objectExists(ctx, bucket, normalizedKey)
	if err != nil {
		return "", err
	}
	if !exists {
		return normalizedKey, nil
	}

	dir, fileName := path.Split(normalizedKey)
	ext := path.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	for version := 1; version <= maxVersionSuffix; version++ {
		candidateName := fmt.Sprintf("%sv%d%s", base, version, ext)
		candidate := strings.TrimPrefix(path.Join(dir, candidateName), "/")
		exists, err := m.objectExists(ctx, bucket, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}

	return "", ErrVersionLimitReached
}

func (m *MinIOClient) objectExists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := m.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func isNotFoundError(err error) bool {
	response := minio.ToErrorResponse(err)
	switch response.Code {
	case "NoSuchKey", "NoSuchBucket", "NoSuchObject", "NotFound":
		return true
	default:
		return false
	}
}

func normalizeObjectKey(key string) (string, error) {
	cleaned := cleanPath(key)
	if cleaned == "" {
		return "", fmt.Errorf("%w: key is required", ErrInvalidPath)
	}
	return cleaned, nil
}

func normalizePrefix(prefix string, allowEmpty bool) (string, error) {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: prefix is required", ErrInvalidPath)
	}

	cleaned := cleanPath(strings.TrimSuffix(trimmed, "/"))
	if cleaned == "" {
		return "", fmt.Errorf("%w: prefix is required", ErrInvalidPath)
	}
	return cleaned + "/", nil
}

func cleanPath(value string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "..") {
		return ""
	}
	cleaned := strings.TrimPrefix(path.Clean(trimmed), "./")
	if cleaned == "." || cleaned == "/" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return cleaned
}

func isKeepObject(key string) bool {
	return strings.HasSuffix(key, ".keep") && path.Base(key) == ".keep"
}

func addFolder(seen map[string]struct{}, folders *[]model.FolderEntry, prefix string) {
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		return
	}
	prefix = trimmed + "/"
	if _, ok := seen[prefix]; ok {
		return
	}
	seen[prefix] = struct{}{}
	*folders = append(*folders, model.FolderEntry{
		Prefix: prefix,
		Name:   path.Base(trimmed),
	})
}

func toFileObject(object minio.ObjectInfo) model.FileObject {
	return model.FileObject{
		Key:          object.Key,
		Name:         path.Base(object.Key),
		Size:         object.Size,
		LastModified: object.LastModified,
		ContentType:  object.ContentType,
		ETag:         object.ETag,
	}
}

func paginateListing(folders []model.FolderEntry, files []model.FileObject, offset, limit int) ([]model.FolderEntry, []model.FileObject, model.Pagination) {
	total := len(folders) + len(files)
	start, end := bounds(offset, limit, total)

	folderStart := min(start, len(folders))
	folderEnd := min(end, len(folders))
	pagedFolders := folders[folderStart:folderEnd]

	fileStart := max(0, start-len(folders))
	fileEnd := max(0, end-len(folders))
	fileEnd = min(fileEnd, len(files))
	pagedFiles := files[fileStart:fileEnd]

	returned := len(pagedFolders) + len(pagedFiles)
	return pagedFolders, pagedFiles, buildPagination(offset, limit, total, returned)
}

func paginateFiles(files []model.FileObject, offset, limit int) ([]model.FileObject, model.Pagination) {
	total := len(files)
	start, end := bounds(offset, limit, total)
	paged := files[start:end]
	return paged, buildPagination(offset, limit, total, len(paged))
}

func bounds(offset, limit, total int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}

func buildPagination(offset, limit, total, returned int) model.Pagination {
	hasMore := offset+returned < total
	var nextOffset *int
	if hasMore {
		next := offset + returned
		nextOffset = &next
	}
	return model.Pagination{
		Offset:     offset,
		Limit:      limit,
		Returned:   returned,
		Total:      total,
		HasMore:    hasMore,
		NextOffset: nextOffset,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

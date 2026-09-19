package importer

import (
	"context"
	"fmt"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

type LegacyStats struct {
	Scanned  int `json:"scanned"`
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

type Legacy struct {
	metadata *repository.Metadata
	storage  *storage.MinIOClient
}

func NewLegacy(metadata *repository.Metadata, storageClient *storage.MinIOClient) *Legacy {
	return &Legacy{metadata: metadata, storage: storageClient}
}

func (i *Legacy) ReconcileDrive(ctx context.Context, principal *auth.Principal) (LegacyStats, error) {
	objects, err := i.storage.ListLegacyObjects(ctx, principal.StorageBucket)
	if err != nil {
		return LegacyStats{}, fmt.Errorf("list legacy objects: %w", err)
	}

	stats := LegacyStats{Scanned: len(objects)}
	for _, object := range objects {
		imported, err := i.metadata.ImportLegacyObject(ctx, principal.DriveID, repository.LegacyObject{
			Bucket:       principal.StorageBucket,
			Key:          object.Key,
			ETag:         object.ETag,
			SizeBytes:    object.Size,
			MIMEType:     object.ContentType,
			LastModified: object.LastModified,
			FolderMarker: object.FolderMarker,
		})
		if err != nil {
			return stats, fmt.Errorf("import %q: %w", object.Key, err)
		}
		if imported {
			stats.Imported++
		} else {
			stats.Skipped++
		}
	}
	return stats, nil
}

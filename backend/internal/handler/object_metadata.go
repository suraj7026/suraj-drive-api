package handler

import (
	"fmt"
	"net/http"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) recordObjectMetadata(r *http.Request, key string) error {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		return fmt.Errorf("missing authenticated drive")
	}
	object, err := h.store.StatLegacyObject(r.Context(), principal.StorageBucket, key)
	if err != nil {
		return fmt.Errorf("read uploaded object metadata: %w", err)
	}
	_, err = h.metadata.RecordStoredObject(r.Context(), principal.DriveID, repository.LegacyObject{
		Bucket:       principal.StorageBucket,
		Key:          object.Key,
		ETag:         object.ETag,
		SizeBytes:    object.Size,
		MIMEType:     object.ContentType,
		LastModified: object.LastModified,
		FolderMarker: object.FolderMarker,
	})
	if err != nil {
		return fmt.Errorf("record uploaded object metadata: %w", err)
	}
	return nil
}

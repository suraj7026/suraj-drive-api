package handler

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/validation"
)

const (
	previewURLTTL = 15 * time.Minute
)

// Preview returns a presigned URL to a JPEG render of the requested object.
// For HEIC/HEIF inputs it lazily generates and caches a JPEG preview in the
// `.previews/` prefix of the user's bucket. For all other file types it
// behaves like PresignDownload (pass-through).
func (h *FileHandler) Preview(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := validation.ItemPath(key, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if !isHEICKey(key) {
		urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), principal.StorageBucket, key, previewURLTTL, path.Base(key), true)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, model.PresignResponse{
			URL:       urlValue,
			Key:       key,
			ExpiresIn: "15m",
		})
		return
	}

	preview, err := h.metadata.GetOrQueuePreview(r.Context(), principal.DriveID, key, repository.HEICPreviewProfile)
	if err != nil {
		if errors.Is(err, repository.ErrPreviewSourceNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if preview.Status == "failed" {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("preview generation failed"))
		return
	}
	if preview.Status != "succeeded" || preview.ArtifactKey == "" {
		writeJSON(w, http.StatusAccepted, model.PreviewResponse{
			Status: preview.Status, JobID: preview.JobID, RetryAfter: 2,
		})
		return
	}

	urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), preview.SourceBucket, preview.ArtifactKey, previewURLTTL, path.Base(key)+".jpg", true)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, model.PreviewResponse{
		Status:    "ready",
		URL:       urlValue,
		Key:       preview.ArtifactKey,
		ExpiresIn: "15m",
	})
}

func isHEICKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.HasSuffix(lower, ".heic") || strings.HasSuffix(lower, ".heif")
}

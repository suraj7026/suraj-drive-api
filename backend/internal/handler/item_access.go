package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) DownloadItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	itemID := chi.URLParam(r, "itemID")
	file, err := h.metadata.ResolveAccessibleFile(r.Context(), principal.UserID, itemID)
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), file.Bucket, file.StorageKey, 15*time.Minute, file.Name, false)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	_ = h.metadata.MarkItemOpened(r.Context(), file.DriveID, principal.UserID, itemID)
	_ = h.metadata.RecordItemRead(r.Context(), principal.UserID, itemID, "file.downloaded")
	writeJSON(w, http.StatusOK, model.PresignResponse{URL: urlValue, ExpiresIn: "15m"})
}

func (h *FileHandler) PreviewItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	itemID := chi.URLParam(r, "itemID")
	file, err := h.metadata.ResolveAccessibleFile(r.Context(), principal.UserID, itemID)
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = h.metadata.MarkItemOpened(r.Context(), file.DriveID, principal.UserID, itemID)
	_ = h.metadata.RecordItemRead(r.Context(), principal.UserID, itemID, "file.previewed")
	if !isHEICKey(strings.ToLower(file.Name)) {
		urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), file.Bucket, file.StorageKey, previewURLTTL, file.Name, true)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, model.PreviewResponse{Status: "ready", URL: urlValue, ExpiresIn: "15m"})
		return
	}
	preview, err := h.metadata.GetOrQueuePreview(r.Context(), file.DriveID, file.StorageKey, repository.HEICPreviewProfile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if preview.Status == "failed" {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("preview generation failed"))
		return
	}
	if preview.Status != "succeeded" || preview.ArtifactKey == "" {
		writeJSON(w, http.StatusAccepted, model.PreviewResponse{Status: preview.Status, JobID: preview.JobID, RetryAfter: 2})
		return
	}
	urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), preview.SourceBucket, preview.ArtifactKey, previewURLTTL, file.Name+".jpg", true)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.PreviewResponse{Status: "ready", URL: urlValue, ExpiresIn: "15m"})
}

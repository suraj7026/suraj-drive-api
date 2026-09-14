package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/validation"
)

func (h *FileHandler) CopyItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		Prefix         string `json:"prefix"`
		ParentID       string `json:"parent_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("idempotency_key is required"))
		return
	}
	if strings.TrimSpace(body.ParentID) == "" {
		if !h.allowLegacyPaths {
			writeError(w, http.StatusBadRequest, fmt.Errorf("parent_id is required"))
			return
		}
		if err := validation.ItemPath(strings.TrimSuffix(body.Prefix, "/"), true); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	source, err := h.metadata.ResolveAccessibleFile(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if len(source.SHA256) != 32 {
		source.SHA256, err = h.store.SHA256Object(r.Context(), source.Bucket, source.StorageKey)
		if err != nil {
			writeStorageError(w, err)
			return
		}
	}
	reservation, err := h.metadata.ReserveUpload(r.Context(), repository.ReserveUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID, Bucket: bucket,
		ParentPublicID: strings.TrimSpace(body.ParentID), Prefix: body.Prefix, Name: source.Name, SizeBytes: source.SizeBytes, MIMEType: source.MIMEType,
		IdempotencyKey: body.IdempotencyKey, TTL: 30 * time.Minute, UploadMode: "single",
		SourceVersionID: source.VersionID,
		ExpectedSHA256:  source.SHA256,
	})
	if err != nil {
		if errors.Is(err, repository.ErrQuotaExceeded) {
			writeError(w, http.StatusInsufficientStorage, err)
			return
		}
		writeError(w, http.StatusConflict, err)
		return
	}
	if reservation.Status == "completed" {
		writeJSON(w, http.StatusOK, reservation)
		return
	}
	upload, err := h.metadata.LoadUpload(r.Context(), principal.DriveID, principal.UserID, reservation.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := h.store.CopyObjectBetweenBuckets(r.Context(), source.Bucket, source.StorageKey, bucket, upload.FinalStorageKey); err != nil {
		_ = h.metadata.AbortUpload(r.Context(), principal.DriveID, principal.UserID, reservation.ID)
		writeStorageError(w, err)
		return
	}
	object, err := h.store.StatLegacyObject(r.Context(), bucket, upload.FinalStorageKey)
	if err != nil {
		_ = h.metadata.AbortUpload(r.Context(), principal.DriveID, principal.UserID, reservation.ID)
		writeStorageError(w, err)
		return
	}
	completed, err := h.metadata.CompleteUpload(r.Context(), repository.CompleteUploadInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID, UploadID: reservation.ID,
		ETag: object.ETag, SizeBytes: object.Size, MIMEType: source.MIMEType, LastModified: object.LastModified,
		SHA256: source.SHA256, RequireMalwareScan: h.malwareScanEnabled, StorageKey: upload.FinalStorageKey,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, completed)
}

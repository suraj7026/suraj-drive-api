package handler

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
	"surajdrive/backend/internal/validation"
)

type uploadReservationResponse struct {
	repository.UploadReservation
	URL           string                  `json:"url,omitempty"`
	ExpiresIn     string                  `json:"expires_in,omitempty"`
	UploadedParts []repository.UploadPart `json:"uploaded_parts,omitempty"`
}

type presignedUploadPart struct {
	Number int    `json:"part_number"`
	URL    string `json:"url"`
}

const (
	multipartUploadThreshold = 16 << 20
	multipartPartSize        = 8 << 20
	maxPresignedPartBatch    = 20
)

func (h *FileHandler) ListUploadReservations(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	uploads, err := h.metadata.ListActiveUploads(r.Context(), principal.DriveID, principal.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": uploads})
}

func (h *FileHandler) GetUploadReservation(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	reservation, err := h.metadata.GetActiveUpload(r.Context(), principal.DriveID, principal.UserID, chi.URLParam(r, "uploadID"))
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	response := uploadReservationResponse{UploadReservation: reservation.UploadReservation, UploadedParts: reservation.UploadedParts}
	if reservation.UploadMode == "multipart" && (reservation.Status == "initiated" || reservation.Status == "uploading") {
		_, parts, err := h.ensureMultipartUpload(r, principal, reservation.UploadReservation)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		response.UploadedParts = parts
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *FileHandler) ReserveUpload(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	var body struct {
		Prefix         string `json:"prefix"`
		ParentID       string `json:"parent_id"`
		Name           string `json:"name"`
		SizeBytes      int64  `json:"size_bytes"`
		MIMEType       string `json:"mime_type"`
		IdempotencyKey string `json:"idempotency_key"`
		ConflictMode   string `json:"conflict_mode"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid upload reservation"))
		return
	}
	if err := validation.ItemName(body.Name); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	if body.SizeBytes < 0 || strings.TrimSpace(body.IdempotencyKey) == "" || len(body.IdempotencyKey) > 200 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("size_bytes and idempotency_key are required"))
		return
	}
	if strings.TrimSpace(body.MIMEType) == "" {
		body.MIMEType = "application/octet-stream"
	}
	expectedSHA256, err := hex.DecodeString(strings.TrimSpace(body.ExpectedSHA256))
	if err != nil || len(expectedSHA256) != 32 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected_sha256 must be a 64-character hexadecimal SHA-256 digest"))
		return
	}

	uploadMode := "single"
	partSize := int64(0)
	if body.SizeBytes > multipartUploadThreshold {
		uploadMode = "multipart"
		partSize = adaptiveMultipartPartSize(body.SizeBytes)
	}
	reservation, err := h.metadata.ReserveUpload(r.Context(), repository.ReserveUploadInput{
		DrivePublicID:  principal.DriveID,
		UserPublicID:   principal.UserID,
		Bucket:         bucket,
		ParentPublicID: strings.TrimSpace(body.ParentID),
		Prefix:         body.Prefix,
		Name:           body.Name,
		SizeBytes:      body.SizeBytes,
		MIMEType:       body.MIMEType,
		IdempotencyKey: body.IdempotencyKey,
		TTL:            30 * time.Minute,
		UploadMode:     uploadMode,
		PartSize:       partSize,
		ConflictMode:   body.ConflictMode,
		ExpectedSHA256: expectedSHA256,
	})
	if err != nil {
		if errors.Is(err, repository.ErrQuotaExceeded) {
			writeError(w, http.StatusInsufficientStorage, err)
			return
		}
		writeError(w, http.StatusConflict, err)
		return
	}

	response := uploadReservationResponse{UploadReservation: reservation}
	if reservation.Status == "initiated" || reservation.Status == "uploading" {
		if time.Now().After(reservation.ExpiresAt) {
			writeError(w, http.StatusGone, fmt.Errorf("upload reservation expired"))
			return
		}
		if reservation.UploadMode == "multipart" {
			upload, parts, err := h.ensureMultipartUpload(r, principal, reservation)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			_ = upload
			response.UploadedParts = parts
		} else {
			response.URL, err = h.store.PresignedPutURLForSize(r.Context(), bucket, reservation.StorageKey, 15*time.Minute, reservation.ExpectedSize)
			if err != nil {
				writeStorageError(w, err)
				return
			}
			response.ExpiresIn = "15m"
		}
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *FileHandler) ensureMultipartUpload(r *http.Request, principal *auth.Principal, reservation repository.UploadReservation) (repository.StoredUpload, []repository.UploadPart, error) {
	upload, err := h.metadata.LoadUpload(r.Context(), principal.DriveID, principal.UserID, reservation.ID)
	if err != nil {
		return repository.StoredUpload{}, nil, err
	}
	if upload.MinIOUploadID == "" {
		createdID, err := h.store.NewMultipartUpload(r.Context(), upload.Bucket, upload.StorageKey, upload.MIMEType)
		if err != nil {
			return repository.StoredUpload{}, nil, err
		}
		actualID, err := h.metadata.AttachMultipartUpload(r.Context(), principal.DriveID, principal.UserID, reservation.ID, createdID)
		if err != nil {
			_ = h.store.AbortMultipartUpload(r.Context(), upload.Bucket, upload.StorageKey, createdID)
			return repository.StoredUpload{}, nil, err
		}
		if actualID != createdID {
			_ = h.store.AbortMultipartUpload(r.Context(), upload.Bucket, upload.StorageKey, createdID)
		}
		upload.MinIOUploadID = actualID
		upload.Status = "uploading"
	}
	storageParts, err := h.store.ListMultipartParts(r.Context(), upload.Bucket, upload.StorageKey, upload.MinIOUploadID)
	if err != nil {
		return repository.StoredUpload{}, nil, err
	}
	parts := make([]repository.UploadPart, 0, len(storageParts))
	for _, part := range storageParts {
		parts = append(parts, repository.UploadPart{Number: part.Number, ETag: part.ETag, Size: part.Size})
	}
	if err := h.metadata.ReplaceUploadParts(r.Context(), principal.DriveID, principal.UserID, reservation.ID, parts); err != nil {
		return repository.StoredUpload{}, nil, err
	}
	return upload, parts, nil
}

func (h *FileHandler) PresignUploadParts(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	var body struct {
		PartNumbers []int `json:"part_numbers"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || len(body.PartNumbers) == 0 || len(body.PartNumbers) > maxPresignedPartBatch {
		writeError(w, http.StatusBadRequest, fmt.Errorf("part_numbers must contain 1 to %d entries", maxPresignedPartBatch))
		return
	}
	if err := h.metadata.RenewUpload(r.Context(), principal.DriveID, principal.UserID, uploadID, 45*time.Minute); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	upload, err := h.metadata.LoadUpload(r.Context(), principal.DriveID, principal.UserID, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if upload.UploadMode != "multipart" || upload.MinIOUploadID == "" || (upload.Status != "initiated" && upload.Status != "uploading") {
		writeError(w, http.StatusConflict, repository.ErrUploadState)
		return
	}
	if time.Now().After(upload.ExpiresAt) {
		writeError(w, http.StatusGone, fmt.Errorf("upload reservation expired"))
		return
	}
	partCount := int((upload.ExpectedSize + upload.PartSize - 1) / upload.PartSize)
	seen := make(map[int]struct{}, len(body.PartNumbers))
	parts := make([]presignedUploadPart, 0, len(body.PartNumbers))
	for _, partNumber := range body.PartNumbers {
		if partNumber < 1 || partNumber > partCount {
			writeError(w, http.StatusBadRequest, fmt.Errorf("part number %d is out of range", partNumber))
			return
		}
		if _, duplicate := seen[partNumber]; duplicate {
			writeError(w, http.StatusBadRequest, fmt.Errorf("part numbers must be unique"))
			return
		}
		seen[partNumber] = struct{}{}
		expectedPartSize := upload.PartSize
		if partNumber == partCount {
			expectedPartSize = upload.ExpectedSize - int64(partNumber-1)*upload.PartSize
		}
		partURL, err := h.store.PresignedUploadPartURLForSize(r.Context(), upload.Bucket, upload.StorageKey, upload.MinIOUploadID, partNumber, 30*time.Minute, expectedPartSize)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		parts = append(parts, presignedUploadPart{Number: partNumber, URL: partURL})
	}
	writeJSON(w, http.StatusOK, map[string]any{"parts": parts, "expires_in": "30m"})
}

func (h *FileHandler) CompleteUploadReservation(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	if uploadID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("upload id is required"))
		return
	}
	var body struct {
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected_sha256 is required"))
		return
	}
	expectedSHA256, decodeErr := hex.DecodeString(strings.TrimSpace(body.ExpectedSHA256))
	if decodeErr != nil || len(expectedSHA256) != 32 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected_sha256 must be a 64-character hexadecimal SHA-256 digest"))
		return
	}
	reservation, err := h.metadata.QueueUploadCompletion(r.Context(), principal.DriveID, principal.UserID, uploadID, expectedSHA256)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrUploadNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrUploadSizeMismatch):
			writeError(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, repository.ErrUploadIntegrity):
			writeError(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, repository.ErrQuotaExceeded):
			writeError(w, http.StatusInsufficientStorage, err)
		case errors.Is(err, repository.ErrUploadState):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	status := http.StatusAccepted
	if reservation.Status == "completed" {
		status = http.StatusOK
	}
	writeJSON(w, status, reservation)
}

func adaptiveMultipartPartSize(size int64) int64 {
	partSize := int64(multipartPartSize)
	minimum := (size + 9_999) / 10_000
	const alignment = int64(1 << 20)
	if minimum > partSize {
		partSize = ((minimum + alignment - 1) / alignment) * alignment
	}
	return partSize
}

func (h *FileHandler) AbortUploadReservation(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	upload, err := h.metadata.LoadUpload(r.Context(), principal.DriveID, principal.UserID, uploadID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if upload.Status == "completed" {
		writeError(w, http.StatusConflict, repository.ErrUploadState)
		return
	}
	if upload.UploadMode == "multipart" && upload.MinIOUploadID != "" && (upload.Status == "initiated" || upload.Status == "uploading" || upload.Status == "completing") {
		if err := h.store.AbortMultipartUpload(r.Context(), upload.Bucket, upload.StorageKey, upload.MinIOUploadID); err != nil && !storage.IsMultipartUploadNotFound(err) {
			writeStorageError(w, err)
			return
		}
	}
	if err := h.metadata.AbortUpload(r.Context(), principal.DriveID, principal.UserID, uploadID); err != nil {
		if errors.Is(err, repository.ErrUploadNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, repository.ErrUploadState) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListFileVersions(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	versions, err := h.metadata.ListFileVersions(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (h *FileHandler) DownloadFileVersion(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	version, err := h.metadata.ResolveAccessibleVersion(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "versionID"))
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), version.Bucket, version.StorageKey, 15*time.Minute, version.Name, false)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.PresignResponse{URL: urlValue, ExpiresIn: "15m"})
}

func (h *FileHandler) SetVersionRetention(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		KeepForever    *bool  `json:"keep_forever"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.KeepForever == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("keep_forever must be a boolean"))
		return
	}
	err := h.metadata.SetVersionKeepForeverIdempotent(
		r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "versionID"), *body.KeepForever, body.IdempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) RestoreFileVersion(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("idempotency_key is required"))
		return
	}
	err := h.metadata.RestoreFileVersionIdempotent(
		r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "versionID"), body.IdempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

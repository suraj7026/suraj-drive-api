package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/validation"
)

func (h *FileHandler) CreateShortcut(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		ParentID       string `json:"parent_id"`
		Name           string `json:"name"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid shortcut request"))
		return
	}
	shortcut, err := h.metadata.CreateShortcut(r.Context(), repository.CreateShortcutInput{
		UserPublicID: principal.UserID, TargetPublicID: chi.URLParam(r, "itemID"),
		ParentPublicID: body.ParentID, Name: body.Name, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrNameConflict), errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrInvalidMove):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate), errors.Is(err, validation.ErrInvalidItemName):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, shortcut)
}

func (h *FileHandler) ResolveShortcut(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	resolution, err := h.metadata.ResolveShortcut(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, resolution)
}

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

func (h *FileHandler) GetItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	item, err := h.metadata.GetAccessibleItem(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *FileHandler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	var body struct {
		Name           *string `json:"name"`
		ParentID       *string `json:"parent_id"`
		Description    *string `json:"description"`
		FolderColor    *string `json:"folder_color"`
		IdempotencyKey string  `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid item update"))
		return
	}
	item, err := h.metadata.UpdateItem(r.Context(), repository.UpdateItemInput{
		DrivePublicID: principal.DriveID, UserPublicID: principal.UserID,
		ItemPublicID: chi.URLParam(r, "itemID"), Name: body.Name,
		ParentPublicID: body.ParentID, Description: body.Description, FolderColor: body.FolderColor, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrInvalidMove):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrNameConflict), errors.Is(err, repository.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate), errors.Is(err, validation.ErrInvalidItemName):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, item)
}

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/validation"
)

func (h *FileHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	var body struct {
		Prefix         string `json:"prefix"`
		ParentID       string `json:"parent_id"`
		Name           string `json:"name"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}

	folderName := strings.Trim(body.Name, "/ ")
	if err := validation.ItemName(folderName); err != nil {
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

	fullPrefix := folderName
	if strings.TrimSpace(body.Prefix) != "" {
		fullPrefix = path.Join(body.Prefix, folderName)
	}
	fullPrefix += "/"

	var folder repository.ItemDetails
	var err error
	if strings.TrimSpace(body.ParentID) != "" {
		folder, err = h.metadata.EnsureFolderByIDIdempotent(
			r.Context(), principal.DriveID, principal.UserID, strings.TrimSpace(body.ParentID), folderName, body.IdempotencyKey,
		)
	} else {
		folder, err = h.metadata.EnsureFolder(r.Context(), principal.DriveID, principal.UserID, strings.TrimSuffix(body.Prefix, "/"), folderName)
	}
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNameConflict), errors.Is(err, repository.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate), errors.Is(err, validation.ErrInvalidItemName):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": folder.ID, "prefix": fullPrefix})
}

func (h *FileHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("prefix is required"))
		return
	}

	itemID, err := h.metadata.TrashFolderByPrefix(r.Context(), principal.DriveID, principal.UserID, prefix)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"trashed": itemID})
}

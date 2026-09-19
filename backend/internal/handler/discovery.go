package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListUserView(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	view := chi.URLParam(r, "view")
	if view != repository.UserViewRecent && view != repository.UserViewStarred && view != repository.UserViewStorage {
		writeError(w, http.StatusNotFound, repository.ErrItemNotFound)
		return
	}
	_, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope := "drive-user-view\x00" + principal.DriveID + "\x00" + principal.UserID + "\x00" + view
	var after *pagination.Position
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		position, err := h.cursors.Decode(rawCursor, scope)
		if err != nil {
			if errors.Is(err, pagination.ErrInvalidCursor) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("cursor is invalid or expired"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		after = &position
	}
	response, next, err := h.metadata.ListUserViewCursor(r.Context(), principal.DriveID, principal.UserID, view, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if next != nil {
		response.Pagination.NextCursor, err = h.cursors.Encode(scope, *next)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *FileHandler) SetItemStarred(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	var body struct {
		Starred        *bool  `json:"starred"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Starred == nil || strings.TrimSpace(body.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("starred and idempotency_key are required"))
		return
	}
	itemID := chi.URLParam(r, "itemID")
	if err := h.metadata.SetItemStarredIdempotent(r.Context(), principal.UserID, itemID, *body.Starred, body.IdempotencyKey); err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, repository.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if errors.Is(err, repository.ErrPermissionDenied) {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": itemID, "starred": *body.Starred})
}

func (h *FileHandler) MarkItemOpened(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	itemID := chi.URLParam(r, "itemID")
	if err := h.metadata.MarkItemOpened(r.Context(), principal.DriveID, principal.UserID, itemID); err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) StorageSummary(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	summary, err := h.metadata.GetStorageSummary(r.Context(), principal.DriveID, principal.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

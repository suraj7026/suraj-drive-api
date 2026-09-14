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

func (h *FileHandler) ListTrash(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	_, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope := "drive-trash\x00" + principal.DriveID
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
	response, next, err := h.metadata.ListTrashCursor(r.Context(), principal.DriveID, after, limit)
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

func (h *FileHandler) TrashItem(w http.ResponseWriter, r *http.Request) {
	h.setItemTrashState(w, r, true)
}

func (h *FileHandler) RestoreItem(w http.ResponseWriter, r *http.Request) {
	h.setItemTrashState(w, r, false)
}

func (h *FileHandler) PermanentlyDeleteItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("idempotency_key is required"))
		return
	}
	err := h.metadata.QueueItemForPermanentDeletionIdempotent(
		r.Context(), principal.DriveID, principal.UserID, chi.URLParam(r, "itemID"), body.IdempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRecentAuthenticationRequired):
			writeError(w, http.StatusUnauthorized, err)
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrDeletionStarted):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	if _, err := h.metadata.QueueExpiredTrash(r.Context(), 200); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": chi.URLParam(r, "itemID"), "status": "deletion_queued"})
}

func (h *FileHandler) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("idempotency_key is required"))
		return
	}
	count, err := h.metadata.QueueAllTrashForPermanentDeletionIdempotent(
		r.Context(), principal.DriveID, principal.UserID, body.IdempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRecentAuthenticationRequired):
			writeError(w, http.StatusUnauthorized, err)
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	if _, err := h.metadata.QueueExpiredTrash(r.Context(), 1000); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "deletion_queued", "items": count})
}

func (h *FileHandler) setItemTrashState(w http.ResponseWriter, r *http.Request, trashed bool) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	itemID := chi.URLParam(r, "itemID")
	if itemID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("item id is required"))
		return
	}
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("idempotency_key is required"))
		return
	}
	if err := h.metadata.SetItemTrashedIdempotent(r.Context(), principal.DriveID, principal.UserID, itemID, trashed, body.IdempotencyKey); err != nil {
		switch {
		case errors.Is(err, repository.ErrItemNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, repository.ErrPermissionDenied):
			writeError(w, http.StatusForbidden, err)
		case errors.Is(err, repository.ErrIdempotencyConflict), errors.Is(err, repository.ErrDeletionStarted):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, repository.ErrInvalidItemUpdate):
			writeError(w, http.StatusBadRequest, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	state := "restored"
	if trashed {
		state = "trashed"
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": itemID, "status": state})
}

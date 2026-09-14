package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListItemActivity(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	_, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	itemID := chi.URLParam(r, "itemID")
	scope := "item-activity\x00" + principal.UserID + "\x00" + itemID
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
	events, next, err := h.metadata.ListItemActivity(r.Context(), principal.UserID, itemID, after, limit)
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paginationResponse := model.Pagination{Limit: limit, Returned: len(events), HasMore: next != nil}
	if next != nil {
		paginationResponse.NextCursor, err = h.cursors.Encode(scope, *next)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "pagination": paginationResponse})
}

package handler

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
)

func (h *FileHandler) TrashItem(w http.ResponseWriter, r *http.Request) {
	h.setItemTrashState(w, r, true)
}

func (h *FileHandler) RestoreItem(w http.ResponseWriter, r *http.Request) {
	h.setItemTrashState(w, r, false)
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
	if err := h.metadata.SetItemTrashed(r.Context(), principal.DriveID, principal.UserID, itemID, trashed); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	state := "restored"
	if trashed {
		state = "trashed"
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": itemID, "status": state})
}

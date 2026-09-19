package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) GetUserPreferences(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	preferences, err := h.metadata.GetUserPreferences(r.Context(), principal.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

func (h *FileHandler) UpdateUserPreferences(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}
	var body struct {
		ViewMode *string `json:"view_mode"`
		Density  *string `json:"density"`
		SortKey  *string `json:"sort_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid user preferences"))
		return
	}
	preferences, err := h.metadata.UpdateUserPreferences(r.Context(), repository.UpdateUserPreferencesInput{
		UserPublicID: principal.UserID, ViewMode: body.ViewMode, Density: body.Density, SortKey: body.SortKey,
	})
	if err != nil {
		if errors.Is(err, repository.ErrInvalidPreference) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, preferences)
}

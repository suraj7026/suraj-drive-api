package handler

import (
	"fmt"
	"net/http"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/importer"
)

type MetadataHandler struct {
	legacy *importer.Legacy
}

func NewMetadataHandler(legacy *importer.Legacy) *MetadataHandler {
	return &MetadataHandler{legacy: legacy}
}

func (h *MetadataHandler) ReconcileLegacy(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	stats, err := h.legacy.ReconcileDrive(r.Context(), principal)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

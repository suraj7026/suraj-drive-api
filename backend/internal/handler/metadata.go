package handler

import (
	"errors"
	"fmt"
	"net/http"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/importer"
	"surajdrive/backend/internal/reconcile"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

type MetadataHandler struct {
	legacy   *importer.Legacy
	metadata *repository.Metadata
	store    *storage.MinIOClient
}

func NewMetadataHandler(legacy *importer.Legacy, metadata *repository.Metadata, store *storage.MinIOClient) *MetadataHandler {
	return &MetadataHandler{legacy: legacy, metadata: metadata, store: store}
}

func (h *MetadataHandler) AuditConsistency(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	expected, err := h.metadata.ListExpectedBlobs(r.Context(), principal.DriveID, principal.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrPermissionDenied) {
			writeError(w, http.StatusForbidden, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	actual, err := h.store.ListAuditObjects(r.Context(), principal.StorageBucket)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	report := reconcile.Compare(expected, actual)
	if err := h.metadata.RecordConsistencyAudit(r.Context(), principal.DriveID, principal.UserID, report.ExpectedObjects, report.ActualObjects, report.IssueCount); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
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

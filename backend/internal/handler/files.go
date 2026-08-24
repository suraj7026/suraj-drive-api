package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

type FileHandler struct {
	store    *storage.MinIOClient
	metadata *repository.Metadata
}

func NewFileHandler(store *storage.MinIOClient, metadata *repository.Metadata) *FileHandler {
	return &FileHandler{store: store, metadata: metadata}
}

func (h *FileHandler) List(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	response, err := h.metadata.ListDrive(r.Context(), principal.DriveID, r.URL.Query().Get("prefix"), offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}

	itemID, err := h.metadata.TrashFileByStorageKey(r.Context(), principal.DriveID, principal.UserID, key)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"trashed": itemID})
}

func (h *FileHandler) Copy(w http.ResponseWriter, r *http.Request) {
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	var body struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Src == "" || body.Dst == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("src and dst are required"))
		return
	}

	resolvedDst, err := h.store.CopyObject(r.Context(), bucket, body.Src, body.Dst)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if err := h.recordObjectMetadata(r, resolvedDst); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"src": body.Src, "dst": resolvedDst})
}

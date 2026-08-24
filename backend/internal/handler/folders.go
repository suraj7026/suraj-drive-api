package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"surajdrive/backend/internal/auth"
)

func (h *FileHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	var body struct {
		Prefix string `json:"prefix"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body"))
		return
	}

	folderName := strings.Trim(body.Name, "/ ")
	if folderName == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}

	fullPrefix := folderName
	if strings.TrimSpace(body.Prefix) != "" {
		fullPrefix = path.Join(body.Prefix, folderName)
	}
	fullPrefix += "/"

	if err := h.store.CreateFolder(r.Context(), bucket, fullPrefix); err != nil {
		writeStorageError(w, err)
		return
	}
	if err := h.recordObjectMetadata(r, fullPrefix+".keep"); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"prefix": fullPrefix})
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

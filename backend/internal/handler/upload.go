package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/validation"
)

const maxServerSideUpload = 10 << 20

func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxServerSideUpload+(1<<20))
	if err := r.ParseMultipartForm(maxServerSideUpload); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("file exceeds %d MB; use presigned upload instead", maxServerSideUpload>>20))
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid multipart upload: %w", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("file field missing"))
		return
	}
	defer file.Close()

	fileName := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if err := validation.ItemName(fileName); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	prefix := r.FormValue("prefix")
	if err := validation.ItemPath(strings.TrimSuffix(prefix, "/"), true); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	requestedKey := fileName
	if strings.TrimSpace(prefix) != "" {
		requestedKey = path.Join(prefix, fileName)
	}

	resolvedKey, err := h.store.ResolveAvailableKey(r.Context(), bucket, requestedKey)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	if err := h.store.PutObject(r.Context(), bucket, resolvedKey, contentType, file, header.Size); err != nil {
		writeStorageError(w, err)
		return
	}
	if err := h.recordObjectMetadata(r, resolvedKey); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"key": resolvedKey})
}

func (h *FileHandler) CompletePresignedUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := validation.ItemPath(body.Key, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.recordObjectMetadata(r, body.Key); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": body.Key, "status": "ready"})
}

func (h *FileHandler) PresignUpload(w http.ResponseWriter, r *http.Request) {
	bucket, err := bucketFromRequest(r, h.store)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := validation.ItemPath(key, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	resolvedKey, err := h.store.ResolveAvailableKey(r.Context(), bucket, key)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	urlValue, err := h.store.PresignedPutURL(r.Context(), bucket, resolvedKey, 15*time.Minute)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, model.PresignResponse{URL: urlValue, Key: resolvedKey, ExpiresIn: "15m"})
}

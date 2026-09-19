package handler

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"time"

	"surajdrive/backend/internal/model"
)

func (h *FileHandler) PresignDownload(w http.ResponseWriter, r *http.Request) {
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

	ttlMinutes := 15
	if rawTTL := r.URL.Query().Get("ttl"); rawTTL != "" {
		parsed, err := strconv.Atoi(rawTTL)
		if err == nil && parsed >= 1 && parsed <= 1440 {
			ttlMinutes = parsed
		}
	}

	urlValue, err := h.store.PresignedGetURLWithDisposition(r.Context(), bucket, key, time.Duration(ttlMinutes)*time.Minute, path.Base(key), false)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, model.PresignResponse{URL: urlValue, Key: key, ExpiresIn: fmt.Sprintf("%dm", ttlMinutes)})
}

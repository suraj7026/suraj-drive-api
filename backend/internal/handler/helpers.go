package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/storage"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, storage.ErrVersionLimitReached):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, storage.ErrObjectNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func parsePagination(r *http.Request) (int, int, error) {
	offset := 0
	limit := defaultPageLimit

	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = parsed
	}

	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			return 0, 0, fmt.Errorf("limit must be greater than 0")
		}
		if parsed > maxPageLimit {
			parsed = maxPageLimit
		}
		limit = parsed
	}

	return offset, limit, nil
}

func bucketFromRequest(r *http.Request, store *storage.MinIOClient) (string, error) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return "", fmt.Errorf("missing auth claims")
	}
	bucket, err := store.BucketNameForSubject(claims.Subject)
	if err != nil {
		return "", err
	}
	if err := store.EnsureBucket(r.Context(), bucket); err != nil {
		return "", fmt.Errorf("failed to provision user bucket: %w", err)
	}
	return bucket, nil
}

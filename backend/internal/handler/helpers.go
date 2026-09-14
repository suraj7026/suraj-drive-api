package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

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
	message := err.Error()
	code := errorCodeForStatus(status)
	if status >= http.StatusInternalServerError {
		log.Error().Err(err).Int("status", status).Msg("request failed")
		message = "internal server error"
		code = "internal_error"
		if status == http.StatusServiceUnavailable {
			message = "service temporarily unavailable"
			code = "service_unavailable"
		}
	}
	payload := map[string]string{"error": message, "message": message, "code": code}
	if requestID := w.Header().Get("X-Request-ID"); requestID != "" {
		payload["request_id"] = requestID
	}
	writeJSON(w, status, payload)
}

func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusInsufficientStorage:
		return "insufficient_storage"
	default:
		return "request_failed"
	}
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
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil || principal.StorageBucket == "" {
		return "", fmt.Errorf("missing authenticated drive")
	}
	bucket := principal.StorageBucket
	if err := store.EnsureBucket(r.Context(), bucket); err != nil {
		return "", fmt.Errorf("failed to provision user bucket: %w", err)
	}
	return bucket, nil
}

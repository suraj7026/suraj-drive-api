package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/ratelimit"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
	"surajdrive/backend/internal/validation"
)

type FileHandler struct {
	store              *storage.MinIOClient
	metadata           *repository.Metadata
	cursors            *pagination.Codec
	malwareScanEnabled bool
	allowLegacyPaths   bool
	publicURL          string
	publicLinkLimiter  *ratelimit.FixedWindow
	clientIPKey        func(*http.Request) string
}

func NewFileHandler(store *storage.MinIOClient, metadata *repository.Metadata, cursors *pagination.Codec, malwareScanEnabled, allowLegacyPaths bool, publicURL string, clientIPKey func(*http.Request) string) *FileHandler {
	return &FileHandler{
		store: store, metadata: metadata, cursors: cursors, malwareScanEnabled: malwareScanEnabled,
		allowLegacyPaths: allowLegacyPaths, publicURL: strings.TrimRight(publicURL, "/"),
		publicLinkLimiter: ratelimit.NewFixedWindow(120, time.Minute, 10_000),
		clientIPKey:       clientIPKey,
	}
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

	prefix := r.URL.Query().Get("prefix")
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor != "" && r.URL.Query().Has("offset") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("cursor and offset cannot be combined"))
		return
	}

	// Explicit offset requests remain available for one compatibility release.
	if r.URL.Query().Has("offset") {
		response, err := h.metadata.ListDrive(r.Context(), principal.DriveID, prefix, offset, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	scope := "drive-list\x00" + principal.DriveID + "\x00" + prefix
	var after *pagination.Position
	if rawCursor != "" {
		position, err := h.cursors.Decode(rawCursor, scope)
		if err != nil {
			if errors.Is(err, pagination.ErrInvalidCursor) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("cursor is invalid or expired"))
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		after = &position
	}

	response, next, err := h.metadata.ListDriveCursor(r.Context(), principal.DriveID, prefix, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if next != nil {
		response.Pagination.NextCursor, err = h.cursors.Encode(scope, *next)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
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
	if err := validation.ItemPath(body.Src, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validation.ItemPath(body.Dst, false); err != nil {
		writeError(w, http.StatusBadRequest, err)
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

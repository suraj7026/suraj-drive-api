package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) Search(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated drive"))
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("q is required"))
		return
	}

	offset, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	filters, err := parseSearchFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if rawCursor != "" && r.URL.Query().Has("offset") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("cursor and offset cannot be combined"))
		return
	}
	if r.URL.Query().Has("offset") {
		response, err := h.metadata.SearchDrive(r.Context(), principal.DriveID, prefix, query, offset, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	scope := "drive-search\x00" + principal.DriveID + "\x00" + principal.UserID + "\x00" + prefix + "\x00" + query + "\x00" + searchFilterScope(filters)
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
	var response model.SearchResponse
	var next *pagination.Position
	if strings.TrimSpace(prefix) == "" {
		response, next, err = h.metadata.AdvancedSearchAccessibleCursor(r.Context(), principal.UserID, query, filters, after, limit)
	} else {
		response, next, err = h.metadata.SearchDriveCursor(r.Context(), principal.DriveID, prefix, query, after, limit)
	}
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

func parseSearchFilters(r *http.Request) (repository.SearchFilters, error) {
	query := r.URL.Query()
	filters := repository.SearchFilters{
		Type:           strings.ToLower(strings.TrimSpace(query.Get("type"))),
		Owner:          strings.TrimSpace(query.Get("owner")),
		LocationItemID: strings.TrimSpace(query.Get("location")),
		Shared:         strings.ToLower(strings.TrimSpace(query.Get("shared"))),
		Starred:        strings.ToLower(strings.TrimSpace(query.Get("starred"))),
		Trash:          strings.ToLower(strings.TrimSpace(query.Get("trash"))),
	}
	if filters.Type != "" {
		allowed := map[string]bool{"folder": true, "file": true, "image": true, "video": true, "audio": true, "pdf": true, "archive": true}
		if !allowed[filters.Type] {
			return repository.SearchFilters{}, fmt.Errorf("unsupported type filter")
		}
	}
	for name, value := range map[string]string{"shared": filters.Shared, "starred": filters.Starred} {
		if value != "" && value != "all" && value != "yes" && value != "no" {
			return repository.SearchFilters{}, fmt.Errorf("unsupported %s filter", name)
		}
	}
	if filters.Trash != "" && filters.Trash != "exclude" && filters.Trash != "only" && filters.Trash != "all" {
		return repository.SearchFilters{}, fmt.Errorf("unsupported trash filter")
	}
	if filters.LocationItemID != "" {
		if _, err := uuid.Parse(filters.LocationItemID); err != nil {
			return repository.SearchFilters{}, fmt.Errorf("location must be a valid item id")
		}
	}
	var err error
	if filters.ModifiedAfter, err = parseOptionalSearchTime(query.Get("modified_after")); err != nil {
		return repository.SearchFilters{}, fmt.Errorf("modified_after must be RFC3339")
	}
	if filters.ModifiedBefore, err = parseOptionalSearchTime(query.Get("modified_before")); err != nil {
		return repository.SearchFilters{}, fmt.Errorf("modified_before must be RFC3339")
	}
	return filters, nil
}

func parseOptionalSearchTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func searchFilterScope(filters repository.SearchFilters) string {
	after, before := "", ""
	if filters.ModifiedAfter != nil {
		after = filters.ModifiedAfter.UTC().Format(time.RFC3339Nano)
	}
	if filters.ModifiedBefore != nil {
		before = filters.ModifiedBefore.UTC().Format(time.RFC3339Nano)
	}
	return strings.Join([]string{
		filters.Type, strings.ToLower(filters.Owner), filters.LocationItemID,
		filters.Shared, filters.Starred, filters.Trash, after, before,
	}, "\x00")
}

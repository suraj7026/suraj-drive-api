package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/pagination"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListSharedWithMe(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	_, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope := "shared-with-me\x00" + principal.UserID
	var after *pagination.Position
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		position, err := h.cursors.Decode(rawCursor, scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("cursor is invalid or expired"))
			return
		}
		after = &position
	}
	response, next, err := h.metadata.ListSharedWithMeCursor(r.Context(), principal.UserID, after, limit)
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

func (h *FileHandler) ListAccessibleChildren(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	_, limit, err := parsePagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	itemID := chi.URLParam(r, "itemID")
	scope := "shared-children\x00" + principal.UserID + "\x00" + itemID
	var after *pagination.Position
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		position, err := h.cursors.Decode(rawCursor, scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("cursor is invalid or expired"))
			return
		}
		after = &position
	}
	response, next, err := h.metadata.ListAccessibleChildrenCursor(r.Context(), principal.UserID, itemID, after, limit)
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
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

func (h *FileHandler) ListItemPermissions(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	permissions, err := h.metadata.ListItemPermissions(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		writeSharingError(w, err)
		return
	}
	invitations, err := h.metadata.ListShareInvitations(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		writeSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": permissions, "invitations": invitations})
}

func (h *FileHandler) GrantItemPermission(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		Email     string     `json:"email"`
		Role      string     `json:"role"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("email and role are required"))
		return
	}
	permission, err := h.metadata.GrantItemPermission(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), body.Email, body.Role, body.ExpiresAt)
	if errors.Is(err, repository.ErrInviteeNotFound) {
		invitation, inviteErr := h.metadata.CreateShareInvitation(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), body.Email, body.Role)
		if inviteErr != nil {
			writeSharingError(w, inviteErr)
			return
		}
		writeJSON(w, http.StatusAccepted, invitation)
		return
	}
	if err != nil {
		writeSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, permission)
}

func (h *FileHandler) RevokeShareInvitation(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	if err := h.metadata.RevokeShareInvitation(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "invitationID")); err != nil {
		writeSharingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) ResendShareInvitation(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	invitation, err := h.metadata.ResendShareInvitation(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "invitationID"))
	if err != nil {
		writeSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, invitation)
}

func (h *FileHandler) RevokeItemPermission(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	err := h.metadata.RevokeItemPermission(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "permissionID"))
	if err != nil {
		writeSharingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeSharingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrPermissionDenied):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, repository.ErrInviteeNotFound), errors.Is(err, repository.ErrItemNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

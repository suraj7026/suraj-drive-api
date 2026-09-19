package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListComments(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	comments, err := h.metadata.ListComments(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": comments})
}

func (h *FileHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		ParentID string `json:"parent_id"`
		Body     string `json:"body"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid comment"))
		return
	}
	comment, err := h.metadata.CreateComment(
		r.Context(), principal.UserID, chi.URLParam(r, "itemID"), strings.TrimSpace(body.ParentID), body.Body,
	)
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, comment)
}

func (h *FileHandler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid comment update"))
		return
	}
	comment, err := h.metadata.UpdateComment(
		r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "commentID"), body.Body,
	)
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (h *FileHandler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	if err := h.metadata.DeleteComment(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "commentID")); err != nil {
		writeCommentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) ResolveComment(w http.ResponseWriter, r *http.Request) {
	h.setCommentResolved(w, r, true)
}

func (h *FileHandler) ReopenComment(w http.ResponseWriter, r *http.Request) {
	h.setCommentResolved(w, r, false)
}

func (h *FileHandler) setCommentResolved(w http.ResponseWriter, r *http.Request, resolved bool) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	comment, err := h.metadata.SetCommentResolved(
		r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "commentID"), resolved,
	)
	if err != nil {
		writeCommentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func writeCommentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrItemNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, repository.ErrPermissionDenied):
		writeError(w, http.StatusForbidden, err)
	case strings.Contains(err.Error(), "comment body"):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

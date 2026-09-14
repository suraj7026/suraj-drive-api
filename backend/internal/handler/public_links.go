package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/model"
	"surajdrive/backend/internal/repository"
)

func (h *FileHandler) ListShareLinks(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	links, err := h.metadata.ListShareLinks(r.Context(), principal.UserID, chi.URLParam(r, "itemID"))
	if err != nil {
		writeSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (h *FileHandler) CreateShareLink(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	var body struct {
		Role          string     `json:"role"`
		AllowDownload bool       `json:"allow_download"`
		ExpiresAt     *time.Time `json:"expires_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid share link"))
		return
	}
	link, token, err := h.metadata.CreateShareLink(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), body.Role, body.AllowDownload, body.ExpiresAt)
	if err != nil {
		if strings.Contains(err.Error(), "expiry") {
			writeError(w, http.StatusBadRequest, err)
		} else {
			writeSharingError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": link.ID, "role": link.Role, "allow_download": link.AllowDownload,
		"expires_at": link.ExpiresAt, "created_at": link.CreatedAt,
		"url": h.publicURL + "/shared-link/" + url.PathEscape(token),
	})
}

func (h *FileHandler) RevokeShareLink(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	if err := h.metadata.RevokeShareLink(r.Context(), principal.UserID, chi.URLParam(r, "itemID"), chi.URLParam(r, "linkID")); err != nil {
		writeSharingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) allowPublicLinkRequest(w http.ResponseWriter, r *http.Request) bool {
	host := h.clientIPKey(r)
	digest := sha256.Sum256([]byte(chi.URLParam(r, "token")))
	key := host + ":" + hex.EncodeToString(digest[:8])
	if !h.publicLinkLimiter.Allow(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("too many public link requests"))
		return false
	}
	return true
}

func (h *FileHandler) GetPublicLink(w http.ResponseWriter, r *http.Request) {
	if !h.allowPublicLinkRequest(w, r) {
		return
	}
	item, err := h.metadata.ResolvePublicLink(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writePublicLinkError(w, err)
		return
	}
	response := map[string]any{
		"id": item.ItemID, "kind": item.Kind, "name": item.Name,
		"mime_type": item.MIMEType, "size_bytes": item.SizeBytes, "allow_download": item.AllowDownload,
	}
	if item.Kind == "file" {
		previewURL, err := h.store.PresignedGetURLWithDisposition(r.Context(), item.Bucket, item.StorageKey, 15*time.Minute, item.Name, true)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		response["preview_url"] = previewURL
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *FileHandler) ListPublicLinkChildren(w http.ResponseWriter, r *http.Request) {
	if !h.allowPublicLinkRequest(w, r) {
		return
	}
	_, listing, err := h.metadata.ListPublicLinkChildren(r.Context(), chi.URLParam(r, "token"), strings.TrimSpace(r.URL.Query().Get("parent_id")))
	if err != nil {
		writePublicLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (h *FileHandler) PreviewPublicLinkItem(w http.ResponseWriter, r *http.Request) {
	h.presignPublicLinkItem(w, r, true)
}

func (h *FileHandler) DownloadPublicLinkItem(w http.ResponseWriter, r *http.Request) {
	h.presignPublicLinkItem(w, r, false)
}

func (h *FileHandler) presignPublicLinkItem(w http.ResponseWriter, r *http.Request, inline bool) {
	if !h.allowPublicLinkRequest(w, r) {
		return
	}
	item, err := h.metadata.ResolvePublicLinkFile(r.Context(), chi.URLParam(r, "token"), chi.URLParam(r, "publicItemID"))
	if err != nil {
		writePublicLinkError(w, err)
		return
	}
	if !inline && !item.AllowDownload {
		writeError(w, http.StatusForbidden, repository.ErrPermissionDenied)
		return
	}
	presignedURL, err := h.store.PresignedGetURLWithDisposition(r.Context(), item.Bucket, item.StorageKey, 15*time.Minute, item.Name, inline)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, model.PresignResponse{URL: presignedURL, ExpiresIn: "15m"})
}

func writePublicLinkError(w http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrItemNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

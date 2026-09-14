package handler

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/config"
	"surajdrive/backend/internal/importer"
	"surajdrive/backend/internal/repository"
	"surajdrive/backend/internal/storage"
)

type AuthHandler struct {
	cfg         *config.Config
	oauthConfig *oauth2.Config
	store       *storage.MinIOClient
	metadata    *repository.Metadata
	legacy      *importer.Legacy
}

func NewAuthHandler(cfg *config.Config, store *storage.MinIOClient, metadata *repository.Metadata, legacy *importer.Legacy) *AuthHandler {
	return &AuthHandler{
		cfg:      cfg,
		store:    store,
		metadata: metadata,
		legacy:   legacy,
		oauthConfig: auth.NewGoogleOAuthConfig(
			cfg.Google.ClientID,
			cfg.Google.ClientSecret,
			cfg.Google.RedirectURL,
		),
	}
}

func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := auth.GenerateStateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("state generation failed: %w", err))
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Expires:  time.Now().Add(10 * time.Minute),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.Server.IsProd,
		Path:     "/",
	})

	http.Redirect(w, r, h.oauthConfig.AuthCodeURL(state), http.StatusTemporaryRedirect)
}

func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid state"))
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.Server.IsProd,
	})

	userInfo, err := auth.GetUserInfo(r.Context(), h.oauthConfig, r.URL.Query().Get("code"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("google auth failed: %w", err))
		return
	}

	if allowedDomain := strings.TrimSpace(h.cfg.Google.AllowedDomain); allowedDomain != "" && !strings.HasSuffix(strings.ToLower(userInfo.Email), "@"+strings.ToLower(allowedDomain)) {
		writeError(w, http.StatusForbidden, fmt.Errorf("email domain not permitted"))
		return
	}

	bucket, err := h.store.BucketNameForSubject(userInfo.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := h.store.EnsureBucket(r.Context(), bucket); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("failed to provision user bucket: %w", err))
		return
	}

	principal, err := h.metadata.ProvisionGoogleAccount(r.Context(), repository.GoogleAccount{
		Subject:       userInfo.ID,
		Email:         userInfo.Email,
		EmailVerified: userInfo.VerifiedEmail,
		Name:          userInfo.Name,
		Picture:       userInfo.Picture,
		StorageBucket: bucket,
	})
	if err != nil {
		if errors.Is(err, repository.ErrAccountUnavailable) {
			writeError(w, http.StatusForbidden, repository.ErrAccountUnavailable)
			return
		}
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("failed to provision account metadata: %w", err))
		return
	}
	if _, err := h.legacy.ReconcileDrive(r.Context(), principal); err != nil {
		log.Warn().Err(err).Str("user_id", principal.UserID).Msg("legacy metadata reconciliation did not complete during login")
	}

	sessionTTL := time.Duration(h.cfg.JWT.ExpiryHrs) * time.Hour
	sessionToken, expiresAt, err := h.metadata.CreateSession(
		r.Context(),
		principal.UserID,
		r.UserAgent(),
		requestIP(r),
		sessionTTL,
	)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("failed to create session: %w", err))
		return
	}

	jwtToken, err := auth.IssueJWT(
		h.cfg.JWT.Secret,
		principal.UserID,
		sessionToken,
		principal.Email,
		principal.Name,
		principal.Picture,
		h.cfg.JWT.ExpiryHrs,
	)
	if err != nil {
		_ = h.metadata.RevokeSession(r.Context(), principal.UserID, sessionToken)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("token issuance failed: %w", err))
		return
	}

	setSessionCookie(w, jwtToken, time.Until(expiresAt), h.cfg.Server.IsProd)

	frontendURL, err := url.Parse(h.cfg.Server.FrontendURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("invalid frontend url: %w", err))
		return
	}

	http.Redirect(w, r, frontendURL.String(), http.StatusTemporaryRedirect)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims != nil {
		if err := h.metadata.RevokeSession(r.Context(), claims.Subject, claims.ID); err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	clearSessionCookie(w, h.cfg.Server.IsProd)
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed_out"})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"id":       principal.UserID,
		"email":    principal.Email,
		"name":     principal.Name,
		"picture":  principal.Picture,
		"drive_id": principal.DriveID,
	})
}

func (h *AuthHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	principal, claims := auth.PrincipalFromContext(r.Context()), auth.ClaimsFromContext(r.Context())
	if principal == nil || claims == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	sessions, err := h.metadata.ListAuthSessions(r.Context(), principal.UserID, claims.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *AuthHandler) RevokeSessionByID(w http.ResponseWriter, r *http.Request) {
	principal, claims := auth.PrincipalFromContext(r.Context()), auth.ClaimsFromContext(r.Context())
	if principal == nil || claims == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	current, err := h.metadata.RevokeAuthSessionByID(r.Context(), principal.UserID, chi.URLParam(r, "sessionID"), claims.ID)
	if err != nil {
		if errors.Is(err, repository.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current {
		clearSessionCookie(w, h.cfg.Server.IsProd)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true, "current": current})
}

func (h *AuthHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	principal, claims := auth.PrincipalFromContext(r.Context()), auth.ClaimsFromContext(r.Context())
	if principal == nil || claims == nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("missing authenticated user"))
		return
	}
	count, err := h.metadata.RevokeOtherAuthSessions(r.Context(), principal.UserID, claims.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": count})
}

func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"surajdrive/backend/internal/auth"
)

type SessionVerifier interface {
	AuthenticateSession(ctx context.Context, userPublicID, sessionToken string) (*auth.Principal, error)
}

func RequireAuth(jwtSecret string, verifier SessionVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := bearerToken(r.Header.Get("Authorization"))
			if tokenStr == "" {
				tokenStr = cookieToken(r)
			}
			if tokenStr == "" {
				writeJSONError(w, http.StatusUnauthorized, "missing token")
				return
			}

			claims, err := auth.ValidateJWT(jwtSecret, tokenStr)
			if err != nil {
				writeJSONError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			principal, err := verifier.AuthenticateSession(r.Context(), claims.Subject, claims.ID)
			if err != nil {
				if errors.Is(err, auth.ErrInvalidSession) {
					writeJSONError(w, http.StatusUnauthorized, "invalid session")
				} else {
					writeJSONError(w, http.StatusServiceUnavailable, "authentication service unavailable")
				}
				return
			}

			ctx := context.WithValue(r.Context(), auth.ClaimsKey, claims)
			ctx = context.WithValue(ctx, auth.PrincipalKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func cookieToken(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

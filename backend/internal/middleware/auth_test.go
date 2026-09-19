package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"surajdrive/backend/internal/auth"
)

type fakeSessionVerifier struct {
	principal *auth.Principal
	err       error
}

func (f fakeSessionVerifier) AuthenticateSession(context.Context, string, string) (*auth.Principal, error) {
	return f.principal, f.err
}

func TestRequireAuthAddsVerifiedPrincipal(t *testing.T) {
	const secret = "test-secret"
	token, err := auth.IssueJWT(secret, "user-id", "session-token", "user@example.com", "User", "", 1)
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}
	principal := &auth.Principal{UserID: "user-id", DriveID: "drive-id"}
	handler := RequireAuth(secret, fakeSessionVerifier{principal: principal})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.PrincipalFromContext(r.Context()) != principal {
			t.Fatal("verified principal missing from request context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
}

func TestRequireAuthDistinguishesInvalidSessionFromDatabaseFailure(t *testing.T) {
	const secret = "test-secret"
	token, err := auth.IssueJWT(secret, "user-id", "session-token", "user@example.com", "User", "", 1)
	if err != nil {
		t.Fatalf("issue JWT: %v", err)
	}

	tests := []struct {
		name       string
		verifyErr  error
		wantStatus int
	}{
		{name: "revoked", verifyErr: auth.ErrInvalidSession, wantStatus: http.StatusUnauthorized},
		{name: "database unavailable", verifyErr: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := RequireAuth(secret, fakeSessionVerifier{err: test.verifyErr})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("request reached protected handler")
			}))
			request := httptest.NewRequest(http.MethodGet, "/api/files", nil)
			request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("expected %d, got %d", test.wantStatus, response.Code)
			}
		})
	}
}

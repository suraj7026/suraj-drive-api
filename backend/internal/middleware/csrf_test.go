package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireTrustedOrigin(t *testing.T) {
	handler := RequireTrustedOrigin("https://drive.example.com/")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	tests := []struct {
		name, method, origin string
		want                 int
	}{
		{name: "same origin mutation", method: http.MethodPost, origin: "https://drive.example.com", want: http.StatusNoContent},
		{name: "foreign mutation", method: http.MethodPost, origin: "https://evil.example", want: http.StatusForbidden},
		{name: "missing mutation origin", method: http.MethodDelete, want: http.StatusForbidden},
		{name: "safe read", method: http.MethodGet, want: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://api.example.com/api/items", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d", response.Code, test.want)
			}
		})
	}
}

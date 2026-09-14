package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TestRequestIDHeader(t *testing.T) {
	handler := chimiddleware.RequestID(RequestIDHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Header().Get("X-Request-ID") == "" {
		t.Fatal("response did not include X-Request-ID")
	}
}

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorDoesNotExposeInternalDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("X-Request-ID", "request-123")
	writeError(recorder, http.StatusInternalServerError, errors.New("password=secret database failure"))
	if strings.Contains(recorder.Body.String(), "password=secret") {
		t.Fatalf("internal error leaked: %s", recorder.Body.String())
	}
	for _, expected := range []string{`"code":"internal_error"`, `"message":"internal server error"`, `"request_id":"request-123"`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("error response %q is missing %q", recorder.Body.String(), expected)
		}
	}
}

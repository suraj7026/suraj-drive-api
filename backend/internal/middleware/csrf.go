package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

func RequireTrustedOrigin(frontendURL string) func(http.Handler) http.Handler {
	trusted := normalizedOrigin(frontendURL)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			origin := normalizedOrigin(r.Header.Get("Origin"))
			if origin == "" || origin != trusted {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"request origin is not allowed","message":"request origin is not allowed","code":"forbidden"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func normalizedOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

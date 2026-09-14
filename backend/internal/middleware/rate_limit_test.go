package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"surajdrive/backend/internal/ratelimit"
)

func TestRateLimitUsesSocketAddressAndReturnsRetryAfter(t *testing.T) {
	limiter := ratelimit.NewFixedWindow(1, time.Minute, 10)
	handler := RateLimit(limiter, 60, RemoteAddressKey)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for index, expected := range []int{http.StatusNoContent, http.StatusTooManyRequests} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "192.0.2.10:12345"
		request.Header.Set("X-Forwarded-For", "203.0.113.1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("request %d: expected %d, got %d", index, expected, response.Code)
		}
		if expected == http.StatusTooManyRequests && response.Header().Get("Retry-After") != "60" {
			t.Fatalf("missing Retry-After header: %q", response.Header().Get("Retry-After"))
		}
	}
}

func TestClientIPKeyIgnoresForwardingFromUntrustedPeer(t *testing.T) {
	key, err := NewClientIPKey("172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	request.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := key(request); got != "192.0.2.10" {
		t.Fatalf("expected socket peer, got %q", got)
	}
}

func TestClientIPKeyUsesFirstUntrustedAddressBeforeTrustedChain(t *testing.T) {
	key, err := NewClientIPKey("172.16.0.0/12, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "172.18.0.4:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 10.1.2.3")
	if got := key(request); got != "198.51.100.9" {
		t.Fatalf("expected original client, got %q", got)
	}
}

func TestClientIPKeyRejectsMalformedOrOversizedForwarding(t *testing.T) {
	key, err := NewClientIPKey("172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	for _, forwarded := range []string{"198.51.100.9, garbage", strings.Repeat("1", 4097)} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = "172.18.0.4:443"
		request.Header.Set("X-Forwarded-For", forwarded)
		if got := key(request); got != "172.18.0.4" {
			t.Fatalf("expected trusted socket peer for malformed forwarding, got %q", got)
		}
	}
}

func TestNewClientIPKeyRejectsInvalidCIDR(t *testing.T) {
	if _, err := NewClientIPKey("not-a-network"); err == nil {
		t.Fatal("expected invalid trusted proxy CIDR to fail")
	}
}

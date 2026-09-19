package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"surajdrive/backend/internal/auth"
	"surajdrive/backend/internal/ratelimit"
)

type RateLimitKey func(*http.Request) string

func RateLimit(limiter *ratelimit.FixedWindow, retryAfterSeconds int, key RateLimitKey) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter.Allow(key(r)) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", formatPositiveInt(retryAfterSeconds))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "rate_limited", "message": "Too many requests. Please retry later.",
			})
		})
	}
}

func RemoteAddressKey(r *http.Request) string {
	return remoteAddress(r.RemoteAddr)
}

func NewClientIPKey(rawTrustedCIDRs string) (RateLimitKey, error) {
	trusted := make([]*net.IPNet, 0)
	for _, rawCIDR := range strings.Split(rawTrustedCIDRs, ",") {
		rawCIDR = strings.TrimSpace(rawCIDR)
		if rawCIDR == "" {
			continue
		}
		_, network, err := net.ParseCIDR(rawCIDR)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", rawCIDR, err)
		}
		trusted = append(trusted, network)
	}

	return func(r *http.Request) string {
		peer := net.ParseIP(remoteAddress(r.RemoteAddr))
		if peer == nil || !ipInNetworks(peer, trusted) {
			return RemoteAddressKey(r)
		}
		forwarded := r.Header.Get("X-Forwarded-For")
		if forwarded == "" || len(forwarded) > 4096 {
			return RemoteAddressKey(r)
		}
		parts := strings.Split(forwarded, ",")
		if len(parts) > 32 {
			return RemoteAddressKey(r)
		}
		addresses := make([]net.IP, 0, len(parts))
		for _, part := range parts {
			address := net.ParseIP(strings.TrimSpace(part))
			if address == nil {
				return RemoteAddressKey(r)
			}
			addresses = append(addresses, address)
		}
		for index := len(addresses) - 1; index >= 0; index-- {
			if !ipInNetworks(addresses[index], trusted) {
				return addresses[index].String()
			}
		}
		return addresses[0].String()
	}, nil
}

func AuthenticatedUserOrIPKey(clientIP RateLimitKey) RateLimitKey {
	return func(r *http.Request) string {
		if principal := auth.PrincipalFromContext(r.Context()); principal != nil && principal.UserID != "" {
			return "user:" + principal.UserID
		}
		return "ip:" + clientIP(r)
	}
}

func remoteAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil && host != "" {
		return host
	}
	if strings.TrimSpace(remoteAddr) != "" {
		return strings.TrimSpace(remoteAddr)
	}
	return "unknown"
}

func ipInNetworks(address net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func formatPositiveInt(value int) string {
	if value <= 0 {
		return "1"
	}
	// Retry intervals are small configuration constants; avoid a formatting
	// dependency in the request hot path.
	digits := [20]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

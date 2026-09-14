package ratelimit

import (
	"testing"
	"time"
)

func TestFixedWindow(t *testing.T) {
	limiter := NewFixedWindow(2, time.Minute, 10)
	now := time.Unix(1_700_000_000, 0)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("key") || !limiter.Allow("key") || limiter.Allow("key") {
		t.Fatal("fixed window did not enforce its limit")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("key") {
		t.Fatal("fixed window did not reset")
	}
}

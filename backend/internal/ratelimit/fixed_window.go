package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	windowStart time.Time
	count       int
}

type FixedWindow struct {
	mu      sync.Mutex
	entries map[string]entry
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
}

func NewFixedWindow(limit int, window time.Duration, maxKeys int) *FixedWindow {
	return &FixedWindow{entries: make(map[string]entry), limit: limit, window: window, maxKeys: maxKeys, now: time.Now}
}

func (l *FixedWindow) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	current, found := l.entries[key]
	if !found || now.Sub(current.windowStart) >= l.window {
		if !found && len(l.entries) >= l.maxKeys {
			for candidate, value := range l.entries {
				if now.Sub(value.windowStart) >= l.window {
					delete(l.entries, candidate)
				}
			}
			if len(l.entries) >= l.maxKeys {
				return false
			}
		}
		l.entries[key] = entry{windowStart: now, count: 1}
		return true
	}
	if current.count >= l.limit {
		return false
	}
	current.count++
	l.entries[key] = current
	return true
}

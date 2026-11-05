package auth

import (
	"sync"
	"time"
)

// maxKeys triggers a sweep of expired windows, so that an attacker cycling
// through usernames cannot grow the map without bound.
const maxKeys = 10_000

// Limiter counts events per key in fixed time windows and reports when a
// key has reached its limit. It is used to slow down password guessing:
// failed logins are recorded, and a blocked key is refused before the
// password is checked.
//
// Counts are kept in memory, so each server replica limits on its own and
// the effective limit across n replicas is n times higher. That is enough
// to make online guessing impractical.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]*limitWindow
}

type limitWindow struct {
	start time.Time
	count int
}

// NewLimiter returns a limiter that blocks a key after limit events within
// window.
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, now: time.Now, windows: map[string]*limitWindow{}}
}

// Blocked reports whether key has reached the limit in the current window.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(key, false)
	return w != nil && w.count >= l.limit
}

// Record counts one event for key.
func (l *Limiter) Record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current(key, true).count++
}

// Allow counts one event for every key and reports true, unless any key
// has already reached the limit, in which case it counts nothing and
// reports false. Checking and counting happen under one lock, so callers
// that count an attempt before doing the expensive work (such as hashing a
// password) cannot be outrun by parallel requests that all pass a separate
// Blocked check before any of them is recorded.
func (l *Limiter) Allow(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if w := l.current(k, false); w != nil && w.count >= l.limit {
			return false
		}
	}
	for _, k := range keys {
		l.current(k, true).count++
	}
	return true
}

// Forgive takes back one event counted by Allow for every key, for an
// attempt that turned out legitimate, such as a login with the right
// password. Keys whose window has ended meanwhile are left alone.
func (l *Limiter) Forgive(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if w := l.current(k, false); w != nil && w.count > 0 {
			w.count--
		}
	}
}

// current returns the live window of key, starting a new one when create is
// set. The caller holds l.mu.
func (l *Limiter) current(key string, create bool) *limitWindow {
	now := l.now()
	w, ok := l.windows[key]
	if ok && now.Sub(w.start) >= l.window {
		delete(l.windows, key)
		w, ok = nil, false
	}
	if ok || !create {
		return w
	}
	if len(l.windows) >= maxKeys {
		for k, old := range l.windows {
			if now.Sub(old.start) >= l.window {
				delete(l.windows, k)
			}
		}
	}
	w = &limitWindow{start: now}
	l.windows[key] = w
	return w
}

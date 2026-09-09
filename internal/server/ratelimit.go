package server

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// loginLimiter throttles password-login attempts per client IP to slow
// brute-force guessing (on top of bcrypt's own cost).
type loginLimiter struct {
	mu    sync.Mutex
	rate  rate.Limit
	burst int
	ips   map[string]*rate.Limiter
	last  time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		rate:  0.5, // one attempt per two seconds, sustained
		burst: 5,   // allow a few quick attempts
		ips:   map[string]*rate.Limiter{},
		last:  time.Now(),
	}
}

// allow reports whether an attempt from ip is permitted, consuming a token.
func (l *loginLimiter) allow(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// Reap the whole table periodically to bound memory (logins are rare).
	if time.Since(l.last) > 10*time.Minute {
		l.ips = map[string]*rate.Limiter{}
		l.last = time.Now()
	}

	lim, ok := l.ips[ip]
	if !ok {
		lim = rate.NewLimiter(l.rate, l.burst)
		l.ips[ip] = lim
	}
	return lim.Allow()
}

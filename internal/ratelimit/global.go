package ratelimit

import (
	"math"
	"sync"
	"time"
)

// GlobalLimiter is a single token bucket limiting the total query rate
// across all clients. Capacity equals maxRPS, allowing a one-second burst.
// A maxRPS <= 0 disables the limiter (Allow always returns true).
type GlobalLimiter struct {
	mu     sync.Mutex
	maxRPS float64
	tokens float64
	last   time.Time
}

func NewGlobal(maxRPS int) *GlobalLimiter {
	return &GlobalLimiter{
		maxRPS: float64(maxRPS),
		tokens: float64(maxRPS),
		last:   time.Now(),
	}
}

func (l *GlobalLimiter) Allow() bool {
	if l.maxRPS <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now
	l.tokens = math.Min(l.maxRPS, l.tokens+elapsed*l.maxRPS)

	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

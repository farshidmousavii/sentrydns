package ratelimit

import (
	"sync"
	"time"
)

type clientState struct {
	windowStart time.Time
	count       int
}

type Limiter struct {
	mu      sync.Mutex
	clients map[string]*clientState
	MaxRPS  int
	window  time.Duration
	stopCh  chan struct{}
	once    sync.Once
}

func New(maxRPS int) *Limiter {
	l := &Limiter{
		clients: make(map[string]*clientState),
		MaxRPS:  maxRPS,
		window:  time.Second,
		stopCh:  make(chan struct{}),
	}
	if maxRPS > 0 {
		go l.cleanup()
	}
	return l
}

func (l *Limiter) Allow(ip string) bool {
	if l.MaxRPS <= 0 {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cs, ok := l.clients[ip]
	if !ok || now.Sub(cs.windowStart) > l.window {
		l.clients[ip] = &clientState{windowStart: now, count: 1}
		return true
	}

	cs.count++
	return cs.count <= l.MaxRPS
}

func (l *Limiter) cleanup() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now()
			for ip, cs := range l.clients {
				if now.Sub(cs.windowStart) > 2*l.window {
					delete(l.clients, ip)
				}
			}
			l.mu.Unlock()
		case <-l.stopCh:
			return
		}
	}
}

func (l *Limiter) Stop() {
	l.once.Do(func() {
		close(l.stopCh)
	})
}

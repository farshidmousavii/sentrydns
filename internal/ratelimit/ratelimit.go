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
	shards    []shard
	MaxRPS    int
	window    time.Duration
	stopCh    chan struct{}
	once      sync.Once
}

type shard struct {
	mu      sync.Mutex
	clients map[string]*clientState
}

const numShards = 16

func New(maxRPS int) *Limiter {
	shards := make([]shard, numShards)
	for i := range shards {
		shards[i] = shard{clients: make(map[string]*clientState)}
	}
	l := &Limiter{
		shards:  shards,
		MaxRPS:  maxRPS,
		window:  time.Second,
		stopCh:  make(chan struct{}),
	}
	if maxRPS > 0 {
		go l.cleanup()
	}
	return l
}

func (l *Limiter) shardFor(ip string) *shard {
	h := uint64(0)
	for _, c := range ip {
		h = h*31 + uint64(c)
	}
	return &l.shards[h%uint64(len(l.shards))]
}

func (l *Limiter) Allow(ip string) bool {
	if l.MaxRPS <= 0 {
		return true
	}

	s := l.shardFor(ip)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cs, ok := s.clients[ip]
	if !ok || now.Sub(cs.windowStart) > l.window {
		s.clients[ip] = &clientState{windowStart: now, count: 1}
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
			now := time.Now()
			for i := range l.shards {
				s := &l.shards[i]
				s.mu.Lock()
				for ip, cs := range s.clients {
					if now.Sub(cs.windowStart) > 2*l.window {
						delete(s.clients, ip)
					}
				}
				s.mu.Unlock()
			}
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

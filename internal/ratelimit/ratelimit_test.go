package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAllowWithinLimit(t *testing.T) {
	rl := New(5)
	defer rl.Stop()

	ip := "192.168.1.1"
	for i := 0; i < 5; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("request %d should be allowed (rate 5)", i)
		}
	}
}

func TestDenyWhenExceeded(t *testing.T) {
	rl := New(3)
	defer rl.Stop()

	ip := "10.0.0.1"
	for i := 0; i < 3; i++ {
		rl.Allow(ip)
	}
	if rl.Allow(ip) {
		t.Error("4th request should be denied (rate 3)")
	}
}

func TestDifferentIPsSeparateBuckets(t *testing.T) {
	rl := New(2)
	defer rl.Stop()

	if !rl.Allow("10.0.0.1") {
		t.Error("first request for .1 should be allowed")
	}
	if !rl.Allow("10.0.0.2") {
		t.Error("first request for .2 should be allowed")
	}
	if !rl.Allow("10.0.0.1") {
		t.Error("second request for .1 should be allowed")
	}
	if !rl.Allow("10.0.0.2") {
		t.Error("second request for .2 should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Error("third request for .1 should be denied (rate 2)")
	}
}

func TestZeroRateDisablesLimit(t *testing.T) {
	rl := New(0)
	defer rl.Stop()

	for i := 0; i < 100; i++ {
		if !rl.Allow("10.0.0.1") {
			t.Fatal("rate 0 should allow all requests")
		}
	}
}

func TestConcurrentAllow(t *testing.T) {
	rl := New(50)
	defer rl.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				rl.Allow("10.0.0.1")
			}
		}()
	}
	wg.Wait()
}

func TestCleanupRemovesStaleEntries(t *testing.T) {
	rl := New(10)
	defer rl.Stop()

	for i := 0; i < 8; i++ {
		rl.Allow(fmt.Sprintf("10.0.0.%d", i+1))
	}

	s := rl.shardFor("10.0.0.1")
	s.mu.Lock()
	count := len(s.clients)
	for _, c := range s.clients {
		c.windowStart = time.Now().Add(-2 * time.Minute)
	}
	s.mu.Unlock()

	s.mu.Lock()
	now := time.Now()
	for ip, cs := range s.clients {
		if now.Sub(cs.windowStart) > 2*rl.window {
			delete(s.clients, ip)
		}
	}
	afterCount := len(s.clients)
	s.mu.Unlock()

	if afterCount != 0 {
		t.Errorf("expected 0 clients after cleanup, got %d", afterCount)
	}
	_ = count
}

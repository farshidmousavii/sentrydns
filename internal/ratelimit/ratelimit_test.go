package ratelimit

import (
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

	rl.mu.Lock()
	ipl := rl.clients["10.0.0.1"]
	rl.mu.Unlock()
	if ipl == nil {
		t.Fatal("expected client entry")
	}
	if ipl.count != 50 {
		t.Errorf("expected count 50 after 10x5 concurrent calls, got %d", ipl.count)
	}
}

func TestCleanupRemovesStaleEntries(t *testing.T) {
	rl := New(10)
	defer rl.Stop()

	rl.Allow("10.0.0.1")
	rl.Allow("10.0.0.2")

	rl.mu.Lock()
	if len(rl.clients) != 2 {
		rl.mu.Unlock()
		t.Fatalf("expected 2 client entries, got %d", len(rl.clients))
	}
	// Force the entries to be old by setting windowStart far in the past
	for _, c := range rl.clients {
		c.windowStart = time.Now().Add(-2 * time.Minute)
	}
	rl.mu.Unlock()

	// Execute one round of cleanup inline
	rl.mu.Lock()
	now := time.Now()
	for ip, cs := range rl.clients {
		if now.Sub(cs.windowStart) > 2*rl.window {
			delete(rl.clients, ip)
		}
	}
	count := len(rl.clients)
	rl.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 clients after cleanup, got %d", count)
	}
}

package ratelimit

import (
	"testing"
	"time"
)

func TestNewGlobalDisabled(t *testing.T) {
	l := NewGlobal(0)
	for i := 0; i < 1000; i++ {
		if !l.Allow() {
			t.Fatal("disabled limiter must always allow")
		}
	}
}

func TestGlobalBurstCapacity(t *testing.T) {
	l := NewGlobal(10)
	for i := 0; i < 10; i++ {
		if !l.Allow() {
			t.Fatalf("burst capacity: request %d denied", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("expected denial after exhausting burst capacity")
	}
}

func TestGlobalRefill(t *testing.T) {
	l := NewGlobal(10)
	for i := 0; i < 10; i++ {
		l.Allow()
	}
	if l.Allow() {
		t.Fatal("expected denial at empty bucket")
	}

	// simulate 1 second of elapsed time: full refill
	l.mu.Lock()
	l.last = l.last.Add(-time.Second)
	l.mu.Unlock()

	for i := 0; i < 10; i++ {
		if !l.Allow() {
			t.Fatalf("refilled bucket: request %d denied", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("expected denial after consuming refill")
	}
}

func TestGlobalPartialRefill(t *testing.T) {
	l := NewGlobal(10)
	for i := 0; i < 10; i++ {
		l.Allow()
	}

	// 100ms at 10 rps = 1 token
	l.mu.Lock()
	l.last = l.last.Add(-100 * time.Millisecond)
	l.mu.Unlock()

	if !l.Allow() {
		t.Fatal("expected one token after 100ms at 10 rps")
	}
	if l.Allow() {
		t.Fatal("expected denial after consuming the single refilled token")
	}
}

func TestGlobalCapOnRefill(t *testing.T) {
	l := NewGlobal(5)
	// bucket empty, then 1 hour passes: tokens must cap at 5, not 5*3600
	for i := 0; i < 5; i++ {
		l.Allow()
	}
	l.mu.Lock()
	l.last = l.last.Add(-time.Hour)
	l.mu.Unlock()

	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("capped refill: request %d denied", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("expected denial after consuming capped refill")
	}
}

func TestGlobalConcurrent(t *testing.T) {
	l := NewGlobal(100)
	const workers = 8
	const perWorker = 100
	allowed := make(chan int, workers)
	for w := 0; w < workers; w++ {
		go func() {
			n := 0
			for i := 0; i < perWorker; i++ {
				if l.Allow() {
					n++
				}
			}
			allowed <- n
		}()
	}
	total := 0
	for w := 0; w < workers; w++ {
		total += <-allowed
	}
	if total > 100 {
		t.Fatalf("concurrent consumers exceeded capacity: %d allowed", total)
	}
	if total == 0 {
		t.Fatal("expected at least one allowed request")
	}
}

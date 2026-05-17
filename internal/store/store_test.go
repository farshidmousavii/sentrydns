package store

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/metrics"
)

func TestStore(t *testing.T) {
	f, _ := os.CreateTemp("", "store-test-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, err := New(f.Name(), discardLog, m, "")
	if err != nil {
		t.Fatal(err)
	}

	if s.IsIran("digikala.com") {
		t.Error("expected false, got true")
	}

	s.Add("digikala.com")

	if !s.IsIran("digikala.com") {
		t.Error("expected true, got false")
	}

	if !s.IsIran("www.digikala.com") {
		t.Error("expected true for subdomain, got false")
	}
}

func TestCleanup(t *testing.T) {
	f, _ := os.CreateTemp("", "store-cleanup-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, err := New(f.Name(), discardLog, m, "")
	if err != nil {
		t.Fatal(err)
	}

	domains := []string{"keep1.com", "remove1.com", "keep2.com", "remove2.com", "keep3.com"}
	for _, d := range domains {
		s.Add(d)
	}

	validate := func(domain string) bool {
		return domain != "remove1.com" && domain != "remove2.com"
	}
	healthCheck := func() bool { return true }

	s.cleanup(validate, 4, healthCheck)

	if s.Count() != 3 {
		t.Errorf("store size = %d, want 3", s.Count())
	}

	if !s.IsIran("keep1.com") {
		t.Error("keep1.com should still exist")
	}
	if !s.IsIran("keep2.com") {
		t.Error("keep2.com should still exist")
	}
	if !s.IsIran("keep3.com") {
		t.Error("keep3.com should still exist")
	}
	if s.IsIran("remove1.com") {
		t.Error("remove1.com should be removed")
	}
	if s.IsIran("remove2.com") {
		t.Error("remove2.com should be removed")
	}

	if m.StoreCleaned.Load() != 2 {
		t.Errorf("StoreCleaned = %d, want 2", m.StoreCleaned.Load())
	}
}

func TestCleanupInterrupted(t *testing.T) {
	f, _ := os.CreateTemp("", "store-cleanup-int-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, err := New(f.Name(), discardLog, m, "")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 1000; i++ {
		s.Add(fmt.Sprintf("domain%d.com", i))
	}

	validate := func(domain string) bool {
		time.Sleep(time.Millisecond)
		return domain >= "domain500.com"
	}
	healthCheck := func() bool { return true }

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.cleanup(validate, 4, healthCheck)
	}()

	time.Sleep(50 * time.Millisecond)
	s.Stop()
	wg.Wait()

	n := s.Count()
	if n == 1000 {
		t.Log("cleanup was interrupted early — no domains deleted (expected on interrupt)")
	} else if n < 1000 {
		t.Logf("cleanup processed some domains before interrupt, remaining: %d", n)
	}
	if n == 0 {
		t.Error("all domains should not have been processed before interrupt")
	}
}

func TestCleanupHealthCheckFail(t *testing.T) {
	f, _ := os.CreateTemp("", "store-health-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, err := New(f.Name(), discardLog, m, "")
	if err != nil {
		t.Fatal(err)
	}

	s.Add("example.com")

	var attempts int32
	healthCheck := func() bool {
		atomic.AddInt32(&attempts, 1)
		time.Sleep(200 * time.Millisecond)
		return false
	}

	done := make(chan struct{})
	go func() {
		s.cleanup(func(domain string) bool { return true }, 4, healthCheck)
		close(done)
	}()

	time.Sleep(1500 * time.Millisecond)
	s.Stop()
	<-done

	if atomic.LoadInt32(&attempts) < 2 {
		t.Errorf("health check should have been retried, got %d attempts", attempts)
	}
}

func TestCleanupProcessedCounter(t *testing.T) {
	f, _ := os.CreateTemp("", "store-proc-*.txt")
	f.Close()
	defer os.Remove(f.Name())

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, err := New(f.Name(), discardLog, m, "")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 50; i++ {
		s.Add(fmt.Sprintf("domain%d.com", i))
	}

	validate := func(domain string) bool {
		time.Sleep(time.Millisecond)
		return true
	}
	healthCheck := func() bool { return true }

	s.cleanup(validate, 10, healthCheck)

	if m.StoreCleaned.Load() != 0 {
		t.Errorf("StoreCleaned should be 0 (all valid), got %d", m.StoreCleaned.Load())
	}
	if s.Count() != 50 {
		t.Errorf("store size should remain 50, got %d", s.Count())
	}
}

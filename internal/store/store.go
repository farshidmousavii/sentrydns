package store

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/metrics"
	statepkg "github.com/farshidmousavii/sentrydns/internal/state"
)

type Store struct {
	mu           sync.RWMutex
	ioMu         sync.Mutex
	domains      map[string]bool
	file         string
	log          *slog.Logger
	metrics      *metrics.Metrics
	statePath    string
	stopCleanup  chan struct{}
	persistBuf   []string
	persistBufGen uint64
	persistMu    sync.Mutex
	writeGen     atomic.Uint64

	flushMu    sync.Mutex
	flushBuf   *statepkg.State
	flushTimer *time.Timer

	rewriteTimer   *time.Timer
	rewriteTimerMu sync.Mutex
}

func New(file string, log *slog.Logger, m *metrics.Metrics, statePath string) (*Store, error) {
	s := &Store{
		domains:     make(map[string]bool),
		file:        file,
		log:         log,
		metrics:     m,
		statePath:   statePath,
		stopCleanup: make(chan struct{}),
		persistBuf:  make([]string, 0, 64),
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	go s.persistFlusher()
	return s, nil
}

func (s *Store) IsIran(domain string) bool {
	domain = normalize(domain)
	s.mu.RLock()
	defer s.mu.RUnlock()

	for {
		if s.domains[domain] {
			return true
		}
		dot := strings.IndexByte(domain, '.')
		if dot < 0 {
			break
		}
		domain = domain[dot+1:]
	}
	return false
}

func (s *Store) Add(domain string) {
	domain = normalize(domain)

	s.mu.Lock()
	if s.domains[domain] {
		s.mu.Unlock()
		return
	}
	s.domains[domain] = true
	s.mu.Unlock()

	if s.metrics != nil {
		s.metrics.LearnedTotal.Add(1)
	}
	s.persist(domain)
	if s.metrics != nil && s.metrics.LearnedTotal.Load()%50 == 0 {
		s.saveLearnedToday()
	}
}

func (s *Store) load() error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()

	f, err := os.Open(s.file)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		s.domains[line] = true
	}
	return scanner.Err()
}

func (s *Store) persist(domain string) {
	s.persistMu.Lock()
	s.persistBuf = append(s.persistBuf, domain)
	s.persistBufGen = s.writeGen.Load()
	s.persistMu.Unlock()
}

func (s *Store) persistFlusher() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.flushPersist()
		case <-s.stopCleanup:
			s.flushPersist()
			return
		}
	}
}

func (s *Store) flushPersist() {
	s.persistMu.Lock()
	if len(s.persistBuf) == 0 {
		s.persistMu.Unlock()
		return
	}
	domains := s.persistBuf
	bufGen := s.persistBufGen
	s.persistBuf = make([]string, 0, 64)
	s.persistBufGen = 0
	s.persistMu.Unlock()

	s.ioMu.Lock()
	// If writeAll rewrote the file since we drained the buffer,
	// the domains are already included in the new file — skip.
	if s.writeGen.Load() != bufGen {
		s.ioMu.Unlock()
		return
	}
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		s.ioMu.Unlock()
		return
	}
	for _, d := range domains {
		f.WriteString(d + "\n")
	}
	f.Sync()
	f.Close()
	s.ioMu.Unlock()
}

func normalize(domain string) string {
	return strings.ToLower(strings.TrimSuffix(domain, "."))
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.domains)
}

func (s *Store) Remove(domain string) {
	domain = normalize(domain)
	s.mu.Lock()
	if !s.domains[domain] {
		s.mu.Unlock()
		return
	}

	delete(s.domains, domain)
	s.mu.Unlock()

	if s.metrics != nil {
		s.metrics.StoreRemoved.Add(1)
	}
	s.scheduleRewrite()
}

func (s *Store) writeAll() {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()

	s.writeGen.Add(1)

	s.mu.RLock()
	domains := make([]string, 0, len(s.domains))
	for d := range s.domains {
		domains = append(domains, d)
	}
	s.mu.RUnlock()
	slices.Sort(domains)

	f, err := os.CreateTemp(filepath.Dir(s.file), "learned-*.tmp")
	if err != nil {
		s.log.Error("failed to create temp file for writeAll", "error", err)
		return
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	for _, d := range domains {
		if _, err := f.WriteString(d + "\n"); err != nil {
			f.Close()
			s.log.Error("failed to write temp file for writeAll", "error", err)
			return
		}
	}
	if err := f.Sync(); err != nil {
		s.log.Error("failed to sync temp file for writeAll", "error", err)
	}
	if err := f.Close(); err != nil {
		s.log.Error("failed to close temp file for writeAll", "error", err)
		return
	}

	if err := os.Rename(tmpPath, s.file); err != nil {
		s.log.Error("failed to rename temp file for writeAll", "error", err)
	}
}

func (s *Store) scheduleRewrite() {
	s.rewriteTimerMu.Lock()
	defer s.rewriteTimerMu.Unlock()
	if s.rewriteTimer != nil {
		s.rewriteTimer.Stop()
	}
	s.rewriteTimer = time.AfterFunc(500*time.Millisecond, s.writeAll)
}

func (s *Store) StartCleanup(initialDelay time.Duration, qps int, validate func(string) bool, healthCheck func() bool, nextDelay func() time.Duration) {
	go func() {
		s.log.Info("cleanup initial delay", "delay", initialDelay.Round(time.Second))
		timer := time.NewTimer(initialDelay)
		select {
		case <-timer.C:
			s.cleanup(validate, qps, healthCheck)
		case <-s.stopCleanup:
			timer.Stop()
			return
		}
		timer.Stop()

		for {
			var delay time.Duration
			if nextDelay != nil {
				delay = nextDelay()
			} else {
				delay = 24 * time.Hour
			}
			timer = time.NewTimer(delay)
			select {
			case <-timer.C:
				s.cleanup(validate, qps, healthCheck)
			case <-s.stopCleanup:
				timer.Stop()
				return
			}
			timer.Stop()
		}
	}()
}

func (s *Store) saveCleanupTime() {
	if s.statePath == "" {
		return
	}
	statepkg.Update(s.statePath, func(st *statepkg.State) {
		st.LastCleanupUnix = time.Now().Unix()
	})
}

func (s *Store) saveLearnedToday() {
	if s.statePath == "" || s.metrics == nil {
		return
	}
	total := s.metrics.LearnedTotal.Load()
	midnight := s.metrics.LearnedTotalAtMidnight.Load()
	today := total - midnight
	if today < 0 {
		today = 0
	}
	st := &statepkg.State{
		LearnedTodayDate:       time.Now().Format("2006-01-02"),
		LearnedTodayCount:      today,
		LearnedTotalAtMidnight: midnight,
		LearnedTotalCount:      total,
	}
	s.saveStateSoon(st)
}

func (s *Store) saveStateSoon(st *statepkg.State) {
	if s.statePath == "" {
		return
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.flushBuf = st
	s.scheduleFlushLocked()
}

func (s *Store) scheduleFlushLocked() {
	if s.flushTimer != nil {
		s.flushTimer.Stop()
	}
	s.flushTimer = time.AfterFunc(10*time.Second, s.flushState)
}

func (s *Store) flushState() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	if s.flushBuf == nil {
		return
	}

	st := s.flushBuf
	s.flushBuf = nil
	statepkg.Update(s.statePath, func(s2 *statepkg.State) {
		s2.LearnedTotalCount = st.LearnedTotalCount
		s2.LearnedTotalAtMidnight = st.LearnedTotalAtMidnight
		s2.LearnedTodayCount = st.LearnedTodayCount
		s2.LearnedTodayDate = st.LearnedTodayDate
	})
}

func (s *Store) cleanup(validate func(domain string) bool, qps int, healthCheck func() bool) {
	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second
	retries := 0
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	for {
		if healthCheck() {
			if retries > 0 {
				s.log.Info("IranDNS recovered, proceeding with cleanup", "retries", retries)
			}
			break
		}
		retries++
		s.log.Warn("cleanup waiting for IranDNS", "backoff", backoff, "attempt", retries)
		timer.Reset(backoff)
		select {
		case <-timer.C:
		case <-s.stopCleanup:
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	start := time.Now()
	total := s.Count()
	s.log.Info("cleanup started", "total", total)

	s.mu.RLock()
	domains := make([]string, 0, len(s.domains))
	for d := range s.domains {
		domains = append(domains, d)
	}
	s.mu.RUnlock()

	var mu sync.Mutex
	var toRemove []string
	var wg sync.WaitGroup
	var processed atomic.Int64

	work := make(chan string, qps*2)
	stop := s.stopCleanup

	for i := 0; i < qps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for domain := range work {
				select {
				case <-stop:
					return
				default:
				}
				valid := validate(domain)
				processed.Add(1)
				if !valid {
					mu.Lock()
					toRemove = append(toRemove, domain)
					mu.Unlock()
				}
			}
		}()
	}

	sent := 0
	for _, d := range domains {
		select {
		case work <- d:
			sent++
		case <-stop:
			close(work)
			wg.Wait()
			s.log.Warn("cleanup interrupted by shutdown",
				"total", total,
				"queued", sent,
				"validated", processed.Load(),
				"would_remove", len(toRemove),
			)
			return
		}
	}
	close(work)
	wg.Wait()

	s.mu.Lock()
	var removed int
	for _, d := range toRemove {
		if s.domains[d] {
			delete(s.domains, d)
			removed++
		}
	}
	if s.metrics != nil {
		s.metrics.StoreCleaned.Add(int64(removed))
	}
	s.mu.Unlock()

	s.writeAll()

	// Cancel any pending rewrite from a concurrent Remove() during cleanup.
	// The rewrite would be redundant since writeAll already wrote the current state.
	s.rewriteTimerMu.Lock()
	if s.rewriteTimer != nil {
		s.rewriteTimer.Stop()
	}
	s.rewriteTimerMu.Unlock()

	s.log.Info("cleanup finished",
		"total", total,
		"removed", removed,
		"remaining", s.Count(),
		"duration", time.Since(start).Round(time.Second).String(),
	)
	s.saveCleanupTime()
}

func (s *Store) Stop() {
	select {
	case <-s.stopCleanup:
	default:
		close(s.stopCleanup)
	}

	s.rewriteTimerMu.Lock()
	if s.rewriteTimer != nil {
		s.rewriteTimer.Stop()
	}
	s.rewriteTimerMu.Unlock()

	s.saveLearnedToday()

	s.flushMu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
	}
	if s.flushBuf != nil {
		s.flushMu.Unlock()
		s.flushState()
	} else {
		s.flushMu.Unlock()
	}
}

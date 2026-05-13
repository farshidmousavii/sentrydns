package store

import (
	"bufio"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu          sync.RWMutex
	domains     map[string]bool
	file        string
	log         *slog.Logger
	stopSorter  chan struct{}
	stopCleanup chan struct{}
}

func New(file string, log *slog.Logger) (*Store, error) {
	s := &Store{
		domains:     make(map[string]bool),
		file:        file,
		log:         log,
		stopSorter:  make(chan struct{}),
		stopCleanup: make(chan struct{}),
	}

	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return s, nil
}

func (s *Store) IsIran(domain string) bool {
	domain = normalize(domain)
	s.mu.RLock()
	defer s.mu.RUnlock()

	parts := strings.Split(domain, ".")
	for i := range parts {
		candidate := strings.Join(parts[i:], ".")
		if s.domains[candidate] {
			return true
		}
	}
	return false
}

func (s *Store) Add(domain string) {
	domain = normalize(domain)
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.domains[domain] {
		return
	}

	s.domains[domain] = true
	s.persist(domain)
}

func (s *Store) load() error {
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
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		s.log.Error("failed to open file for persist", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(domain + "\n"); err != nil {
		s.log.Error("failed to write domain", "domain", domain, "error", err)
	}
	f.Sync()
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
	defer s.mu.Unlock()

	if !s.domains[domain] {
		return
	}

	delete(s.domains, domain)
	s.writeAll()
}

func (s *Store) writeAll() {
	domains := make([]string, 0, len(s.domains))
	for d := range s.domains {
		domains = append(domains, d)
	}
	slices.Sort(domains)

	f, err := os.Create(s.file)
	if err != nil {
		return
	}
	defer f.Close()

	for _, d := range domains {
		_, _ = f.WriteString(d + "\n")
	}
}

func (s *Store) StartSorter(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sort()
			case <-s.stopSorter:
				return
			}
		}
	}()
}

func (s *Store) sort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeAll()
}

func (s *Store) StartCleanup(interval time.Duration, validate func(domain string) bool) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.cleanup(validate)
			case <-s.stopCleanup:
				return
			}
		}
	}()
}

func (s *Store) cleanup(validate func(domain string) bool) {
	start := time.Now()
	total := s.Count()
	s.log.Info("cleanup started", "total", total)

	s.mu.RLock()
	domains := make([]string, 0, len(s.domains))
	for d := range s.domains {
		domains = append(domains, d)
	}
	s.mu.RUnlock()

	sem := make(chan struct{}, 100)
	var mu sync.Mutex
	var toRemove []string
	var wg sync.WaitGroup

	for _, d := range domains {
		wg.Add(1)
		go func(domain string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if !validate(domain) {
				mu.Lock()
				toRemove = append(toRemove, domain)
				mu.Unlock()
			}
		}(d)
	}
	wg.Wait()

	s.mu.Lock()
	var removed int
	for _, d := range toRemove {
		if s.domains[d] {
			delete(s.domains, d)
			removed++
		}
	}
	s.writeAll()
	s.mu.Unlock()

	s.log.Info("cleanup finished",
		"total", total,
		"removed", removed,
		"remaining", s.Count(),
		"duration", time.Since(start).Round(time.Second).String(),
	)
}
func (s *Store) Stop() {
	select {
	case <-s.stopSorter:
	default:
		close(s.stopSorter)
	}
	select {
	case <-s.stopCleanup:
	default:
		close(s.stopCleanup)
	}
}

package classifier

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"sync"
)

type Classifier struct {
	mu          sync.RWMutex
	iranRanges4 []netip.Prefix
	iranRanges6 []netip.Prefix
}

func New(rangesFile string) (*Classifier, error) {
	c := &Classifier{}

	f, err := os.Open(rangesFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		prefix, err := netip.ParsePrefix(line)
		if err != nil {
			continue
		}
		if prefix.Addr().Is4() {
			c.iranRanges4 = append(c.iranRanges4, prefix)
		} else {
			c.iranRanges6 = append(c.iranRanges6, prefix)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", rangesFile, err)
	}
	if len(c.iranRanges4)+len(c.iranRanges6) == 0 {
		return nil, fmt.Errorf("no valid CIDR ranges found in %s", rangesFile)
	}
	slices.SortFunc(c.iranRanges4, func(a, b netip.Prefix) int {
		return b.Bits() - a.Bits()
	})
	slices.SortFunc(c.iranRanges6, func(a, b netip.Prefix) int {
		return b.Bits() - a.Bits()
	})
	return c, nil
}

func (c *Classifier) IsIran(ipStr string) bool {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ranges := c.iranRanges6
	if ip.Is4() {
		ranges = c.iranRanges4
	}
	for _, p := range ranges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *Classifier) Reload(path string) error {
	newC, err := New(path)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.iranRanges4 = newC.iranRanges4
	c.iranRanges6 = newC.iranRanges6
	return nil
}

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
	iranRanges  []netip.Prefix
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
		c.iranRanges = append(c.iranRanges, prefix)
		if prefix.Addr().Is4() {
			c.iranRanges4 = append(c.iranRanges4, prefix)
		} else {
			c.iranRanges6 = append(c.iranRanges6, prefix)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", rangesFile, err)
	}
	if len(c.iranRanges) == 0 {
		return nil, fmt.Errorf("no valid CIDR ranges found in %s", rangesFile)
	}
	sortRanges(c.iranRanges4)
	sortRanges(c.iranRanges6)
	return c, nil
}

func sortRanges(ranges []netip.Prefix) {
	slices.SortFunc(ranges, func(a, b netip.Prefix) int {
		return a.Bits() - b.Bits()
	})
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
	for _, network := range ranges {
		if network.Contains(ip) {
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
	c.iranRanges = newC.iranRanges
	c.iranRanges4 = newC.iranRanges4
	c.iranRanges6 = newC.iranRanges6
	return nil
}

package classifier

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"sync"
)

type Classifier struct {
	mu         sync.RWMutex
	iranRanges []*net.IPNet
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
		_, network, err := net.ParseCIDR(line)
		if err != nil {
			continue
		}
		c.iranRanges = append(c.iranRanges, network)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", rangesFile, err)
	}
	if len(c.iranRanges) == 0 {
		return nil, fmt.Errorf("no valid CIDR ranges found in %s", rangesFile)
	}
	return c, nil
}

func (c *Classifier) IsIran(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, network := range c.iranRanges {
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
	return nil
}

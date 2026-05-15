package cache

import (
	"log/slog"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type entry struct {
	msg     *dns.Msg
	expires time.Time
}

type Cache struct {
	mu      sync.RWMutex
	entries map[string]*entry
	log     *slog.Logger
	minTTL  uint32
	maxTTL  uint32
	stop    chan struct{}
}

func New(log *slog.Logger, minTTL, maxTTL uint32) *Cache {
	c := &Cache{
		entries: make(map[string]*entry),
		minTTL:  minTTL,
		maxTTL:  maxTTL,
		log:     log,
		stop:    make(chan struct{}),
	}
	go c.cleanup()
	return c
}

func (c *Cache) Get(req *dns.Msg) *dns.Msg {
	key := makeKey(req)
	if key == "" {
		return nil
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	if ok && time.Now().After(e.expires) {
		ok = false
	}
	c.mu.RUnlock()

	if !ok {
		c.log.Debug("cache_miss", "key", key)
		return nil
	}

	c.log.Debug("cache_hit", "key", key)
	resp := e.msg.Copy()
	resp.Id = req.Id
	return resp
}

func (c *Cache) Set(req *dns.Msg, resp *dns.Msg) {
	if resp == nil || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) == 0 {
		return
	}

	ttl := minTTL(resp)
	if ttl == 0 {
		return
	}

	if ttl < c.minTTL {
		ttl = c.minTTL
	}
	if ttl > c.maxTTL {
		ttl = c.maxTTL
	}

	key := makeKey(req)
	c.mu.Lock()
	c.entries[key] = &entry{
		msg:     resp.Copy(),
		expires: time.Now().Add(time.Duration(ttl) * time.Second),
	}
	c.mu.Unlock()
}

func makeKey(req *dns.Msg) string {
	if req == nil || len(req.Question) == 0 {
		return ""
	}
	q := req.Question[0]
	return q.Name + ":" + dns.TypeToString[q.Qtype]
}

func minTTL(msg *dns.Msg) uint32 {
	var ttl uint32 = ^uint32(0)
	for _, ans := range msg.Answer {
		if ans.Header().Ttl < ttl {
			ttl = ans.Header().Ttl
		}
	}
	if ttl == ^uint32(0) {
		return 0
	}
	return ttl
}

func (c *Cache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			c.mu.Lock()
			for k, e := range c.entries {
				if now.After(e.expires) {
					delete(c.entries, k)
				}
			}
			c.mu.Unlock()
		case <-c.stop:
			return
		}
	}
}

func (c *Cache) Stop() {
	close(c.stop)
}

package cache

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func newTestCache() *Cache {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), 300, 3600)
}

func aResp(domain, ip string, ttl uint32) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	m.Response = true
	m.Answer = append(m.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: dns.Fqdn(domain), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
		A:   net.ParseIP(ip),
	})
	return m
}

func req(domain string) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	return m
}

func TestCacheGetSet(t *testing.T) {
	c := newTestCache()
	r := req("digikala.com")
	resp := aResp("digikala.com", "5.22.0.1", 600)

	c.Set(r, resp)
	got := c.Get(r)
	if got == nil {
		t.Fatal("expected cached response")
	}
	if got.Answer[0].(*dns.A).A.String() != "5.22.0.1" {
		t.Errorf("unexpected IP: %s", got.Answer[0].(*dns.A).A.String())
	}
}

func TestCacheMiss(t *testing.T) {
	c := newTestCache()
	r := req("unknown.com")
	if c.Get(r) != nil {
		t.Error("expected nil for uncached query")
	}
}

func TestCacheClampsMinTTL(t *testing.T) {
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), 600, 3600)
	r := req("example.com")
	resp := aResp("example.com", "1.2.3.4", 60)

	c.Set(r, resp)
	got := c.Get(r)
	if got == nil {
		t.Fatal("expected cached entry")
	}
	c.mu.RLock()
	e, ok := c.entries["example.com.:A"]
	c.mu.RUnlock()
	if !ok {
		t.Error("expected entry in internal map")
	}
	remaining := time.Until(e.expires)
	if remaining < 500*time.Second {
		t.Errorf("expected expiry far in future (clamped to 600s), got ~%ds", int(remaining.Seconds()))
	}
}

func TestCacheExpiry(t *testing.T) {
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), 1, 1)
	r := req("short.com")
	resp := aResp("short.com", "1.2.3.4", 1)

	c.Set(r, resp)
	if c.Get(r) == nil {
		t.Fatal("expected immediate hit")
	}

	time.Sleep(1100 * time.Millisecond)
	if c.Get(r) != nil {
		t.Error("expected expired entry to return nil")
	}
}

func TestCacheDifferentTypes(t *testing.T) {
	c := newTestCache()
	a := req("example.com")
	aaaa := new(dns.Msg)
	aaaa.SetQuestion(dns.Fqdn("example.com"), dns.TypeAAAA)

	resp := aResp("example.com", "1.2.3.4", 600)
	c.Set(a, resp)

	if c.Get(aaaa) != nil {
		t.Error("AAAA query should not match A cache entry")
	}
}

func TestCacheStop(t *testing.T) {
	c := newTestCache()
	c.Stop()
}

package resolver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/classifier"
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/store"

	"github.com/miekg/dns"
)

type mockDNSHandler struct {
	responses   map[string]mockResponse
	callCount   int32
}

type mockResponse struct {
	ip      string
	rcode   int
	delay   time.Duration
}

func startFlexibleMockDNS(t *testing.T, handler *mockDNSHandler) string {
	t.Helper()
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		atomic.AddInt32(&handler.callCount, 1)
		domain := r.Question[0].Name
		m := new(dns.Msg)
		m.SetReply(r)

		resp, ok := handler.responses[domain]
		if !ok {
			resp = mockResponse{rcode: dns.RcodeNameError}
		}

		if resp.delay > 0 {
			time.Sleep(resp.delay)
		}

		m.Rcode = resp.rcode
		if resp.rcode == dns.RcodeSuccess && resp.ip != "" {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   domain,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP(resp.ip),
			})
		}
		w.WriteMsg(m)
	})

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := &dns.Server{PacketConn: conn, Handler: mux}
	go func() {
		if err := server.ActivateAndServe(); err != nil {
			t.Logf("mock DNS server stopped: %v", err)
		}
	}()
	t.Cleanup(func() { server.Shutdown() })

	addr := conn.LocalAddr().String()

	c := &dns.Client{Timeout: 100 * time.Millisecond}
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("test.com"), dns.TypeA)
	for i := 0; i < 50; i++ {
		if _, _, err := c.Exchange(req, addr); err == nil {
			return addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mock DNS server did not start on %s", addr)
	return ""
}

func startMockDNS(t *testing.T, responses map[string]string) string {
	t.Helper()
	h := &mockDNSHandler{
		responses: make(map[string]mockResponse, len(responses)),
	}
	for domain, ip := range responses {
		h.responses[domain] = mockResponse{ip: ip, rcode: dns.RcodeSuccess}
	}
	return startFlexibleMockDNS(t, h)
}

func TestDebug(t *testing.T) {
	addr := startMockDNS(t, map[string]string{
		"test.com.": "5.22.0.1",
	})

	c := &dns.Client{Timeout: time.Second}
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("test.com"), dns.TypeA)

	resp, _, err := c.Exchange(req, addr)
	t.Logf("resp: %v, err: %v", resp, err)
}

var testIranTLDs = []string{"ir", "ایران"}
var testHijackIPs = []string{"10.10.34.34", "10.10.34.35", "10.10.34.36"}
var testHijackRanges = []string{"50.7.0.0/16"}

func newTestResolver(t *testing.T, iranDNS, globalDNS string, iranTLDs, hijackIPs []string, hijackRanges []string, preferIranDomains []string, minTTL, maxTTL uint32) (*Resolver, *store.Store) {
	t.Helper()

	c, err := classifier.New("../../data/iran-ranges.txt")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.CreateTemp("", "store-*.txt")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, _ := store.New(f.Name(), discardLog, m, "")
	r := New(c, s, iranDNS, globalDNS, discardLog, iranTLDs, hijackIPs, hijackRanges, preferIranDomains, minTTL, maxTTL, m, "", 0, 5, time.Second)
	r.SetTimeout(time.Second)
	return r, s
}

func TestIranDomain(t *testing.T) {
	iranAddr := startMockDNS(t, map[string]string{
		"digikala.com.": "5.22.0.1",
	})
	globalAddr := startMockDNS(t, map[string]string{
		"digikala.com.": "142.250.0.1",
	})

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("digikala.com"), dns.TypeA)

	resp := r.Resolve(context.Background(), req)
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success")
	}
	if !s.IsIran("digikala.com.") {
		t.Fatal("digikala.com should be learned as iran domain after first query")
	}

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m2 := metrics.New()
	r2 := New(r.classifier, s, iranAddr, globalAddr, discardLog, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600, m2, "", 0, 5, time.Second)

	resp = 	r2.Resolve(context.Background(), req)
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success on second query using store")
	}
	ips := extractIPs(resp)
	if len(ips) == 0 || ips[0] != "5.22.0.1" {
		t.Errorf("expected Iran IP 5.22.0.1 via store, got %v", ips)
	}
}

func TestForeignDomain(t *testing.T) {
	iranAddr := startMockDNS(t, map[string]string{
		"youtube.com.": "142.250.0.1",
	})
	globalAddr := startMockDNS(t, map[string]string{
		"youtube.com.": "142.250.0.2",
	})

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("youtube.com"), dns.TypeA)
	resp := r.Resolve(context.Background(), req)

	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success")
	}
	if s.IsIran("youtube.com.") {
		t.Error("youtube.com should NOT be learned as iran")
	}
}

func TestHijackedDomain(t *testing.T) {
	iranAddr := startMockDNS(t, map[string]string{
		"filtered.com.": "10.10.34.34",
	})
	globalAddr := startMockDNS(t, map[string]string{
		"filtered.com.": "1.2.3.4",
	})

	r, _ := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("filtered.com"), dns.TypeA)
	resp := r.Resolve(context.Background(), req)

	ips := extractIPs(resp)
	if len(ips) == 0 || ips[0] != "1.2.3.4" {
		t.Errorf("expected global dns IP, got %v", ips)
	}
}

func TestSERVFAILRetry(t *testing.T) {
	iranAddr := startFlexibleMockDNS(t, &mockDNSHandler{
		responses: map[string]mockResponse{
			"retry-test.com.": {ip: "5.22.0.1", rcode: dns.RcodeServerFailure},
		},
	})
	globalAddr := startMockDNS(t, map[string]string{
		"retry-test.com.": "1.2.3.4",
	})

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)
	r.metrics.QueriesRetried.Store(0)
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("retry-test.com"), dns.TypeA)

	r.SetTimeout(2 * time.Second)
	resp := r.Resolve(context.Background(), req)

	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success via global after iran servfail")
	}
	if s.IsIran("retry-test.com.") {
		t.Error("retry-test.com should NOT be learned as iran")
	}
}

func TestSERVFAILBothAttempts(t *testing.T) {
	handler := &mockDNSHandler{
		responses: map[string]mockResponse{
			"always-servfail.com.": {rcode: dns.RcodeServerFailure},
		},
	}
	iranAddr := startFlexibleMockDNS(t, handler)
	globalAddr := startFlexibleMockDNS(t, handler)

	r, _ := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("always-servfail.com"), dns.TypeA)

	r.SetTimeout(2 * time.Second)
	r.metrics.QueriesRetried.Store(0)
	resp := r.Resolve(context.Background(), req)

	if resp == nil || resp.Rcode != dns.RcodeServerFailure {
		t.Fatal("expected SERVFAIL after both attempts fail")
	}
	if r.metrics.QueriesServfail.Load() != 1 {
		t.Errorf("QueriesServfail = %d, want 1", r.metrics.QueriesServfail.Load())
	}
	if r.metrics.QueriesRetried.Load() != 1 {
		t.Errorf("QueriesRetried = %d, want 1", r.metrics.QueriesRetried.Load())
	}
}

func TestGlobalFallbackDNS(t *testing.T) {
	globalAddr := startFlexibleMockDNS(t, &mockDNSHandler{
		responses: map[string]mockResponse{
			"fallback-test.com.": {rcode: dns.RcodeSuccess, delay: 2 * time.Second},
		},
	})

	c, err := classifier.New("../../data/iran-ranges.txt")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.CreateTemp("", "store-*.txt")
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.New()
	s, _ := store.New(f.Name(), discardLog, m, "")

	// Pre-load domain into store so we route through the store path,
	// which calls query() directly (bypassing resolveWithLearning/shortWait).
	s.Add("fallback-test.com")

	iranAddr := startMockDNS(t, map[string]string{})

	fallbackAddr := startMockDNS(t, map[string]string{
		"fallback-test.com.": "9.9.9.9",
	})

	r := New(c, s, iranAddr, globalAddr, discardLog, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600, m, fallbackAddr, 0, 5, time.Second)
	r.SetGlobalTimeout(50 * time.Millisecond)
	r.SetTimeout(time.Second)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("fallback-test.com"), dns.TypeA)

	resp := r.Resolve(context.Background(), req)
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success via fallback")
	}

	ips := extractIPs(resp)
	if len(ips) == 0 || ips[0] != "9.9.9.9" {
		t.Errorf("expected fallback IP 9.9.9.9, got %v", ips)
	}
	if m.GlobalFallbackCount.Load() != 1 {
		t.Errorf("GlobalFallbackCount = %d, want 1", m.GlobalFallbackCount.Load())
	}
	if m.GlobalTimeouts.Load() != 1 {
		t.Errorf("GlobalTimeouts = %d, want 1 (global DNS timed out)", m.GlobalTimeouts.Load())
	}
}

func TestQueriesGlobalCounted(t *testing.T) {
	iranAddr := startMockDNS(t, map[string]string{
		"foreign.com.": "142.250.0.1",
	})
	globalAddr := startMockDNS(t, map[string]string{
		"foreign.com.": "142.250.0.2",
	})

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)
	r.metrics.QueriesGlobal.Store(0)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("foreign.com"), dns.TypeA)
	resp := r.Resolve(context.Background(), req)

	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success")
	}
	if s.IsIran("foreign.com.") {
		t.Error("foreign.com should NOT be learned as iran")
	}
	if r.metrics.QueriesGlobal.Load() == 0 {
		t.Error("QueriesGlobal should be > 0 after routing foreign domain")
	}
}

func TestPreferIranDomain(t *testing.T) {
	iranAddr := startMockDNS(t, map[string]string{
		"preferred.com.": "5.22.0.1",
	})
	globalAddr := startMockDNS(t, map[string]string{
		"preferred.com.": "1.2.3.4",
	})

	r, _ := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, []string{"preferred.com"}, 300, 3600)
	r.metrics.PathPreferIran.Store(0)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("preferred.com"), dns.TypeA)
	resp := r.Resolve(context.Background(), req)

	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success")
	}
	if r.metrics.PathPreferIran.Load() != 1 {
		t.Errorf("PathPreferIran = %d, want 1", r.metrics.PathPreferIran.Load())
	}
	ips := extractIPs(resp)
	if len(ips) == 0 || ips[0] != "5.22.0.1" {
		t.Errorf("expected Iran IP 5.22.0.1 via prefer-iran path, got %v", ips)
	}
}

func TestPTRSkipsClassification(t *testing.T) {
	handler := &mockDNSHandler{
		responses: map[string]mockResponse{
			"1.0.168.192.in-addr.arpa.": {ip: "192.168.0.1", rcode: dns.RcodeSuccess},
		},
	}
	addr := startFlexibleMockDNS(t, handler)

	r, _ := newTestResolver(t, addr, addr, testIranTLDs, testHijackIPs, testHijackRanges, nil, 300, 3600)

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("1.0.168.192.in-addr.arpa"), dns.TypePTR)
	req.Question[0].Qtype = dns.TypePTR

	resp := r.Resolve(context.Background(), req)
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success for PTR query")
	}
}

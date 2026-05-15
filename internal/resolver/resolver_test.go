package resolver

import (
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/classifier"
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/store"

	"github.com/miekg/dns"
)

func startMockDNS(t *testing.T, responses map[string]string) string {
	t.Helper()
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		domain := r.Question[0].Name
		if ip, ok := responses[domain]; ok {
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   domain,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP(ip),
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
var minTTL = 300
var maxTTL = 3600

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
	s, _ := store.New(f.Name(), discardLog, m)
	r := New(c, s, iranDNS, globalDNS, discardLog, iranTLDs, hijackIPs, hijackRanges, preferIranDomains, minTTL, maxTTL, m)
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

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, uint32(minTTL), uint32(maxTTL))

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("digikala.com"), dns.TypeA)

	resp := r.Resolve(req)
	if resp == nil || resp.Rcode != dns.RcodeSuccess {
		t.Fatal("expected success")
	}
	if !s.IsIran("digikala.com.") {
		t.Fatal("digikala.com should be learned as iran domain after first query")
	}

	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	m2 := metrics.New()
	r2 := New(r.classifier, s, iranAddr, globalAddr, discardLog, testIranTLDs, testHijackIPs, testHijackRanges, nil, uint32(minTTL), uint32(maxTTL), m2)

	resp = r2.Resolve(req)
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

	r, s := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, uint32(minTTL), uint32(maxTTL))

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("youtube.com"), dns.TypeA)
	resp := r.Resolve(req)

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

	r, _ := newTestResolver(t, iranAddr, globalAddr, testIranTLDs, testHijackIPs, testHijackRanges, nil, uint32(minTTL), uint32(maxTTL))

	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn("filtered.com"), dns.TypeA)
	resp := r.Resolve(req)

	ips := extractIPs(resp)
	if len(ips) == 0 || ips[0] != "1.2.3.4" {
		t.Errorf("expected global dns IP, got %v", ips)
	}
}

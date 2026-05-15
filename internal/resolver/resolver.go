package resolver

import (
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/cache"
	"github.com/farshidmousavii/sentrydns/internal/classifier"
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/store"

	"github.com/miekg/dns"
	"golang.org/x/sync/singleflight"
)

func ServerFail(req *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Rcode = dns.RcodeServerFailure
	return m
}

type Resolver struct {
	classifier   *classifier.Classifier
	store        *store.Store
	cache        *cache.Cache
	iranDNS      string
	globalDNS    string
	timeout      atomic.Int64
	log          *slog.Logger
	metrics      *metrics.Metrics
	iranTLDs     map[string]bool
	hijackIPs    map[string]bool
	hijackRanges []*net.IPNet
	preferIran   map[string]bool
	sf           singleflight.Group
}

func New(c *classifier.Classifier, s *store.Store, iranDNS, globalDNS string, log *slog.Logger, iranTLDs, hijackIPs []string, hijackRanges []string, preferIranDomains []string, minTTL, maxTTL uint32, m *metrics.Metrics) *Resolver {
	tlds := make(map[string]bool)
	for _, t := range iranTLDs {
		tlds[strings.ToLower(t)] = true
	}

	hijacks := make(map[string]bool)
	for _, ip := range hijackIPs {
		hijacks[ip] = true
	}

	var ranges []*net.IPNet
	for _, cidr := range hijackRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			ranges = append(ranges, network)
		}
	}

	preferIran := make(map[string]bool, len(preferIranDomains))
	for _, d := range preferIranDomains {
		preferIran[strings.ToLower(d)] = true
	}

	r := &Resolver{
		classifier:   c,
		store:        s,
		cache:        cache.New(log, minTTL, maxTTL),
		iranDNS:      iranDNS,
		globalDNS:    globalDNS,
		log:          log,
		metrics:      m,
		iranTLDs:     tlds,
		hijackIPs:    hijacks,
		hijackRanges: ranges,
		preferIran:   preferIran,
	}
	r.timeout.Store(int64(3 * time.Second))
	return r
}

func (r *Resolver) isIranTLD(domain string) bool {
	parts := strings.Split(strings.TrimSuffix(domain, "."), ".")
	tld := strings.ToLower(parts[len(parts)-1])
	return r.iranTLDs[tld]
}

func (r *Resolver) isHijacked(ips []string) bool {
	for _, ipStr := range ips {
		if r.hijackIPs[ipStr] {
			return true
		}
		ip := net.ParseIP(ipStr)
		if ip != nil {
			for _, network := range r.hijackRanges {
				if network.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

func (r *Resolver) isPreferIran(domain string) bool {
	parts := strings.Split(strings.TrimSuffix(domain, "."), ".")
	for i := range parts {
		candidate := strings.Join(parts[i:], ".")
		if r.preferIran[candidate] {
			return true
		}
	}
	return false
}

func (r *Resolver) resolveWithLearning(req *dns.Msg, domain string) *dns.Msg {
	iranCh := make(chan *dns.Msg, 1)
	globalCh := make(chan *dns.Msg, 1)

	go func() { iranCh <- r.query(req, r.iranDNS) }()
	go func() { globalCh <- r.query(req, r.globalDNS) }()

	shortWait := time.Duration(r.timeout.Load()) / 4

	var iranMsg, globalMsg *dns.Msg

	select {
	case msg := <-iranCh:
		iranMsg = msg
	case msg := <-globalCh:
		globalMsg = msg
	}

	if iranMsg != nil {
		ips := extractIPs(iranMsg)
		if len(ips) > 0 && !r.isHijacked(ips) {
			for _, ip := range ips {
				if r.classifier.IsIran(ip) {
					r.store.Add(domain)
					r.metrics.QueriesIran.Add(1)
					r.log.Info("learned", "domain", domain, "ip", ip)
					select {
					case <-globalCh:
					default:
					}
					return iranMsg
				}
			}
		}

		waitTimer := time.NewTimer(shortWait)
		select {
		case msg := <-globalCh:
			waitTimer.Stop()
			globalMsg = msg
		case <-waitTimer.C:
		}

		if globalMsg != nil {
			r.log.Info("routed", "domain", domain, "upstream", "global")
			return globalMsg
		}

		r.log.Warn("global timeout, iran returned non-iranian ip", "domain", domain)
		return ServerFail(req)
	}

	if globalMsg != nil {
		waitTimer := time.NewTimer(shortWait)
		select {
		case msg := <-iranCh:
			waitTimer.Stop()
			iranMsg = msg
			if iranMsg != nil {
				ips := extractIPs(iranMsg)
				if len(ips) > 0 && !r.isHijacked(ips) {
					for _, ip := range ips {
						if r.classifier.IsIran(ip) {
							r.store.Add(domain)
							r.metrics.QueriesGlobal.Add(1)
							r.log.Info("learned", "domain", domain, "ip", ip)
						}
					}
				}
			}
		case <-waitTimer.C:
		}
		r.log.Info("routed", "domain", domain, "upstream", "global")
		return globalMsg
	}
	r.log.Warn("both upstreams failed", "domain", domain)
	return ServerFail(req)
}

func (r *Resolver) Resolve(req *dns.Msg) *dns.Msg {
	r.metrics.InFlightQueries.Add(1)
	defer r.metrics.InFlightQueries.Add(-1)

	if req == nil || len(req.Question) == 0 {
		return ServerFail(req)
	}

	r.metrics.QueriesTotal.Add(1)
	if cached := r.cache.Get(req); cached != nil {
		r.metrics.QueriesCached.Add(1)
		return cached
	}
	r.metrics.CacheMiss.Add(1)

	domain := req.Question[0].Name
	key := domain + ":" + dns.TypeToString[req.Question[0].Qtype]

	v, _, _ := r.sf.Do(key, func() (interface{}, error) {
		resp := r.resolve(req, domain)

		if resp == nil || resp.Rcode == dns.RcodeServerFailure {
			base := time.Duration(r.timeout.Load()) / 15
			sleep := base + time.Duration(rand.Int63n(int64(base/2+1)))
			time.Sleep(sleep)
			r.metrics.QueriesServfail.Add(1)
			resp = r.resolve(req, domain)
		}

		if resp == nil {
			return ServerFail(req), nil
		}
		r.cache.Set(req, resp)
		return resp, nil
	})

	resp := v.(*dns.Msg).Copy()
	resp.Id = req.Id
	return resp
}

func (r *Resolver) resolve(req *dns.Msg, domain string) *dns.Msg {
	if r.isIranTLD(domain) {
		r.metrics.PathTLD.Add(1)
		resp := r.query(req, r.iranDNS)
		if resp == nil || resp.Rcode != dns.RcodeSuccess {
			r.metrics.QueriesGlobal.Add(1)
			resp = r.query(req, r.globalDNS)
			return resp
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	if r.isPreferIran(domain) {
		r.metrics.PathPreferIran.Add(1)
		resp := r.query(req, r.iranDNS)
		if resp != nil && resp.Rcode == dns.RcodeSuccess {
			ips := extractIPs(resp)
			if len(ips) > 0 && !r.isHijacked(ips) {
				r.metrics.QueriesIran.Add(1)
				r.log.Info("routed", "domain", domain, "upstream", "iran-preferred")
				return resp
			}
		}
		r.metrics.QueriesGlobal.Add(1)
		return r.query(req, r.globalDNS)
	}

	if r.store.IsIran(domain) {
		r.metrics.PathStore.Add(1)
		resp := r.query(req, r.iranDNS)
		if resp != nil && resp.Rcode == dns.RcodeNameError {
			r.store.Remove(domain)
			return r.resolveWithLearning(req, domain)
		} else if resp == nil || resp.Rcode != dns.RcodeSuccess {
			r.metrics.QueriesGlobal.Add(1)
			return r.query(req, r.globalDNS)
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	r.metrics.PathLearn.Add(1)
	return r.resolveWithLearning(req, domain)
}

func (r *Resolver) query(req *dns.Msg, upstream string) *dns.Msg {
	c := &dns.Client{Timeout: time.Duration(r.timeout.Load())}

	addr := upstream
	if _, _, err := net.SplitHostPort(upstream); err != nil {
		addr = net.JoinHostPort(upstream, "53")
	}

	start := time.Now()
	resp, _, err := c.Exchange(req, addr)
	elapsed := time.Since(start)

	isIran := upstream == r.iranDNS
	if isIran {
		r.metrics.IranQueryCount.Add(1)
		if err != nil {
			r.metrics.IranTimeouts.Add(1)
		} else {
			r.metrics.IranLatencyTotal.Add(int64(elapsed))
		}
	} else {
		r.metrics.GlobalQueryCount.Add(1)
		if err != nil {
			r.metrics.GlobalTimeouts.Add(1)
		} else {
			r.metrics.GlobalLatencyTotal.Add(int64(elapsed))
		}
	}

	if err != nil {
		return nil
	}

	if resp.Truncated {
		r.metrics.TcpFallbackCount.Add(1)
		tcpResp, _, err := (&dns.Client{
			Timeout: time.Duration(r.timeout.Load()),
			Net:     "tcp",
		}).Exchange(req, addr)
		if err == nil {
			return tcpResp
		}
	}

	return resp
}

func (r *Resolver) ValidateDomain(domain string) bool {
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	resp := r.query(req, r.iranDNS)
	if resp == nil {
		return false
	}
	return resp.Rcode != dns.RcodeNameError
}

func (r *Resolver) IranDNSHealthy() bool {
	req := new(dns.Msg)
	req.SetQuestion("google.com.", dns.TypeA)
	c := &dns.Client{Timeout: 2 * time.Second}
	resp, _, err := c.Exchange(req, r.iranDNS)
	return err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess
}

func extractIPs(msg *dns.Msg) []string {
	var ips []string
	for _, ans := range msg.Answer {
		switch v := ans.(type) {
		case *dns.A:
			ips = append(ips, v.A.String())
		case *dns.AAAA:
			ips = append(ips, v.AAAA.String())
		}
	}
	return ips
}

func (r *Resolver) SetTimeout(d time.Duration) {
	r.timeout.Store(int64(d))
}

func (r *Resolver) Stop() {
	r.cache.Stop()
}

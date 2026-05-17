package resolver

import (
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/cache"
	"github.com/farshidmousavii/sentrydns/internal/classifier"
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/store"

	"github.com/miekg/dns"
	"golang.org/x/sync/singleflight"
)

type circuitBreaker struct {
	mu        sync.Mutex
	failures  int
	threshold int
	lastOpen  time.Time
	cooldown  time.Duration
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastOpen = time.Now()
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
}

func (cb *circuitBreaker) isOpen() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.failures >= cb.threshold {
		if time.Since(cb.lastOpen) > cb.cooldown {
			cb.failures = 0
			return false
		}
		return true
	}
	return false
}

func ServerFail(req *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Rcode = dns.RcodeServerFailure
	return m
}

type Resolver struct {
	classifier        *classifier.Classifier
	store             *store.Store
	cache             *cache.Cache
	iranDNS           string
	globalDNS         string
	timeout           atomic.Int64
	globalTimeout     atomic.Int64
	globalDNSFallback string
	log               *slog.Logger
	metrics           *metrics.Metrics
	iranTLDs          map[string]bool
	hijackIPs         map[string]bool
	hijackRanges      []*net.IPNet
	preferIran        map[string]bool
	sf                singleflight.Group
	iranCb            *circuitBreaker
	iranAddr          string
	globalAddr        string
}

func New(c *classifier.Classifier, s *store.Store, iranDNS, globalDNS string, log *slog.Logger, iranTLDs, hijackIPs []string, hijackRanges []string, preferIranDomains []string, minTTL, maxTTL uint32, m *metrics.Metrics, globalDNSFallback string) *Resolver {
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
		classifier:        c,
		store:             s,
		cache:             cache.New(log, minTTL, maxTTL),
		iranDNS:           iranDNS,
		globalDNS:         globalDNS,
		globalDNSFallback: globalDNSFallback,
		log:               log,
		metrics:           m,
		iranTLDs:          tlds,
		hijackIPs:         hijacks,
		hijackRanges:      ranges,
		preferIran:        preferIran,
		iranAddr:          resolveAddr(iranDNS),
		globalAddr:        resolveAddr(globalDNS),
		iranCb: &circuitBreaker{
			threshold: 5,
			cooldown:  30 * time.Second,
		},
	}
	r.timeout.Store(int64(3 * time.Second))
	r.globalTimeout.Store(int64(1500 * time.Millisecond))
	return r
}

func (r *Resolver) isIranTLD(domain string) bool {
	domain = strings.TrimSuffix(domain, ".")
	dot := strings.LastIndexByte(domain, '.')
	if dot < 0 {
		return r.iranTLDs[strings.ToLower(domain)]
	}
	return r.iranTLDs[strings.ToLower(domain[dot+1:])]
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
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if r.preferIran[domain] {
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

func (r *Resolver) resolveWithLearning(req *dns.Msg, domain string) *dns.Msg {
	iranCh := make(chan *dns.Msg, 1)
	globalCh := make(chan *dns.Msg, 1)

	go func() { globalCh <- r.query(req, r.globalDNS) }()

	if !r.iranCb.isOpen() {
		go func() { iranCh <- r.query(req, r.iranDNS) }()
	}

	shortWait := time.Duration(r.timeout.Load()) / 4

	var iranMsg, globalMsg *dns.Msg

	select {
	case msg := <-iranCh:
		iranMsg = msg
	case msg := <-globalCh:
		globalMsg = msg
	}

	if iranMsg != nil {
		qtype := req.Question[0].Qtype
		if qtype != dns.TypeA && qtype != dns.TypeAAAA {
			select {
			case <-globalCh:
			default:
			}
			r.metrics.QueriesIran.Add(1)
			return iranMsg
		}

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
			r.metrics.QueriesGlobal.Add(1)
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
							r.log.Info("learned", "domain", domain, "ip", ip)
						}
					}
				}
			}
		case <-waitTimer.C:
		}
		r.metrics.QueriesGlobal.Add(1)
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
	key := strings.ToLower(domain) + ":" + dns.TypeToString[req.Question[0].Qtype]

	v, _, _ := r.sf.Do(key, func() (interface{}, error) {
		resp := r.resolve(req, domain)

		if resp == nil || resp.Rcode == dns.RcodeServerFailure {
			base := time.Duration(r.timeout.Load()) / 15
			sleep := base + time.Duration(rand.Int63n(int64(base/2+1)))
			time.Sleep(sleep)
			r.metrics.QueriesRetried.Add(1)
			resp = r.resolve(req, domain)
		}

		if resp == nil {
			r.metrics.QueriesServfail.Add(1)
			return ServerFail(req), nil
		}
		if resp.Rcode == dns.RcodeServerFailure {
			r.metrics.QueriesServfail.Add(1)
			return resp, nil
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
	isIran := upstream == r.iranDNS
	timeout := time.Duration(r.timeout.Load())
	if !isIran && r.globalTimeout.Load() > 0 {
		timeout = time.Duration(r.globalTimeout.Load())
	}
	c := &dns.Client{Timeout: timeout}

	addr := r.iranAddr
	if !isIran {
		if upstream == r.globalDNS {
			addr = r.globalAddr
		} else {
			addr = upstream
			if _, _, err := net.SplitHostPort(upstream); err != nil {
				addr = net.JoinHostPort(upstream, "53")
			}
		}
	}

	start := time.Now()
	resp, _, err := c.Exchange(req, addr)
	elapsed := time.Since(start)

	if isIran {
		r.metrics.IranQueryCount.Add(1)
		if err != nil {
			r.metrics.IranTimeouts.Add(1)
			r.iranCb.recordFailure()
		} else {
			r.metrics.IranLatencyTotal.Add(int64(elapsed))
			r.iranCb.recordSuccess()
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
		if !isIran && r.globalDNSFallback != "" {
			fbAddr := r.globalDNSFallback
			if _, _, err := net.SplitHostPort(r.globalDNSFallback); err != nil {
				fbAddr = net.JoinHostPort(r.globalDNSFallback, "53")
			}
			fbStart := time.Now()
			fbResp, _, fbErr := (&dns.Client{Timeout: timeout}).Exchange(req, fbAddr)
			if fbErr == nil {
				r.metrics.GlobalFallbackCount.Add(1)
				r.metrics.GlobalFallbackLatencyTotal.Add(int64(time.Since(fbStart)))
				return fbResp
			}
		}
		return nil
	}

	if resp.Truncated {
		r.metrics.TcpFallbackCount.Add(1)
		tcpResp, _, err := (&dns.Client{
			Timeout: timeout,
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
	c := &dns.Client{Timeout: 1 * time.Second}
	resp, _, err := c.Exchange(req, r.iranAddr)
	if err != nil || resp == nil {
		return true
	}
	return resp.Rcode != dns.RcodeNameError
}

func resolveAddr(s string) string {
	if _, _, err := net.SplitHostPort(s); err != nil {
		return net.JoinHostPort(s, "53")
	}
	return s
}

func (r *Resolver) IranDNSHealthy() bool {
	req := new(dns.Msg)
	req.SetQuestion("nic.ir.", dns.TypeA)
	c := &dns.Client{Timeout: 2 * time.Second}
	for i := 0; i < 3; i++ {
		resp, _, err := c.Exchange(req, r.iranAddr)
		if err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess {
			return true
		}
		if i < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	r.log.Warn("IranDNS health check failed", "addr", r.iranAddr)
	return false
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

func (r *Resolver) SetGlobalTimeout(d time.Duration) {
	r.globalTimeout.Store(int64(d))
}

func (r *Resolver) Stop() {
	r.cache.Stop()
}

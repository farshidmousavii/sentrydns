package resolver

import (
	"context"
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

var dnsClientPool = sync.Pool{
	New: func() interface{} { return &dns.Client{} },
}

type cbState int32

const (
	cbClosed   cbState = 0
	cbOpen     cbState = 1
	cbHalfOpen cbState = 2
)

type circuitBreaker struct {
	state         atomic.Int32
	failures      atomic.Int32
	threshold     int32
	lastOpen      atomic.Int64
	cooldown      time.Duration
	onStateChange func(cbState)
}

func (cb *circuitBreaker) recordFailure() {
	n := cb.failures.Add(1)
	prev := cbState(cb.state.Load())
	if prev == cbHalfOpen || (prev == cbClosed && n >= cb.threshold) {
		if cb.state.CompareAndSwap(int32(prev), int32(cbOpen)) {
			cb.lastOpen.Store(time.Now().UnixNano())
			if cb.onStateChange != nil {
				cb.onStateChange(cbOpen)
			}
		}
	}
}

func (cb *circuitBreaker) recordSuccess() {
	switch cbState(cb.state.Load()) {
	case cbHalfOpen:
		cb.failures.Store(0)
		cb.state.Store(int32(cbClosed))
		if cb.onStateChange != nil {
			cb.onStateChange(cbClosed)
		}
	case cbClosed:
		for {
			cur := cb.failures.Load()
			if cur <= 0 || cb.failures.CompareAndSwap(cur, cur-1) {
				return
			}
		}
	}
}

func (cb *circuitBreaker) isOpen() bool {
	if cb.state.Load() != int32(cbOpen) {
		return false
	}
	if time.Since(time.Unix(0, cb.lastOpen.Load())) > cb.cooldown {
		cb.state.CompareAndSwap(int32(cbOpen), int32(cbHalfOpen))
		return false
	}
	return true
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

func New(c *classifier.Classifier, s *store.Store, iranDNS, globalDNS string, log *slog.Logger, iranTLDs, hijackIPs []string, hijackRanges []string, preferIranDomains []string, minTTL, maxTTL uint32, m *metrics.Metrics, globalDNSFallback string, cacheMaxEntries int) *Resolver {
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
		cache:             cache.New(log, minTTL, maxTTL, cacheMaxEntries),
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
			onStateChange: func(s cbState) {
				switch s {
				case cbOpen:
					m.IranCBTrips.Add(1)
					m.IranCBOpen.Store(1)
				case cbClosed:
					m.IranCBOpen.Store(0)
				}
			},
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

func (r *Resolver) resolveWithLearning(ctx context.Context, req *dns.Msg, domain string) *dns.Msg {
	iranCh := make(chan *dns.Msg, 1)
	globalCh := make(chan *dns.Msg, 1)

	iranCtx, iranCancel := context.WithCancel(ctx)
	defer iranCancel()

	go func() { globalCh <- r.query(ctx, req, r.globalDNS) }()
	if !r.iranCb.isOpen() {
		go func() { iranCh <- r.queryIranDNS(iranCtx, req) }()
	}

	shortWait := time.Duration(r.timeout.Load()) / 4

	var iranMsg, globalMsg *dns.Msg

	select {
	case msg := <-iranCh:
		iranMsg = msg
	case msg := <-globalCh:
		iranCancel()
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
		if !r.iranCb.isOpen() {
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
		}
		r.metrics.QueriesGlobal.Add(1)
		r.log.Info("routed", "domain", domain, "upstream", "global")
		return globalMsg
	}
	r.log.Warn("both upstreams failed", "domain", domain)
	return ServerFail(req)
}

func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) *dns.Msg {
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
		resp := r.resolve(ctx, req, domain)

		if resp == nil || resp.Rcode == dns.RcodeServerFailure {
			base := time.Duration(r.timeout.Load()) / 15
			sleep := base + time.Duration(rand.Int63n(int64(base/2+1)))
			time.Sleep(sleep)
			r.metrics.QueriesRetried.Add(1)
			resp = r.resolve(ctx, req, domain)
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

func (r *Resolver) resolve(ctx context.Context, req *dns.Msg, domain string) *dns.Msg {
	if r.isIranTLD(domain) {
		r.metrics.PathTLD.Add(1)
		resp := r.queryIranDNS(ctx, req)
		if resp == nil || resp.Rcode != dns.RcodeSuccess {
			r.metrics.QueriesGlobal.Add(1)
			resp = r.query(ctx, req, r.globalDNS)
			return resp
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	if r.isPreferIran(domain) {
		r.metrics.PathPreferIran.Add(1)
		resp := r.queryIranDNS(ctx, req)
		if resp != nil && resp.Rcode == dns.RcodeSuccess {
			ips := extractIPs(resp)
			if len(ips) > 0 && !r.isHijacked(ips) {
				r.metrics.QueriesIran.Add(1)
				r.log.Info("routed", "domain", domain, "upstream", "iran-preferred")
				return resp
			}
		}
		r.metrics.QueriesGlobal.Add(1)
		return r.query(ctx, req, r.globalDNS)
	}

	if r.store.IsIran(domain) {
		r.metrics.PathStore.Add(1)
		resp := r.queryIranDNS(ctx, req)
		if resp != nil && resp.Rcode == dns.RcodeNameError {
			r.store.Remove(domain)
			return r.resolveWithLearning(ctx, req, domain)
		} else if resp == nil || resp.Rcode != dns.RcodeSuccess {
			r.metrics.QueriesGlobal.Add(1)
			return r.query(ctx, req, r.globalDNS)
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	r.metrics.PathLearn.Add(1)
	return r.resolveWithLearning(ctx, req, domain)
}

func (r *Resolver) queryIranDNS(ctx context.Context, req *dns.Msg) *dns.Msg {
	if r.iranCb.isOpen() {
		r.metrics.IranCBSkipped.Add(1)
		r.log.Debug("circuit open, skipping IranDNS", "domain", req.Question[0].Name)
		return nil
	}
	return r.query(ctx, req, r.iranDNS)
}

func (r *Resolver) query(ctx context.Context, req *dns.Msg, upstream string) *dns.Msg {
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	isIran := upstream == r.iranDNS
	timeout := time.Duration(r.timeout.Load())
	if !isIran && r.globalTimeout.Load() > 0 {
		timeout = time.Duration(r.globalTimeout.Load())
	}
	c := dnsClientPool.Get().(*dns.Client)
	c.Timeout = timeout
	c.Net = "udp"
	defer dnsClientPool.Put(c)

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
			fbC := dnsClientPool.Get().(*dns.Client)
			fbC.Timeout = timeout
			fbC.Net = "udp"
			fbResp, _, fbErr := fbC.Exchange(req, fbAddr)
			dnsClientPool.Put(fbC)
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
		tcpC := dnsClientPool.Get().(*dns.Client)
		tcpC.Timeout = timeout
		tcpC.Net = "tcp"
		tcpResp, _, err := tcpC.Exchange(req, addr)
		dnsClientPool.Put(tcpC)
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

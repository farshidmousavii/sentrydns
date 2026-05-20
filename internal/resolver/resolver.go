package resolver

import (
	"context"
	"log/slog"
	"math/rand"
	"net"
	"net/netip"
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

var dnsIDCounter atomic.Uint32

func randDNSID() uint16 {
	id := dnsIDCounter.Add(1)
	// Mask to 16 bits; skip 0 to avoid clients treating it as unset
	id &= 0xFFFF
	if id == 0 {
		id = 1
	}
	return uint16(id)
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
	cooldown      atomic.Int64
	onStateChange func(cbState)
}

func (cb *circuitBreaker) recordFailure() {
	n := cb.failures.Add(1)
	prev := cbState(cb.state.Load())
	if prev == cbHalfOpen || (prev == cbClosed && n >= cb.threshold) {
		if cb.state.CompareAndSwap(int32(prev), int32(cbOpen)) {
			cb.lastOpen.Store(time.Now().UnixNano())
			cb.failures.Store(0)
			if cb.onStateChange != nil {
				cb.onStateChange(cbOpen)
			}
		}
	}
}

func (cb *circuitBreaker) recordSuccess() {
	switch cbState(cb.state.Load()) {
	case cbHalfOpen:
		if cb.state.CompareAndSwap(int32(cbHalfOpen), int32(cbClosed)) {
			cb.failures.Store(0)
			if cb.onStateChange != nil {
				cb.onStateChange(cbClosed)
			}
		}
	case cbClosed:
		if cb.failures.Load() > 0 {
			cb.failures.Add(-1)
		}
	}
}

func (cb *circuitBreaker) isOpen() bool {
	switch cbState(cb.state.Load()) {
	case cbOpen, cbHalfOpen:
		return true
	default:
		return false
	}
}

func (cb *circuitBreaker) tryProbe() bool {
	st := cbState(cb.state.Load())
	if st != cbOpen {
		return false
	}
	if time.Since(time.Unix(0, cb.lastOpen.Load())) <= time.Duration(cb.cooldown.Load()) {
		return false
	}
	return cb.state.CompareAndSwap(int32(cbOpen), int32(cbHalfOpen))
}

func ServerFail(req *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	if req != nil {
		m.SetReply(req)
	}
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
	hijackRanges      []netip.Prefix
	preferIran        map[string]bool
	sf                singleflight.Group
	iranCb            *circuitBreaker
	iranAddr          string
	globalAddr        string
	stopped           atomic.Bool
	stopCh            chan struct{}
	active            atomic.Int64
}

func New(c *classifier.Classifier, s *store.Store, iranDNS, globalDNS string, log *slog.Logger, iranTLDs, hijackIPs []string, hijackRanges []string, preferIranDomains []string, minTTL, maxTTL uint32, m *metrics.Metrics, globalDNSFallback string, cacheMaxEntries int, cbThreshold int, cbCooldown time.Duration) *Resolver {
	tlds := make(map[string]bool)
	for _, t := range iranTLDs {
		tlds[strings.ToLower(t)] = true
	}

	hijacks := make(map[string]bool)
	for _, ip := range hijackIPs {
		hijacks[ip] = true
	}

	var ranges []netip.Prefix
	for _, cidr := range hijackRanges {
		prefix, err := netip.ParsePrefix(cidr)
		if err == nil {
			ranges = append(ranges, prefix)
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
			threshold: int32(cbThreshold),
			cooldown:  atomic.Int64{},
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
		stopCh: make(chan struct{}),
	}
	r.iranCb.cooldown.Store(int64(cbCooldown))
	r.timeout.Store(int64(3 * time.Second))
	r.globalTimeout.Store(int64(1500 * time.Millisecond))
	return r
}

func (r *Resolver) isIranTLD(domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if r.iranTLDs[domain] {
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

func (r *Resolver) isHijacked(ips []string) bool {
	for _, ipStr := range ips {
		if r.hijackIPs[ipStr] {
			return true
		}
		ip, err := netip.ParseAddr(ipStr)
		if err == nil {
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
	globalCtx, globalCancel := context.WithCancel(ctx)
	defer globalCancel()

	iranOpen := r.iranCb.isOpen()
	needGlobal := req.Question[0].Qtype == dns.TypeA || req.Question[0].Qtype == dns.TypeAAAA || iranOpen

	if needGlobal {
		go func() { globalCh <- r.query(globalCtx, req.Copy(), r.globalDNS) }()
	}
	if !iranOpen {
		go func() { iranCh <- r.queryIranDNS(iranCtx, req.Copy()) }()
	}

	shortWait := time.Duration(r.globalTimeout.Load())
	if shortWait <= 0 {
		shortWait = time.Duration(r.timeout.Load()) / 4
	} else {
		shortWait = shortWait / 4
	}
	if shortWait < 100*time.Millisecond {
		shortWait = 100 * time.Millisecond
	}

	var iranMsg, globalMsg *dns.Msg

	select {
	case msg := <-iranCh:
		iranMsg = msg
	case msg := <-globalCh:
		if msg != nil {
			iranCancel()
		}
		globalCancel()
		globalMsg = msg
	case <-ctx.Done():
		return ServerFail(req)
	}

	// If the first upstream returned nil, wait for the other one.
	if iranMsg == nil && globalMsg == nil {
		select {
		case msg := <-iranCh:
			iranMsg = msg
		case msg := <-globalCh:
			if msg != nil {
				iranCancel()
			}
			globalCancel()
			globalMsg = msg
		case <-time.After(shortWait):
		case <-ctx.Done():
			return ServerFail(req)
		}
	}

	if iranMsg != nil {
		qtype := req.Question[0].Qtype
		if qtype != dns.TypeA && qtype != dns.TypeAAAA {
			globalCancel()
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
					globalCancel()
					return iranMsg
				}
			}
		}

		waitTimer := time.NewTimer(shortWait)
		defer waitTimer.Stop()
		select {
		case msg := <-globalCh:
			if !waitTimer.Stop() {
				select {
				case <-waitTimer.C:
				default:
				}
			}
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
		if !iranOpen {
			waitTimer := time.NewTimer(shortWait)
			defer waitTimer.Stop()
			select {
			case msg := <-iranCh:
				if !waitTimer.Stop() {
					select {
					case <-waitTimer.C:
					default:
					}
				}
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
	if !needGlobal {
		fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), time.Duration(r.globalTimeout.Load()))
		defer fallbackCancel()
		if msg := r.query(fallbackCtx, req.Copy(), r.globalDNS); msg != nil {
			r.metrics.QueriesGlobal.Add(1)
			r.log.Info("routed", "domain", domain, "upstream", "global")
			return msg
		}
	}

	r.log.Warn("no suitable upstream response", "domain", domain, "iran_attempted", !iranOpen, "global_attempted", needGlobal)
	return ServerFail(req)
}

func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) *dns.Msg {
	if r.stopped.Load() {
		return ServerFail(req)
	}
	select {
	case <-r.stopCh:
		return ServerFail(req)
	default:
	}
	r.metrics.InFlightQueries.Add(1)
	r.active.Add(1)
	defer r.active.Add(-1)
	defer r.metrics.InFlightQueries.Add(-1)

	if req == nil {
		return ServerFail(nil)
	}
	if len(req.Question) == 0 {
		return ServerFail(req)
	}

	r.metrics.QueriesTotal.Add(1)
	if cached := r.cache.Get(req); cached != nil {
		r.metrics.QueriesCached.Add(1)
		return cached
	}
	r.metrics.CacheMiss.Add(1)

	domain := req.Question[0].Name
	origID := req.Id
	key := strings.ToLower(domain) + ":" + dns.TypeToString[req.Question[0].Qtype]

	v, err, _ := r.sf.Do(key, func() (interface{}, error) {
		resp := r.resolve(ctx, req, domain)

		if resp == nil || resp.Rcode == dns.RcodeServerFailure {
			r.metrics.QueriesRetried.Add(1)
			timeout := time.Duration(r.timeout.Load())
			const divisor = 15
			jitter := timeout/divisor + time.Duration(rand.Int63n(int64(timeout/(divisor*2))))
			time.Sleep(jitter)
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
	if err != nil {
		r.metrics.QueriesServfail.Add(1)
		return ServerFail(req)
	}

	resp := v.(*dns.Msg).Copy()
	resp.Id = origID
	return resp
}

func (r *Resolver) resolve(ctx context.Context, req *dns.Msg, domain string) *dns.Msg {
	queryCtx, queryCancel := context.WithCancel(ctx)
	defer queryCancel()

	if r.isIranTLD(domain) {
		r.metrics.PathTLD.Add(1)
		reqCopy := req.Copy()
		resp := r.queryIranDNS(queryCtx, reqCopy)
		if resp == nil || resp.Rcode != dns.RcodeSuccess {
			r.metrics.QueriesGlobal.Add(1)
			resp = r.query(queryCtx, reqCopy, r.globalDNS)
			return resp
		}
		if ips := extractIPs(resp); len(ips) > 0 && r.isHijacked(ips) {
			r.log.Warn("hijacked response for TLD domain", "domain", domain)
			r.metrics.QueriesGlobal.Add(1)
			return r.query(queryCtx, reqCopy, r.globalDNS)
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	if r.isPreferIran(domain) {
		r.metrics.PathPreferIran.Add(1)
		reqCopy := req.Copy()
		resp := r.queryIranDNS(queryCtx, reqCopy)
		if resp != nil && resp.Rcode == dns.RcodeSuccess {
			ips := extractIPs(resp)
			if len(ips) > 0 && !r.isHijacked(ips) {
				r.metrics.QueriesIran.Add(1)
				r.log.Info("routed", "domain", domain, "upstream", "iran-preferred")
				return resp
			}
		}
		r.metrics.QueriesGlobal.Add(1)
		return r.query(queryCtx, reqCopy, r.globalDNS)
	}

	if r.store.IsIran(domain) {
		r.metrics.PathStore.Add(1)
		reqCopy := req.Copy()
		resp := r.queryIranDNS(queryCtx, reqCopy)
		if resp != nil && resp.Rcode == dns.RcodeNameError {
			r.log.Warn("nxdomain for store domain, removing and relearning", "domain", domain)
			r.store.Remove(domain)
			return r.resolveWithLearning(ctx, req, domain)
		} else if resp == nil || resp.Rcode != dns.RcodeSuccess {
			if resp == nil {
				r.log.Warn("iran dns query returned nil for store domain, falling back to global", "domain", domain)
			} else {
				r.log.Warn("iran dns failed for store domain, falling back to global", "domain", domain, "rcode", resp.Rcode)
			}
			r.metrics.QueriesGlobal.Add(1)
			return r.query(queryCtx, reqCopy, r.globalDNS)
		}
		if ips := extractIPs(resp); len(ips) > 0 && r.isHijacked(ips) {
			r.log.Warn("hijacked response for store domain", "domain", domain)
			r.metrics.QueriesGlobal.Add(1)
			return r.query(queryCtx, reqCopy, r.globalDNS)
		}
		r.metrics.QueriesIran.Add(1)
		return resp
	}

	r.metrics.PathLearn.Add(1)
	return r.resolveWithLearning(ctx, req, domain)
}

func (r *Resolver) queryIranDNS(ctx context.Context, req *dns.Msg) *dns.Msg {
	if r.iranCb.isOpen() {
		if !r.iranCb.tryProbe() {
			r.metrics.IranCBSkipped.Add(1)
			r.log.Debug("circuit open, skipping IranDNS", "domain", req.Question[0].Name)
			return nil
		}
		r.log.Debug("circuit half-open, probe to IranDNS", "domain", req.Question[0].Name)
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

	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return nil
	}

	c := dnsClientPool.Get().(*dns.Client)
	c.Timeout = timeout
	c.Net = "udp"
	dnsCtx, dnsCancel := context.WithTimeout(ctx, timeout)
	defer dnsCancel()

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

	if req.IsEdns0() == nil {
		req.SetEdns0(1232, false)
	}

	start := time.Now()
	req.Id = randDNSID()
	resp, _, err := c.ExchangeContext(dnsCtx, req, addr)
	elapsed := time.Since(start)
	dnsClientPool.Put(c)

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
			fbCtx, fbCancel := context.WithTimeout(ctx, timeout)
			fbResp, _, fbErr := fbC.ExchangeContext(fbCtx, req, fbAddr)
			fbCancel()
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
		tcpCtx, tcpCancel := context.WithTimeout(ctx, timeout)
		defer tcpCancel()
		tcpResp, _, err := tcpC.ExchangeContext(tcpCtx, req, addr)
		dnsClientPool.Put(tcpC)
		if err == nil {
			return tcpResp
		}
	}

	return resp
}

func (r *Resolver) ValidateDomain(domain string) bool {
	if r.stopped.Load() {
		return true
	}
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	req.Id = randDNSID()
	c := dnsClientPool.Get().(*dns.Client)
	c.Timeout = time.Second
	c.Net = "udp"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, _, err := c.ExchangeContext(ctx, req, r.iranAddr)
	dnsClientPool.Put(c)
	if err != nil || resp == nil {
		return true
	}
	return resp.Rcode != dns.RcodeNameError
}

func resolveAddr(s string) string {
	if s == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return net.JoinHostPort(s, "53")
	}
	return s
}

func (r *Resolver) IranDNSHealthy() bool {
	if r.stopped.Load() {
		return false
	}
	req := new(dns.Msg)
	req.SetQuestion("nic.ir.", dns.TypeA)
	req.Id = randDNSID()
	c := dnsClientPool.Get().(*dns.Client)
	c.Timeout = 2 * time.Second
	c.Net = "udp"
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, _, err := c.ExchangeContext(ctx, req, r.iranAddr)
		cancel()
		if err == nil && resp != nil && resp.Rcode == dns.RcodeSuccess {
			dnsClientPool.Put(c)
			return true
		}
		if i < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	dnsClientPool.Put(c)
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

func (r *Resolver) SetCBCooldown(d time.Duration) {
	r.iranCb.cooldown.Store(int64(d))
}

func (r *Resolver) Stop() {
	r.stopped.Store(true)
	close(r.stopCh)

	const stopTimeout = 30 * time.Second
	deadline := time.Now().Add(stopTimeout)
	for r.active.Load() > 0 {
		if time.Now().After(deadline) {
			r.log.Warn("resolver stop timeout, forcing shutdown")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.cache.Stop()
}

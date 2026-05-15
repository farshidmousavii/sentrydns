package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/classifier"
	"github.com/farshidmousavii/sentrydns/internal/config"
	"github.com/farshidmousavii/sentrydns/internal/logger"
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/resolver"
	"github.com/farshidmousavii/sentrydns/internal/store"
	"github.com/farshidmousavii/sentrydns/internal/updater"

	"github.com/miekg/dns"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if err := ensureLogDir(cfg.LogFile); err != nil {
		log.Fatalf("failed to create log directory: %v", err)
	}

	slog, closeLog := logger.New(cfg.LogLevel, cfg.LogFormat == "json", cfg.LogFile)

	slog.Info("starting sentrydns",
		"listen", cfg.Listen,
		"iran_dns", cfg.IranDNS,
		"global_dns", cfg.GlobalDNS,
		"log_level", cfg.LogLevel,
		"log_file", cfg.LogFile,
	)

	c, err := classifier.New(cfg.IranRanges)
	if err != nil {
		slog.Error("failed to load classifier", "error", err)
		os.Exit(1)
	}
	slog.Info("classifier loaded")

	m := metrics.New()

	s, err := store.New(cfg.Learned, slog, m, cfg.StateFile)
	if err != nil {
		slog.Error("failed to load store", "error", err)
		os.Exit(1)
	}
	slog.Info("store loaded", "domains", s.Count())

	m.RestoreFromFile(cfg.StateFile)

	metricsSrv := m.StartServer(cfg.MetricsAddr, func() int64 {
		return int64(s.Count())
	})
	slog.Info("metrics server started", "addr", cfg.MetricsAddr)

	r := resolver.New(c, s, cfg.IranDNS, cfg.GlobalDNS, slog, cfg.IranTLDs, cfg.HijackIPs, cfg.HijackRanges, cfg.PreferIranDomains, uint32(cfg.MinTTL), uint32(cfg.MaxTTL), m)

	r.SetTimeout(time.Duration(cfg.Timeout) * time.Second)

	var u *updater.Updater
	if cfg.IranRangesURL != "" {
		interval, err := time.ParseDuration(cfg.IranRangesUpdateInterval)
		if err != nil {
			slog.Warn("invalid update interval, using default 24h", "error", err)
			interval = 24 * time.Hour
		}
		u = updater.New(cfg.IranRangesURL, cfg.IranRanges, interval, c, slog, m, cfg.StateFile)
		u.Start()
	}

	s.StartCleanup(24*time.Hour, func(domain string) bool {
		return r.ValidateDomain(domain)
	})

	dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		if len(req.Question) == 0 {
			w.WriteMsg(resolver.ServerFail(req))
			return
		}

		domain := req.Question[0].Name
		start := time.Now()

		resp := r.Resolve(req)
		if resp == nil {
			resp = resolver.ServerFail(req)
		}
		w.WriteMsg(resp)

		slog.Info("query",
			"domain", domain,
			"rcode", resp.Rcode,
			"answers", len(resp.Answer),
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})

	udpServer := &dns.Server{Addr: cfg.Listen, Net: "udp"}
	tcpServer := &dns.Server{Addr: cfg.Listen, Net: "tcp"}

	go func() {
		slog.Info("udp server started", "addr", cfg.Listen)
		if err := udpServer.ListenAndServe(); err != nil {
			slog.Error("udp server error", "error", err)
			os.Exit(1)
		}
	}()

	go func() {
		slog.Info("tcp server started", "addr", cfg.Listen)
		if err := tcpServer.ListenAndServe(); err != nil {
			slog.Error("tcp server error", "error", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down...")

	udpServer.Shutdown()
	tcpServer.Shutdown()
	metricsSrv.Shutdown(context.Background())

	r.Stop()
	s.Stop()
	if u != nil {
		u.Stop()
	}

	slog.Info("bye")
	closeLog()
}

func ensureLogDir(logFile string) error {
	if logFile == "" {
		return nil
	}
	dir := filepath.Dir(logFile)
	return os.MkdirAll(dir, 0755)
}

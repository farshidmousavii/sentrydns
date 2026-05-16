package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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

	r := resolver.New(c, s, cfg.IranDNS, cfg.GlobalDNS, slog, cfg.IranTLDs, cfg.HijackIPs, cfg.HijackRanges, cfg.PreferIranDomains, uint32(cfg.MinTTL), uint32(cfg.MaxTTL), m, cfg.GlobalDNSFallback)

	r.SetTimeout(time.Duration(cfg.Timeout) * time.Second)
	r.SetGlobalTimeout(time.Duration(cfg.GlobalDNSTimeout * float64(time.Second)))

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

	cleanupDelay, cleanupNext := parseCleanupSchedule(cfg.CleanupSchedule, slog)
	if cleanupNext == nil {
		var err error
		cleanupDelay, err = time.ParseDuration(cfg.CleanupInitialDelay)
		if err != nil {
			slog.Warn("invalid cleanup_initial_delay, using default 1h", "error", err)
			cleanupDelay = 1 * time.Hour
		}
	}

	s.StartCleanup(cleanupDelay, cfg.CleanupQPS, r.ValidateDomain, r.IranDNSHealthy, cleanupNext)

	dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "recover", rec)
				w.WriteMsg(resolver.ServerFail(req))
			}
		}()

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
		}
	}()

	go func() {
		slog.Info("tcp server started", "addr", cfg.Listen)
		if err := tcpServer.ListenAndServe(); err != nil {
			slog.Error("tcp server error", "error", err)
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

func parseCleanupSchedule(schedule string, log *slog.Logger) (time.Duration, func() time.Duration) {
	if schedule == "" {
		return 0, nil
	}
	parts := strings.Split(schedule, ":")
	if len(parts) != 2 {
		log.Warn("invalid cleanup_schedule format, expected HH:MM", "schedule", schedule)
		return 0, nil
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		log.Warn("invalid cleanup_schedule hour", "schedule", schedule)
		return 0, nil
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		log.Warn("invalid cleanup_schedule minute", "schedule", schedule)
		return 0, nil
	}

	nextCleanup := func() time.Duration {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		return next.Sub(now)
	}

	log.Info("cleanup scheduled", "at", fmt.Sprintf("%02d:%02d", hour, min), "next", nextCleanup().Round(time.Second))
	return nextCleanup(), nextCleanup
}

func ensureLogDir(logFile string) error {
	if logFile == "" {
		return nil
	}
	dir := filepath.Dir(logFile)
	return os.MkdirAll(dir, 0755)
}

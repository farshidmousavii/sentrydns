package config

import (
	"os"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfig(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen != ":53" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":53")
	}
	if cfg.MinTTL != 300 {
		t.Errorf("MinTTL = %d, want %d", cfg.MinTTL, 300)
	}
	if cfg.MaxTTL != 3600 {
		t.Errorf("MaxTTL = %d, want %d", cfg.MaxTTL, 3600)
	}
	if cfg.IranRanges != "data/iran-ranges.txt" {
		t.Errorf("IranRanges = %q, want %q", cfg.IranRanges, "data/iran-ranges.txt")
	}
	if cfg.Learned != "data/learned.conf" {
		t.Errorf("Learned = %q, want %q", cfg.Learned, "data/learned.conf")
	}
	if cfg.Timeout != 3 {
		t.Errorf("Timeout = %d, want %d", cfg.Timeout, 3)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want %q", cfg.LogFormat, "json")
	}
	if cfg.LogFile != "/var/log/sentrydns/sentrydns.log" {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, "/var/log/sentrydns/sentrydns.log")
	}
	if len(cfg.IranTLDs) != 2 || cfg.IranTLDs[0] != "ir" {
		t.Errorf("IranTLDs = %v, want [ir ایران]", cfg.IranTLDs)
	}
	if len(cfg.HijackIPs) != 3 {
		t.Errorf("HijackIPs = %v, want 3 entries", cfg.HijackIPs)
	}
	if len(cfg.HijackRanges) != 1 || cfg.HijackRanges[0] != "50.7.0.0/16" {
		t.Errorf("HijackRanges = %v, want [50.7.0.0/16]", cfg.HijackRanges)
	}
	if len(cfg.PreferIranDomains) != 0 {
		t.Errorf("PreferIranDomains = %v, want empty", cfg.PreferIranDomains)
	}
}

func TestLoadPartialConfig(t *testing.T) {
	yaml := `
listen: ":9999"
min_ttl: 600
timeout: 5
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen != ":9999" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":9999")
	}
	if cfg.MinTTL != 600 {
		t.Errorf("MinTTL = %d, want %d", cfg.MinTTL, 600)
	}
	if cfg.Timeout != 5 {
		t.Errorf("Timeout = %d, want %d", cfg.Timeout, 5)
	}
	if cfg.MaxTTL != 3600 {
		t.Errorf("MaxTTL = %d, want %d", cfg.MaxTTL, 3600)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadFullConfig(t *testing.T) {
	yaml := `
iran_dns: "10.0.0.1"
global_dns: "10.0.0.2"
listen: ":53"
min_ttl: 100
max_ttl: 5000
iran_ranges: "/etc/dns/iran.txt"
learned: "/var/dns/learned.conf"
timeout: 5
log_level: "debug"
log_format: "text"
log_file: "/tmp/test.log"
iran_tlds:
  - "ir"
  - "ایران"
hijack_ips:
  - "10.0.0.1"
prefer_iran_domains:
  - "example.com"
iran_ranges_url: "http://example.com/ranges.txt"
iran_ranges_update_interval: "12h"
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.IranDNS != "10.0.0.1" {
		t.Errorf("IranDNS = %q", cfg.IranDNS)
	}
	if cfg.IranRangesURL != "http://example.com/ranges.txt" {
		t.Errorf("IranRangesURL = %q", cfg.IranRangesURL)
	}
	if len(cfg.PreferIranDomains) != 1 || cfg.PreferIranDomains[0] != "example.com" {
		t.Errorf("PreferIranDomains = %v, want [example.com]", cfg.PreferIranDomains)
	}
	if cfg.IranRangesUpdateInterval != "12h" {
		t.Errorf("IranRangesUpdateInterval = %q", cfg.IranRangesUpdateInterval)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

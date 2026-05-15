package config

import (
	"errors"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	IranDNS                  string   `yaml:"iran_dns"`
	GlobalDNS                string   `yaml:"global_dns"`
	Listen                   string   `yaml:"listen"`
	MinTTL                   int      `yaml:"min_ttl"`
	MaxTTL                   int      `yaml:"max_ttl"`
	IranRanges               string   `yaml:"iran_ranges"`
	Learned                  string   `yaml:"learned"`
	Timeout                  int      `yaml:"timeout"`
	LogLevel                 string   `yaml:"log_level"`
	LogFormat                string   `yaml:"log_format"`
	LogFile                  string   `yaml:"log_file"`
	MetricsAddr              string   `yaml:"metrics_addr"`
	IranTLDs                 []string `yaml:"iran_tlds"`
	HijackIPs                []string `yaml:"hijack_ips"`
	HijackRanges             []string `yaml:"hijack_ranges"`
	PreferIranDomains        []string `yaml:"prefer_iran_domains"`
	IranRangesURL            string   `yaml:"iran_ranges_url"`
	IranRangesUpdateInterval string   `yaml:"iran_ranges_update_interval"`
	StateFile                string   `yaml:"state_file"`
}

func defaultConfig() Config {
	return Config{
		Listen:                   ":53",
		MinTTL:                   300,
		MaxTTL:                   3600,
		IranRanges:               "data/iran-ranges.txt",
		Learned:                  "data/learned.conf",
		Timeout:                  3,
		LogLevel:                 "info",
		LogFormat:                "json",
		LogFile:                  "/var/log/sentrydns/sentrydns.log",
		MetricsAddr:              ":9153",
		IranTLDs:                 []string{"ir", "ایران"},
		HijackIPs:                []string{"10.10.34.34", "10.10.34.35", "10.10.34.36"},
		HijackRanges:             []string{"50.7.0.0/16"},
		IranRangesURL:            "https://raw.githubusercontent.com/farshidmousavii/iran-ip/main/ipv4.txt",
		IranRangesUpdateInterval: "24h",
		StateFile:                "data/.sentrydns-state",
	}
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := defaultConfig()
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil && err != io.EOF {
		return nil, err
	}

	return &cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	if c.IranDNS == "" {
		return errors.New("iran_dns is required")
	}
	if c.GlobalDNS == "" {
		return errors.New("global_dns is required")
	}
	if c.Listen == "" {
		return errors.New("listen is required")
	}
	if c.IranRanges == "" {
		return errors.New("iran_ranges is required")
	}
	if c.Learned == "" {
		return errors.New("learned is required")
	}
	if c.MetricsAddr == "" {
		return errors.New("metrics_addr is required")
	}
	if c.MinTTL <= 0 {
		return errors.New("min_ttl must be positive")
	}
	if c.MaxTTL <= 0 {
		return errors.New("max_ttl must be positive")
	}
	if c.MinTTL > c.MaxTTL {
		return errors.New("min_ttl must not exceed max_ttl")
	}
	if c.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

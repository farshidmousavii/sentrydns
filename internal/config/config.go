package config

import (
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

	return &cfg, nil
}

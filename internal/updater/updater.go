package updater

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/classifier"
)

type Updater struct {
	url        string
	filePath   string
	interval   time.Duration
	classifier *classifier.Classifier
	log        *slog.Logger
	stop       chan struct{}
	client     *http.Client
}

func New(url, filePath string, interval time.Duration, c *classifier.Classifier, log *slog.Logger) *Updater {
	return &Updater{
		url:        url,
		filePath:   filePath,
		interval:   interval,
		classifier: c,
		log:        log,
		stop:       make(chan struct{}),
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (u *Updater) Start() {
	go func() {
		u.update()

		ticker := time.NewTicker(u.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				u.update()
			case <-u.stop:
				return
			}
		}
	}()
}

func (u *Updater) Stop() {
	close(u.stop)
}

func (u *Updater) update() {
	u.log.Info("updating iran-ranges...", "url", u.url)

	resp, err := u.client.Get(u.url)
	if err != nil {
		u.log.Error("failed to download iran-ranges", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		u.log.Error("bad response", "status", resp.StatusCode)
		return
	}

	tmp := u.filePath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		u.log.Error("failed to create temp file", "error", err)
		return
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		u.log.Error("failed to write temp file", "error", err)
		return
	}
	f.Close()

	if !containsValidCIDR(tmp) {
		os.Remove(tmp)
		u.log.Error("downloaded file contains no valid CIDR ranges")
		return
	}

	if err := os.Rename(tmp, u.filePath); err != nil {
		u.log.Error("failed to replace file", "error", err)
		return
	}

	if err := u.classifier.Reload(u.filePath); err != nil {
		u.log.Error("failed to reload classifier", "error", err)
		return
	}

	u.log.Info("iran-ranges updated successfully")
}

func containsValidCIDR(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		if _, _, err := net.ParseCIDR(line); err == nil {
			return true
		}
	}
	return false
}

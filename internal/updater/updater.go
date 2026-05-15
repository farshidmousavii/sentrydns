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
	"github.com/farshidmousavii/sentrydns/internal/metrics"
	"github.com/farshidmousavii/sentrydns/internal/state"
)

type Updater struct {
	url        string
	filePath   string
	statePath  string
	interval   time.Duration
	classifier *classifier.Classifier
	log        *slog.Logger
	stop       chan struct{}
	client     *http.Client
	metrics    *metrics.Metrics
}

func New(url, filePath string, interval time.Duration, c *classifier.Classifier, log *slog.Logger, m *metrics.Metrics, statePath string) *Updater {
	return &Updater{
		url:        url,
		filePath:   filePath,
		statePath:  statePath,
		interval:   interval,
		classifier: c,
		log:        log,
		stop:       make(chan struct{}),
		client:     &http.Client{Timeout: 30 * time.Second},
		metrics:    m,
	}
}

func (u *Updater) Start() {
	remaining := u.scheduleFromMtime()

	go func() {
		if remaining > 0 {
			u.log.Info("update deferred", "remaining", remaining.Round(time.Second))
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
				u.update()
			case <-u.stop:
				timer.Stop()
				return
			}
		} else {
			u.update()
		}

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

func (u *Updater) scheduleFromMtime() time.Duration {
	fi, err := os.Stat(u.filePath)
	if err != nil {
		u.metrics.LastUpdateSuccess.Store(false)
		return -1
	}

	mtime := fi.ModTime()
	u.metrics.LastUpdateTime.Store(mtime)
	u.metrics.LastUpdateSuccess.Store(true)

	elapsed := time.Since(mtime)
	if elapsed >= u.interval {
		return -1
	}
	return u.interval - elapsed
}

func (u *Updater) Stop() {
	close(u.stop)
}

func (u *Updater) update() {
	u.log.Info("updating iran-ranges...", "url", u.url)

	resp, err := u.client.Get(u.url)
	if err != nil {
		u.log.Error("failed to download iran-ranges", "error", err)
		u.setUpdateSuccess(false)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		u.log.Error("bad response", "status", resp.StatusCode)
		u.setUpdateSuccess(false)
		return
	}

	tmp := u.filePath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		u.log.Error("failed to create temp file", "error", err)
		u.setUpdateSuccess(false)
		return
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		u.log.Error("failed to write temp file", "error", err)
		u.setUpdateSuccess(false)
		return
	}
	f.Close()

	if !containsValidCIDR(tmp) {
		os.Remove(tmp)
		u.log.Error("downloaded file contains no valid CIDR ranges")
		u.setUpdateSuccess(false)
		return
	}

	if err := os.Rename(tmp, u.filePath); err != nil {
		u.log.Error("failed to replace file", "error", err)
		u.setUpdateSuccess(false)
		return
	}

	if err := u.classifier.Reload(u.filePath); err != nil {
		u.log.Error("failed to reload classifier", "error", err)
		u.setUpdateSuccess(false)
		return
	}

	u.log.Info("iran-ranges updated successfully")
	u.metrics.LastUpdateTime.Store(time.Now())
	u.metrics.LastUpdateSuccess.Store(true)
	if u.statePath != "" {
		st := state.Load(u.statePath)
		st.LastUpdateUnix = time.Now().Unix()
		st.LastUpdateSuccess = true
		state.Save(u.statePath, st)
	}
}

func (u *Updater) setUpdateSuccess(success bool) {
	u.metrics.LastUpdateSuccess.Store(success)
	if u.statePath != "" {
		st := state.Load(u.statePath)
		st.LastUpdateSuccess = success
		if success {
			st.LastUpdateUnix = time.Now().Unix()
		}
		state.Save(u.statePath, st)
	}
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

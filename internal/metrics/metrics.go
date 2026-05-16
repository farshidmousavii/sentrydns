package metrics

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/state"
)

type Metrics struct {
	QueriesTotal    atomic.Int64
	QueriesIran     atomic.Int64
	QueriesGlobal   atomic.Int64
	QueriesRetried atomic.Int64
	QueriesCached   atomic.Int64
	CacheMiss       atomic.Int64
	LearnedTotal    atomic.Int64
	LearnedToday    atomic.Int64

	// routing path distribution
	PathTLD        atomic.Int64
	PathPreferIran atomic.Int64
	PathStore      atomic.Int64
	PathLearn      atomic.Int64

	// per-upstream latency & errors
	IranLatencyTotal   atomic.Int64
	IranQueryCount     atomic.Int64
	IranTimeouts       atomic.Int64
	GlobalLatencyTotal atomic.Int64
	GlobalQueryCount   atomic.Int64
	GlobalTimeouts     atomic.Int64
	TcpFallbackCount   atomic.Int64
	GlobalFallbackCount atomic.Int64
	GlobalFallbackLatencyTotal atomic.Int64

	// store operations
	StoreRemoved atomic.Int64
	StoreCleaned atomic.Int64

	// in-flight gauge
	InFlightQueries atomic.Int64

	// iran-ranges update
	LastUpdateTime    atomic.Value // time.Time
	LastUpdateSuccess atomic.Bool

	startTime time.Time
	statePath string
}

func New() *Metrics {
	m := &Metrics{
		startTime: time.Now(),
	}
	go m.resetDailyStats()
	return m
}

func (m *Metrics) RestoreFromFile(path string) {
	m.statePath = path
	st := state.Load(path)
	m.LearnedTotal.Store(st.LearnedTotalCount)
	if st.LearnedTodayDate == time.Now().Format("2006-01-02") {
		m.LearnedToday.Store(st.LearnedTodayCount)
	}
	if st.LastUpdateUnix > 0 {
		m.LastUpdateTime.Store(time.Unix(st.LastUpdateUnix, 0))
	}
	m.LastUpdateSuccess.Store(st.LastUpdateSuccess)
}

func (m *Metrics) resetDailyStats() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		m.LearnedToday.Store(0)
		if m.statePath != "" {
			state.Update(m.statePath, func(st *state.State) {
				st.LearnedTodayDate = time.Now().Format("2006-01-02")
				st.LearnedTodayCount = 0
			})
		}
	}
}

func (m *Metrics) Uptime() string {
	d := time.Since(m.startTime).Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

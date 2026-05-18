package metrics

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/state"
)

type Metrics struct {
	QueriesTotal             atomic.Int64
	QueriesIran              atomic.Int64
	QueriesGlobal            atomic.Int64
	QueriesRetried           atomic.Int64
	QueriesServfail          atomic.Int64
	QueriesCached            atomic.Int64
	CacheMiss                atomic.Int64
	LearnedTotal             atomic.Int64
	LearnedTotalAtMidnight   atomic.Int64

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

	// rate limiting
	QueriesRateLimited atomic.Int64

	// circuit breaker
	IranCBSkipped atomic.Int64
	IranCBTrips   atomic.Int64
	IranCBOpen    atomic.Int64 // gauge: 1 = open/half-open, 0 = closed

	// iran-ranges update
	LastUpdateTime    atomic.Value // time.Time
	LastUpdateSuccess atomic.Bool

	startTime time.Time
	statePath string
	stop      chan struct{}
}

func (m *Metrics) LearnedTodayValue() int64 {
	midnight := m.LearnedTotalAtMidnight.Load()
	total := m.LearnedTotal.Load()
	if total < midnight {
		return 0
	}
	return total - midnight
}

func New() *Metrics {
	m := &Metrics{
		startTime: time.Now(),
		stop:      make(chan struct{}),
	}
	go m.resetDailyStats()
	return m
}

func (m *Metrics) RestoreFromFile(path string) {
	m.statePath = path
	st := state.Load(path)
	m.LearnedTotal.Store(st.LearnedTotalCount)

	today := time.Now().Format("2006-01-02")
	if st.LearnedTodayDate == today && st.LearnedTotalAtMidnight > 0 {
		m.LearnedTotalAtMidnight.Store(st.LearnedTotalAtMidnight)
	} else if st.LearnedTodayDate == today {
		midnight := st.LearnedTotalCount - st.LearnedTodayCount
		if midnight < 0 || midnight > st.LearnedTotalCount {
			midnight = st.LearnedTotalCount
		}
		m.LearnedTotalAtMidnight.Store(midnight)
	} else {
		m.LearnedTotalAtMidnight.Store(st.LearnedTotalCount)
	}

	if st.LastUpdateUnix > 0 {
		m.LastUpdateTime.Store(time.Unix(st.LastUpdateUnix, 0))
	}
	m.LastUpdateSuccess.Store(st.LastUpdateSuccess)
}

func (m *Metrics) resetDailyStats() {
	now := time.Now()
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	delay := nextMidnight.Sub(now)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			total := m.LearnedTotal.Load()
			m.LearnedTotalAtMidnight.Store(total)
			if m.statePath != "" {
				state.Update(m.statePath, func(st *state.State) {
					st.LearnedTodayDate = time.Now().Format("2006-01-02")
					st.LearnedTodayCount = 0
					st.LearnedTotalAtMidnight = total
				})
			}
			timer.Reset(24 * time.Hour)
		case <-m.stop:
			return
		}
	}
}

func (m *Metrics) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
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

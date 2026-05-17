package metrics

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type response struct {
	Uptime            string `json:"uptime"`
	StartTime         string `json:"start_time"`
	QueriesTotal      int64  `json:"queries_total"`
	QueriesIran       int64  `json:"queries_iran"`
	QueriesGlobal     int64  `json:"queries_global"`
	QueriesRetried    int64  `json:"queries_retried"`
	QueriesServfail   int64  `json:"queries_servfail"`
	QueriesCached     int64  `json:"queries_cached"`
	CacheMiss         int64  `json:"cache_miss"`
	CacheHitRatio     string `json:"cache_hit_ratio"`
	LearnedTotal      int64  `json:"learned_total"`
	LearnedToday      int64  `json:"learned_today"`
	StoreSize         int64  `json:"store_size"`
	StoreRemoved      int64  `json:"store_removed"`
	StoreCleaned      int64  `json:"store_cleaned"`
	LastUpdateTime    string `json:"last_update_time"`
	LastUpdateSuccess bool   `json:"last_update_success"`
	LastUpdateAgo     string `json:"last_update_ago"`
	PathTLD           int64  `json:"path_tld"`
	PathPreferIran    int64  `json:"path_prefer_iran"`
	PathStore         int64  `json:"path_store"`
	PathLearn         int64  `json:"path_learn"`
	IranAvgLatencyMs  int64  `json:"iran_avg_latency_ms"`
	IranTimeouts      int64  `json:"iran_timeouts"`
	GlobalAvgLatencyMs int64 `json:"global_avg_latency_ms"`
	GlobalTimeouts    int64 `json:"global_timeouts"`
	GlobalFallbackCount int64 `json:"global_fallback_count"`
	GlobalFallbackAvgLatencyMs int64 `json:"global_fallback_avg_latency_ms"`
	TcpFallbackCount  int64 `json:"tcp_fallback_count"`
	InFlightQueries   int64 `json:"in_flight_queries"`
	QueriesRateLimited int64 `json:"queries_rate_limited"`
	IranCBSkipped     int64 `json:"iran_cb_skipped"`
	IranCBTrips       int64 `json:"iran_cb_trips"`
	IranCBOpen        int64 `json:"iran_cb_open"`
}

func (m *Metrics) MetricsHandler(storeSize func() int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total := m.QueriesTotal.Load()
		cached := m.QueriesCached.Load()

		hitRatio := "0%"
		if total > 0 {
			hitRatio = fmt.Sprintf("%.1f%%", float64(cached)/float64(total)*100)
		}

		lastUpdate := "never"
		lastUpdateAgo := "never"
		if t, ok := m.LastUpdateTime.Load().(time.Time); ok && !t.IsZero() {
			lastUpdate = t.Format("2006-01-02 15:04:05")
			lastUpdateAgo = time.Since(t).Round(time.Minute).String()
		}

		var iranAvg int64
		if qc := m.IranQueryCount.Load(); qc > 0 {
			iranAvg = (m.IranLatencyTotal.Load() / qc) / int64(time.Millisecond)
		}
		var globalAvg int64
		if qc := m.GlobalQueryCount.Load(); qc > 0 {
			globalAvg = (m.GlobalLatencyTotal.Load() / qc) / int64(time.Millisecond)
		}
		var globalFallbackAvg int64
		if fc := m.GlobalFallbackCount.Load(); fc > 0 {
			globalFallbackAvg = (m.GlobalFallbackLatencyTotal.Load() / fc) / int64(time.Millisecond)
		}

		resp := response{
			Uptime:            m.Uptime(),
			StartTime:         m.startTime.Format("2006-01-02 15:04:05"),
			QueriesTotal:      total,
			QueriesIran:       m.QueriesIran.Load(),
			QueriesGlobal:     m.QueriesGlobal.Load(),
			QueriesRetried:    m.QueriesRetried.Load(),
			QueriesServfail:   m.QueriesServfail.Load(),
			QueriesCached:     cached,
			CacheMiss:         m.CacheMiss.Load(),
			CacheHitRatio:     hitRatio,
			LearnedTotal:      m.LearnedTotal.Load(),
			LearnedToday:      m.LearnedToday.Load(),
			StoreSize:         storeSize(),
			StoreRemoved:      m.StoreRemoved.Load(),
			StoreCleaned:      m.StoreCleaned.Load(),
			LastUpdateTime:    lastUpdate,
			LastUpdateSuccess: m.LastUpdateSuccess.Load(),
			LastUpdateAgo:     lastUpdateAgo,
			PathTLD:           m.PathTLD.Load(),
			PathPreferIran:    m.PathPreferIran.Load(),
			PathStore:         m.PathStore.Load(),
			PathLearn:         m.PathLearn.Load(),
			IranAvgLatencyMs:  iranAvg,
			IranTimeouts:      m.IranTimeouts.Load(),
			GlobalAvgLatencyMs: globalAvg,
			GlobalTimeouts:    m.GlobalTimeouts.Load(),
			GlobalFallbackCount: m.GlobalFallbackCount.Load(),
			GlobalFallbackAvgLatencyMs: globalFallbackAvg,
			TcpFallbackCount:  m.TcpFallbackCount.Load(),
		InFlightQueries:   m.InFlightQueries.Load(),
		QueriesRateLimited: m.QueriesRateLimited.Load(),
		IranCBSkipped:     m.IranCBSkipped.Load(),
		IranCBTrips:       m.IranCBTrips.Load(),
		IranCBOpen:        m.IranCBOpen.Load(),
	}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func (m *Metrics) HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"uptime": m.Uptime(),
	})
}

func (m *Metrics) StartServer(addr string, storeSize func() int64) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", m.MetricsHandler(storeSize))
	mux.HandleFunc("/health", m.HealthHandler)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server error", "error", err)
		}
	}()
	return srv
}

package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// prometheusHandler exposes all metrics in the Prometheus text exposition
// format (0.0.4) at /metrics/prom. The JSON /metrics endpoint is unchanged.
//
// Naming follows Prometheus conventions:
//   - counters end in _total
//   - gauges carry the semantic unit (seconds, ratio)
//   - avg latencies are gauges in seconds (base SI unit)

type promMetric struct {
	help string
	typ  string // counter | gauge
}

var promDefs = []promMetric{
	{"Total queries received", "counter"},
	{"Queries routed via Iranian TLD path", "counter"},
	{"Queries routed via prefer-iran path", "counter"},
	{"Queries routed via learned-store path", "counter"},
	{"Queries routed via learning path", "counter"},
	{"Queries answered from static records", "counter"},
	{"Queries served from cache", "counter"},
	{"Cache misses", "counter"},
	{"Queries retried after SERVFAIL", "counter"},
	{"Queries answered SERVFAIL", "counter"},
	{"Queries denied by per-client rate limit", "counter"},
	{"Queries denied by global QPS cap", "counter"},
	{"Queries refused by loop detection", "counter"},
	{"Domains learned (cumulative)", "counter"},
	{"Learned domains removed", "counter"},
	{"Learned domains cleaned", "counter"},
	{"IranDNS timeouts", "counter"},
	{"GlobalDNS timeouts", "counter"},
	{"TCP fallback queries", "counter"},
	{"GlobalDNS fallback queries", "counter"},
	{"Iran circuit breaker skipped queries", "counter"},
	{"Iran circuit breaker trips", "counter"},
	{"Global circuit breaker skipped queries", "counter"},
	{"Global circuit breaker trips", "counter"},
	{"ShortWait expired before GlobalDNS responded", "counter"},
}

var promGaugeDefs = []promMetric{
	{"Process uptime", "gauge"},
	{"Process start time (unix seconds)", "gauge"},
	{"Cache hit ratio (0-1)", "gauge"},
	{"Domains learned today", "gauge"},
	{"Learned store size", "gauge"},
	{"Last Iran ranges update (unix seconds, 0 = never)", "gauge"},
	{"Last Iran ranges update succeeded (1 = yes)", "gauge"},
	{"IranDNS average latency (seconds)", "gauge"},
	{"GlobalDNS average latency (seconds)", "gauge"},
	{"GlobalDNS fallback average latency (seconds)", "gauge"},
	{"Queries currently in flight", "gauge"},
	{"Iran circuit breaker open (1 = open)", "gauge"},
	{"Global circuit breaker open (1 = open)", "gauge"},
}

func (m *Metrics) PrometheusHandler(storeSize func() int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder

		counterNames := []string{
			"sentrydns_queries_total",
			"sentrydns_path_tld_total",
			"sentrydns_path_prefer_iran_total",
			"sentrydns_path_store_total",
			"sentrydns_path_learn_total",
			"sentrydns_path_static_total",
			"sentrydns_queries_cached_total",
			"sentrydns_cache_miss_total",
			"sentrydns_queries_retried_total",
			"sentrydns_queries_servfail_total",
			"sentrydns_queries_rate_limited_total",
			"sentrydns_queries_global_limited_total",
			"sentrydns_loop_detections_total",
			"sentrydns_learned_total",
			"sentrydns_store_removed_total",
			"sentrydns_store_cleaned_total",
			"sentrydns_iran_timeouts_total",
			"sentrydns_global_timeouts_total",
			"sentrydns_tcp_fallback_total",
			"sentrydns_global_fallback_total",
			"sentrydns_iran_cb_skipped_total",
			"sentrydns_iran_cb_trips_total",
			"sentrydns_global_cb_skipped_total",
			"sentrydns_global_cb_trips_total",
			"sentrydns_short_wait_expired_total",
		}
		counterValues := []int64{
			m.QueriesTotal.Load(),
			m.PathTLD.Load(),
			m.PathPreferIran.Load(),
			m.PathStore.Load(),
			m.PathLearn.Load(),
			m.PathStatic.Load(),
			m.QueriesCached.Load(),
			m.CacheMiss.Load(),
			m.QueriesRetried.Load(),
			m.QueriesServfail.Load(),
			m.QueriesRateLimited.Load(),
			m.QueriesGlobalLimited.Load(),
			m.LoopDetections.Load(),
			m.LearnedTotal.Load(),
			m.StoreRemoved.Load(),
			m.StoreCleaned.Load(),
			m.IranTimeouts.Load(),
			m.GlobalTimeouts.Load(),
			m.TcpFallbackCount.Load(),
			m.GlobalFallbackCount.Load(),
			m.IranCBSkipped.Load(),
			m.IranCBTrips.Load(),
			m.GlobalCBSkipped.Load(),
			m.GlobalCBTrips.Load(),
			m.ShortWaitExpired.Load(),
		}

		total := m.QueriesTotal.Load()
		cached := m.QueriesCached.Load()
		hitRatio := 0.0
		if total > 0 {
			hitRatio = float64(cached) / float64(total)
		}

		lastUpdate := int64(0)
		if t, ok := m.LastUpdateTime.Load().(time.Time); ok && !t.IsZero() {
			lastUpdate = t.Unix()
		}
		lastUpdateSuccess := int64(0)
		if m.LastUpdateSuccess.Load() {
			lastUpdateSuccess = 1
		}

		var iranAvg, globalAvg, fallbackAvg float64
		if qc := m.IranQueryCount.Load(); qc > 0 {
			iranAvg = float64(m.IranLatencyTotal.Load()) / float64(qc) / float64(time.Second)
		}
		if qc := m.GlobalQueryCount.Load(); qc > 0 {
			globalAvg = float64(m.GlobalLatencyTotal.Load()) / float64(qc) / float64(time.Second)
		}
		if fc := m.GlobalFallbackCount.Load(); fc > 0 {
			fallbackAvg = float64(m.GlobalFallbackLatencyTotal.Load()) / float64(fc) / float64(time.Second)
		}

		gaugeNames := []string{
			"sentrydns_uptime_seconds",
			"sentrydns_start_time_seconds",
			"sentrydns_cache_hit_ratio",
			"sentrydns_learned_today",
			"sentrydns_store_size",
			"sentrydns_last_update_timestamp_seconds",
			"sentrydns_last_update_success",
			"sentrydns_iran_avg_latency_seconds",
			"sentrydns_global_avg_latency_seconds",
			"sentrydns_global_fallback_avg_latency_seconds",
			"sentrydns_in_flight_queries",
			"sentrydns_iran_cb_open",
			"sentrydns_global_cb_open",
		}
		gaugeValues := []float64{
			time.Since(m.startTime).Seconds(),
			float64(m.startTime.Unix()),
			hitRatio,
			float64(m.LearnedTodayValue()),
			float64(storeSize()),
			float64(lastUpdate),
			float64(lastUpdateSuccess),
			iranAvg,
			globalAvg,
			fallbackAvg,
			float64(m.InFlightQueries.Load()),
			float64(m.IranCBOpen.Load()),
			float64(m.GlobalCBOpen.Load()),
		}

		for i, def := range promDefs {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
				counterNames[i], def.help, counterNames[i], counterNames[i], counterValues[i])
		}
		for i, def := range promGaugeDefs {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
				gaugeNames[i], def.help, gaugeNames[i], gaugeNames[i], formatFloat(gaugeValues[i]))
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}

// formatFloat renders a float without trailing noise (e.g. "0" not "0.000000").
func formatFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}

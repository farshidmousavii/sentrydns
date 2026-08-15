package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func prometheusBody(t *testing.T, m *Metrics) string {
	t.Helper()
	handler := m.PrometheusHandler(func() int64 { return 300 })
	req := httptest.NewRequest("GET", "/metrics/prom", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prometheus format", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestPrometheusHandlerCounterValues(t *testing.T) {
	m := New()
	m.QueriesTotal.Store(42)
	m.QueriesIran.Store(20)
	m.QueriesGlobal.Store(20)
	m.QueriesCached.Store(10)
	m.CacheMiss.Store(32)
	m.QueriesRateLimited.Store(3)
	m.QueriesGlobalLimited.Store(7)
	m.LoopDetections.Store(1)
	m.PathTLD.Store(8)
	m.PathPreferIran.Store(3)
	m.PathStore.Store(15)
	m.PathLearn.Store(16)
	m.QueriesServfail.Store(5)
	m.QueriesRetried.Store(2)

	body := prometheusBody(t, m)

	tests := []struct {
		line string
		want string
	}{
		{"sentrydns_queries_total ", "sentrydns_queries_total 42"},
		{"sentrydns_queries_cached_total ", "sentrydns_queries_cached_total 10"},
		{"sentrydns_cache_miss_total ", "sentrydns_cache_miss_total 32"},
		{"sentrydns_queries_rate_limited_total ", "sentrydns_queries_rate_limited_total 3"},
		{"sentrydns_queries_global_limited_total ", "sentrydns_queries_global_limited_total 7"},
		{"sentrydns_loop_detections_total ", "sentrydns_loop_detections_total 1"},
		{"sentrydns_path_tld_total ", "sentrydns_path_tld_total 8"},
		{"sentrydns_queries_servfail_total ", "sentrydns_queries_servfail_total 5"},
	}
	for _, tt := range tests {
		if !strings.Contains(body, "\n"+tt.want+"\n") && !strings.Contains(body, tt.want+"\n") {
			t.Errorf("missing metric line %q in:\n%s", tt.want, body)
		}
	}

	// HELP and TYPE lines present
	for _, want := range []string{
		"# HELP sentrydns_queries_total",
		"# TYPE sentrydns_queries_total counter",
		"# TYPE sentrydns_loop_detections_total counter",
		"# TYPE sentrydns_uptime_seconds gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in prometheus output", want)
		}
	}
}

func TestPrometheusHandlerGauges(t *testing.T) {
	m := New()
	m.LearnedTotal.Store(500)
	m.LearnedTotalAtMidnight.Store(485)
	m.LastUpdateTime.Store(time.Unix(1000000, 0))
	m.LastUpdateSuccess.Store(true)
	m.IranLatencyTotal.Store(500_000_000) // 500ms total / 10 queries = 50ms
	m.IranQueryCount.Store(10)
	m.InFlightQueries.Store(4)
	m.IranCBOpen.Store(1)

	body := prometheusBody(t, m)

	tests := []struct {
		want string
	}{
		{"sentrydns_learned_today 15"},
		{"sentrydns_store_size 300"},
		{"sentrydns_last_update_timestamp_seconds 1000000"},
		{"sentrydns_last_update_success 1"},
		{"sentrydns_iran_avg_latency_seconds 0.05"},
		{"sentrydns_in_flight_queries 4"},
		{"sentrydns_iran_cb_open 1"},
		{"sentrydns_cache_hit_ratio 0"},
	}
	for _, tt := range tests {
		if !strings.Contains(body, "\n"+tt.want+"\n") && !strings.Contains(body, tt.want+"\n") {
			t.Errorf("missing gauge line %q in:\n%s", tt.want, body)
		}
	}
}

func TestPrometheusHandlerZeroState(t *testing.T) {
	m := New()
	body := prometheusBody(t, m)

	// zero state must not crash and must emit valid lines
	if !strings.Contains(body, "sentrydns_queries_total 0\n") {
		t.Errorf("expected zero-valued queries_total in:\n%s", body)
	}
	if strings.Contains(body, "\n\n\n") {
		t.Error("prometheus output contains blank lines between metrics")
	}
	// cache hit ratio should be 0, not NaN
	if !strings.Contains(body, "sentrydns_cache_hit_ratio 0\n") {
		t.Errorf("expected cache_hit_ratio 0, got:\n%s", body)
	}
}

func TestPrometheusHandlerRegistered(t *testing.T) {
	m := New()
	var storeSize atomic.Int64
	storeSize.Store(100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics/prom":
			m.PrometheusHandler(func() int64 { return storeSize.Load() })(w, r)
		}
	}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics/prom")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sentrydns_store_size 100") {
		t.Errorf("expected store_size 100, got:\n%s", body)
	}
}

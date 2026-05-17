package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farshidmousavii/sentrydns/internal/state"
)

func TestNew(t *testing.T) {
	m := New()
	if m == nil {
		t.Fatal("New() returned nil")
	}
	if m.Uptime() == "" {
		t.Error("Uptime should not be empty")
	}
}

func TestQueryCounters(t *testing.T) {
	m := New()
	m.QueriesTotal.Add(10)
	m.QueriesIran.Add(4)
	m.QueriesGlobal.Add(5)
	m.QueriesRetried.Add(1)
	m.QueriesCached.Add(3)
	m.CacheMiss.Add(7)

	if m.QueriesTotal.Load() != 10 {
		t.Errorf("QueriesTotal = %d, want 10", m.QueriesTotal.Load())
	}
	if m.QueriesIran.Load() != 4 {
		t.Errorf("QueriesIran = %d, want 4", m.QueriesIran.Load())
	}
	if m.QueriesGlobal.Load() != 5 {
		t.Errorf("QueriesGlobal = %d, want 5", m.QueriesGlobal.Load())
	}
	if m.QueriesRetried.Load() != 1 {
		t.Errorf("QueriesRetried = %d, want 1", m.QueriesRetried.Load())
	}
	if m.QueriesCached.Load() != 3 {
		t.Errorf("QueriesCached = %d, want 3", m.QueriesCached.Load())
	}
	if m.CacheMiss.Load() != 7 {
		t.Errorf("CacheMiss = %d, want 7", m.CacheMiss.Load())
	}
}

func TestLearnedCounters(t *testing.T) {
	m := New()
	m.LearnedTotal.Add(100)
	m.LearnedToday.Add(5)

	if m.LearnedTotal.Load() != 100 {
		t.Errorf("LearnedTotal = %d, want 100", m.LearnedTotal.Load())
	}
	if m.LearnedToday.Load() != 5 {
		t.Errorf("LearnedToday = %d, want 5", m.LearnedToday.Load())
	}
}

func TestPathCounters(t *testing.T) {
	m := New()
	m.PathTLD.Add(10)
	m.PathPreferIran.Add(5)
	m.PathStore.Add(20)
	m.PathLearn.Add(3)

	if m.PathTLD.Load() != 10 {
		t.Errorf("PathTLD = %d, want 10", m.PathTLD.Load())
	}
	if m.PathPreferIran.Load() != 5 {
		t.Errorf("PathPreferIran = %d, want 5", m.PathPreferIran.Load())
	}
	if m.PathStore.Load() != 20 {
		t.Errorf("PathStore = %d, want 20", m.PathStore.Load())
	}
	if m.PathLearn.Load() != 3 {
		t.Errorf("PathLearn = %d, want 3", m.PathLearn.Load())
	}
}

func TestStoreOpCounters(t *testing.T) {
	m := New()
	m.StoreRemoved.Add(8)
	m.StoreCleaned.Add(50)

	if m.StoreRemoved.Load() != 8 {
		t.Errorf("StoreRemoved = %d, want 8", m.StoreRemoved.Load())
	}
	if m.StoreCleaned.Load() != 50 {
		t.Errorf("StoreCleaned = %d, want 50", m.StoreCleaned.Load())
	}
}

func TestUpstreamCounters(t *testing.T) {
	m := New()
	m.IranLatencyTotal.Add(500_000_000) // 500ms total
	m.IranQueryCount.Add(10)
	m.IranTimeouts.Add(2)
	m.GlobalLatencyTotal.Add(200_000_000) // 200ms total
	m.GlobalQueryCount.Add(20)
	m.GlobalTimeouts.Add(1)
	m.TcpFallbackCount.Add(3)
	m.GlobalFallbackCount.Add(4)
	m.GlobalFallbackLatencyTotal.Add(100_000_000) // 100ms total
	m.QueriesServfail.Add(5)

	if m.IranQueryCount.Load() != 10 {
		t.Errorf("IranQueryCount = %d, want 10", m.IranQueryCount.Load())
	}
	if m.IranTimeouts.Load() != 2 {
		t.Errorf("IranTimeouts = %d, want 2", m.IranTimeouts.Load())
	}
	if m.GlobalQueryCount.Load() != 20 {
		t.Errorf("GlobalQueryCount = %d, want 20", m.GlobalQueryCount.Load())
	}
	if m.GlobalTimeouts.Load() != 1 {
		t.Errorf("GlobalTimeouts = %d, want 1", m.GlobalTimeouts.Load())
	}
	if m.TcpFallbackCount.Load() != 3 {
		t.Errorf("TcpFallbackCount = %d, want 3", m.TcpFallbackCount.Load())
	}
	if m.GlobalFallbackCount.Load() != 4 {
		t.Errorf("GlobalFallbackCount = %d, want 4", m.GlobalFallbackCount.Load())
	}
	if m.GlobalFallbackLatencyTotal.Load() != 100_000_000 {
		t.Errorf("GlobalFallbackLatencyTotal = %d, want 100000000", m.GlobalFallbackLatencyTotal.Load())
	}
	if m.QueriesServfail.Load() != 5 {
		t.Errorf("QueriesServfail = %d, want 5", m.QueriesServfail.Load())
	}
}

func TestInFlightGauge(t *testing.T) {
	m := New()
	m.InFlightQueries.Add(1)
	m.InFlightQueries.Add(1)
	m.InFlightQueries.Add(-1)

	if m.InFlightQueries.Load() != 1 {
		t.Errorf("InFlightQueries = %d, want 1", m.InFlightQueries.Load())
	}
	m.InFlightQueries.Add(-1)
	if m.InFlightQueries.Load() != 0 {
		t.Errorf("InFlightQueries = %d, want 0", m.InFlightQueries.Load())
	}
}

func TestUpdateMetrics(t *testing.T) {
	m := New()
	m.LastUpdateTime.Store(time.Now())
	m.LastUpdateSuccess.Store(true)

	if !m.LastUpdateSuccess.Load() {
		t.Error("LastUpdateSuccess should be true")
	}

	m.LastUpdateSuccess.Store(false)
	if m.LastUpdateSuccess.Load() {
		t.Error("LastUpdateSuccess should be false after failure")
	}

	if tval, ok := m.LastUpdateTime.Load().(time.Time); !ok || tval.IsZero() {
		t.Error("LastUpdateTime should be a non-zero time.Time")
	}
}

func TestAtomicConcurrency(t *testing.T) {
	m := New()
	done := make(chan struct{})
	var want int64 = 1000

	go func() {
		for i := int64(0); i < want/2; i++ {
			m.QueriesTotal.Add(1)
			m.QueriesIran.Add(1)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := int64(0); i < want/2; i++ {
			m.QueriesTotal.Add(1)
			m.QueriesIran.Add(1)
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if m.QueriesTotal.Load() != want {
		t.Errorf("QueriesTotal = %d, want %d", m.QueriesTotal.Load(), want)
	}
	if m.QueriesIran.Load() != want {
		t.Errorf("QueriesIran = %d, want %d", m.QueriesIran.Load(), want)
	}
}

func TestMetricsHandler(t *testing.T) {
	m := New()
	m.QueriesTotal.Store(42)
	m.QueriesIran.Store(20)
	m.QueriesGlobal.Store(20)
	m.QueriesRetried.Store(2)
	m.QueriesCached.Store(10)
	m.CacheMiss.Store(32)
	m.LearnedTotal.Store(500)
	m.LearnedToday.Store(15)
	m.LastUpdateTime.Store(time.Unix(1000000, 0))
	m.LastUpdateSuccess.Store(true)
	m.PathTLD.Store(8)
	m.PathPreferIran.Store(3)
	m.PathStore.Store(15)
	m.PathLearn.Store(16)
	m.IranLatencyTotal.Store(500_000_000)
	m.IranQueryCount.Store(10)
	m.IranTimeouts.Store(2)
	m.GlobalLatencyTotal.Store(200_000_000)
	m.GlobalQueryCount.Store(20)
	m.GlobalTimeouts.Store(1)
	m.TcpFallbackCount.Store(3)
	m.GlobalFallbackCount.Store(4)
	m.GlobalFallbackLatencyTotal.Store(100_000_000)
	m.QueriesServfail.Store(5)
	m.StoreRemoved.Store(5)
	m.StoreCleaned.Store(40)
	m.InFlightQueries.Store(4)

	var storeSize atomic.Int64
	storeSize.Store(300)

	handler := m.MetricsHandler(func() int64 { return storeSize.Load() })

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	tests := []struct {
		key  string
		want float64
	}{
		{"queries_total", 42},
		{"queries_iran", 20},
		{"queries_global", 20},
		{"queries_retried", 2},
		{"queries_cached", 10},
		{"cache_miss", 32},
		{"learned_total", 500},
		{"learned_today", 15},
		{"store_size", 300},
		{"store_removed", 5},
		{"store_cleaned", 40},
		{"path_tld", 8},
		{"path_prefer_iran", 3},
		{"path_store", 15},
		{"path_learn", 16},
		{"iran_avg_latency_ms", 50},
		{"iran_timeouts", 2},
		{"global_avg_latency_ms", 10},
		{"global_timeouts", 1},
		{"tcp_fallback_count", 3},
		{"global_fallback_count", 4},
		{"global_fallback_avg_latency_ms", 25},
		{"queries_servfail", 5},
		{"in_flight_queries", 4},
	}

	for _, tt := range tests {
		v, ok := data[tt.key]
		if !ok {
			t.Errorf("missing key %q", tt.key)
			continue
		}
		got, ok := v.(float64)
		if !ok {
			t.Errorf("key %q = %T, want float64", tt.key, v)
			continue
		}
		if got != tt.want {
			t.Errorf("key %q = %v, want %v", tt.key, got, tt.want)
		}
	}

	if v, ok := data["last_update_success"]; !ok {
		t.Error("missing last_update_success")
	} else if v != true {
		t.Errorf("last_update_success = %v, want true", v)
	}

	if v, ok := data["last_update_time"]; !ok {
		t.Error("missing last_update_time")
	} else if v == "" {
		t.Errorf("last_update_time should not be empty")
	}

	if data["uptime"] == "" {
		t.Error("uptime should not be empty")
	}
}

func TestMetricsHandlerZeroHitRatio(t *testing.T) {
	m := New()
	handler := m.MetricsHandler(func() int64 { return 0 })

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	var data map[string]interface{}
	json.Unmarshal(body, &data)

	if data["cache_hit_ratio"] != "0%" {
		t.Errorf("cache_hit_ratio = %v, want 0%%", data["cache_hit_ratio"])
	}
}

func TestMetricsHandlerZeroLatency(t *testing.T) {
	m := New()
	handler := m.MetricsHandler(func() int64 { return 0 })

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	var data map[string]interface{}
	json.Unmarshal(body, &data)

	// should not crash with zero query counts
	if data["iran_avg_latency_ms"] != float64(0) {
		t.Errorf("iran_avg_latency_ms = %v, want 0", data["iran_avg_latency_ms"])
	}
	if data["global_avg_latency_ms"] != float64(0) {
		t.Errorf("global_avg_latency_ms = %v, want 0", data["global_avg_latency_ms"])
	}
}

func TestHealthHandler(t *testing.T) {
	m := New()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	m.HealthHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	if data["status"] != "ok" {
		t.Errorf("health status = %v, want ok", data["status"])
	}
	if data["uptime"] == "" {
		t.Error("uptime should not be empty")
	}
}

func TestStartServer(t *testing.T) {
	m := New()
	var storeSize atomic.Int64
	storeSize.Store(100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			m.MetricsHandler(func() int64 { return storeSize.Load() })(w, r)
		case "/health":
			m.HealthHandler(w, r)
		}
	}))
	defer server.Close()

	m.QueriesTotal.Store(7)
	m.QueriesIran.Store(3)

	resp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	json.Unmarshal(body, &data)

	if data["queries_total"] != float64(7) {
		t.Errorf("queries_total = %v, want 7", data["queries_total"])
	}
	if data["queries_iran"] != float64(3) {
		t.Errorf("queries_iran = %v, want 3", data["queries_iran"])
	}
	if data["store_size"] != float64(100) {
		t.Errorf("store_size = %v, want 100", data["store_size"])
	}
}

func TestUptime(t *testing.T) {
	m := New()
	u := m.Uptime()
	if u == "" {
		t.Error("Uptime should not be empty")
	}
}

func TestUptimeFormat(t *testing.T) {
	m := &Metrics{startTime: time.Now().Add(-2 * time.Hour)}
	m.LastUpdateTime.Store(time.Time{})
	m.LastUpdateSuccess.Store(false)
	m.LearnedToday.Store(0)

	u := m.Uptime()
	if u == "" {
		t.Error("Uptime should not be empty")
	}

	t.Logf("uptime = %s", u)
}

func TestLastUpdateNever(t *testing.T) {
	m := New()
	handler := m.MetricsHandler(func() int64 { return 0 })

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	var data map[string]interface{}
	json.Unmarshal(body, &data)

	if data["last_update_time"] != "never" {
		t.Errorf("last_update_time = %v, want never", data["last_update_time"])
	}
	if data["last_update_ago"] != "never" {
		t.Errorf("last_update_ago = %v, want never", data["last_update_ago"])
	}
}

func TestRestoreFromFile(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	// write a state file with today's date
	today := time.Now().Format("2006-01-02")
	state.Save(path, &state.State{
		LastUpdateUnix:    1000,
		LastUpdateSuccess: true,
		LastCleanupUnix:   2000,
		LearnedTodayDate:  today,
		LearnedTodayCount: 42,
	})

	m := New()
	m.RestoreFromFile(path)

	if m.LearnedToday.Load() != 42 {
		t.Errorf("LearnedToday = %d, want 42", m.LearnedToday.Load())
	}
	if !m.LastUpdateSuccess.Load() {
		t.Error("LastUpdateSuccess should be true")
	}
	if v, ok := m.LastUpdateTime.Load().(time.Time); !ok || v.Unix() != 1000 {
		t.Errorf("LastUpdateTime = %v, want unix 1000", v)
	}
}

func TestRestoreFromFileWrongDate(t *testing.T) {
	path := tempFile(t)
	defer os.Remove(path)

	// write a state file with YESTERDAY's date — should NOT restore
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	state.Save(path, &state.State{
		LearnedTodayDate:  yesterday,
		LearnedTodayCount: 99,
	})

	m := New()
	m.RestoreFromFile(path)

	if m.LearnedToday.Load() != 0 {
		t.Errorf("LearnedToday = %d, want 0 (wrong date)", m.LearnedToday.Load())
	}
}

func TestRestoreFromFileMissing(t *testing.T) {
	m := New()
	m.RestoreFromFile("/nonexistent/state.json")
	// should not crash, defaults should be zero
	if m.LearnedToday.Load() != 0 {
		t.Errorf("LearnedToday = %d, want 0", m.LearnedToday.Load())
	}
	if m.LastUpdateSuccess.Load() {
		t.Error("LastUpdateSuccess should be false")
	}
}

func tempFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "metrics-state-*.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

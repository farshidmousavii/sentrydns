#!/bin/bash
# sentrydps — sentrydns diagnostic probe & threshold watcher
#
# Polls :9153/metrics, tracks a rolling window of counters,
# logs to sentrydps.json, and raises alerts on stderr when
# thresholds are breached.
#
# No external dependencies beyond curl and a POSIX shell (bash).
#
# Usage:
#   ./sentrydps.sh                    # foreground (default)
#   ./sentrydps.sh --daemon           # daemonize
#   ./sentrydps.sh --check            # one-shot check, exit
#
# Environment variables (all optional):
#   SENTRY_METRICS_URL     http://localhost:9153/metrics
#   SENTRY_DPS_LOG         /var/log/sentrydns/sentrydps.json
#   SENTRY_PIDFILE         /var/run/sentrydps.pid
#   SENTRY_INTERVAL        30  (seconds between polls)
#   SENTRY_WINDOW          300 (seconds for rolling window)
#   SENTRY_IRAN_TIMEOUT_PCT   60
#   SENTRY_GLOBAL_TIMEOUT_PCT 60
#   SENTRY_INFLIGHT_MAX       100
#   SENTRY_SERVFAIL_RATE      10  (percent)
#   SENTRY_IRAN_LATENCY_MS    2000
#   SENTRY_GLOBAL_LATENCY_MS  2000
#   SENTRY_UPSTREAM_DEAD_SEC  120
set -euo pipefail

# ---- defaults ----
METRICS_URL="${SENTRY_METRICS_URL:-http://localhost:9153/metrics}"
LOG="${SENTRY_DPS_LOG:-/var/log/sentrydns/sentrydps.json}"
PIDFILE="${SENTRY_PIDFILE:-/var/run/sentrydps.pid}"
INTERVAL="${SENTRY_INTERVAL:-30}"
WINDOW="${SENTRY_WINDOW:-300}"
IRAN_TIMEOUT_PCT="${SENTRY_IRAN_TIMEOUT_PCT:-60}"
GLOBAL_TIMEOUT_PCT="${SENTRY_GLOBAL_TIMEOUT_PCT:-60}"
INFLIGHT_MAX="${SENTRY_INFLIGHT_MAX:-100}"
SERVFAIL_RATE="${SENTRY_SERVFAIL_RATE:-10}"
IRAN_LATENCY_MS="${SENTRY_IRAN_LATENCY_MS:-2000}"
GLOBAL_LATENCY_MS="${SENTRY_GLOBAL_LATENCY_MS:-2000}"
UPSTREAM_DEAD_SEC="${SENTRY_UPSTREAM_DEAD_SEC:-120}"
VERSION="1.0.0"

# ---- helpers ----
log_entry() {
    local level="$1" msg="$2"
    echo "{\"time\":\"$(date -Iseconds)\",\"level\":\"$level\",\"message\":\"$msg\"}" >> "$LOG"
}

alert() {
    local level="$1" msg="$2"
    entry="{\"time\":\"$(date -Iseconds)\",\"level\":\"$level\",\"alert\":\"$msg\"}"
    echo "$entry" >> "$LOG"
    echo "ALERT [$level] $msg" >&2
}

cleanup() {
    rm -f "$PIDFILE"
    log_entry "info" "stopped"
    exit 0
}

die() {
    echo "FATAL: $*" >&2
    exit 1
}

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

# Extract a field from flat JSON (single line, no nested objects).
# Returns the raw value — strings have quotes stripped, numbers are bare.
get_field() {
    echo "$1" | grep -oP "\"$2\":\K[^,}]+" | head -1 | tr -d '"'
}

# ---- dependencies ----
require_cmd curl

# ---- mode dispatcher ----
MODE="${1:-}"

case "$MODE" in
    --daemon)
        (umask 0; exec >/dev/null 2>&1 </dev/null)
        setsid "$0" --daemon-child &
        exit 0
        ;;
    --daemon-child)
        ;;
    --check)
        ;;
    --help|-h)
        cat <<EOF
sentrydps v$VERSION — sentrydns diagnostic probe

Usage: $0 [--daemon|--check|--help]

  (no args)     Run in foreground, polling forever
  --daemon      Fork into background
  --check       One-shot: poll once, print metrics, exit

Thresholds (override via env):
  SENTRY_IRAN_TIMEOUT_PCT    (default 60)
  SENTRY_GLOBAL_TIMEOUT_PCT  (default 60)
  SENTRY_INFLIGHT_MAX        (default 100)
  SENTRY_SERVFAIL_RATE       (default 10)
  SENTRY_IRAN_LATENCY_MS     (default 2000)
  SENTRY_GLOBAL_LATENCY_MS   (default 2000)
  SENTRY_UPSTREAM_DEAD_SEC   (default 120)
EOF
        exit 0
        ;;
esac

mkdir -p "$(dirname "$LOG")" "$(dirname "$PIDFILE")" 2>/dev/null || true

# ---- one-shot mode ----
if [ "$MODE" = "--check" ]; then
    data=$(curl -sf --max-time 5 "$METRICS_URL" 2>/dev/null) || die "cannot reach $METRICS_URL"

    uptime=$(get_field "$data" uptime)
    iran_q=$(get_field "$data" queries_iran)
    iran_t=$(get_field "$data" iran_timeouts)
    global_q=$(get_field "$data" queries_global)
    global_t=$(get_field "$data" global_timeouts)
    iran_avg=$(get_field "$data" iran_avg_latency_ms)
    global_avg=$(get_field "$data" global_avg_latency_ms)
    inflight=$(get_field "$data" in_flight_queries)
    gfb=$(get_field "$data" global_fallback_count)

    iran_tp=$(awk -v t="${iran_t:-0}" -v q="${iran_q:-0}" 'BEGIN{if(q+0>0) printf "%.1f", t/q*100; else print "0"}')
    global_tp=$(awk -v t="${global_t:-0}" -v q="${global_q:-0}" 'BEGIN{if(q+0>0) printf "%.1f", t/q*100; else print "0"}')

    printf '{"uptime":"%s","iran_timeout_pct":%s,"global_timeout_pct":%s,"in_flight_queries":%s,"iran_avg_latency_ms":%s,"global_avg_latency_ms":%s,"global_fallback_count":%s}\n' \
        "$uptime" "$iran_tp" "$global_tp" "${inflight:-0}" "${iran_avg:-0}" "${global_avg:-0}" "${gfb:-0}"
    exit 0
fi

# ---- daemon mode: write PID file ----
echo $$ > "$PIDFILE"
trap cleanup SIGINT SIGTERM SIGHUP

log_entry "info" "started (interval=${INTERVAL}s, window=${WINDOW}s, pid=$$)"

# rolling window buffer: pipe-separated cumulative counters
# format: timestamp|queries_iran|iran_timeouts|queries_global|global_timeouts|queries_servfail|queries_total|queries_cached|global_fallback_count|store_cleaned|learned_total|in_flight|iran_avg_lat_ms|global_avg_lat_ms
declare -a SAMPLES=()
LAST_UPSTREAM_OK=$(date +%s)

while true; do
    data=$(curl -sf --max-time 5 "$METRICS_URL" 2>/dev/null) || {
        sleep "$INTERVAL"
        continue
    }

    iran_q=$(get_field "$data" queries_iran)
    iran_t=$(get_field "$data" iran_timeouts)
    global_q=$(get_field "$data" queries_global)
    global_t=$(get_field "$data" global_timeouts)
    servfail=$(get_field "$data" queries_servfail)
    total=$(get_field "$data" queries_total)
    cached=$(get_field "$data" queries_cached)
    global_fb=$(get_field "$data" global_fallback_count)
    cleaned=$(get_field "$data" store_cleaned)
    learned=$(get_field "$data" learned_total)
    inflight=$(get_field "$data" in_flight_queries)
    iran_avg_lat=$(get_field "$data" iran_avg_latency_ms)
    global_avg_lat=$(get_field "$data" global_avg_latency_ms)
    uptime=$(get_field "$data" uptime)

    now=$(date +%s)
    SAMPLES+=("$now|${iran_q:-0}|${iran_t:-0}|${global_q:-0}|${global_t:-0}|${servfail:-0}|${total:-0}|${cached:-0}|${global_fb:-0}|${cleaned:-0}|${learned:-0}|${inflight:-0}|${iran_avg_lat:-0}|${global_avg_lat:-0}")

    # prune samples outside window
    cutoff=$((now - WINDOW))
    NEW=()
    for s in "${SAMPLES[@]}"; do
        t="${s%%|*}"
        [ "$t" -ge "$cutoff" ] && NEW+=("$s")
    done
    SAMPLES=("${NEW[@]}")

    # need at least 2 samples to compute a delta
    if [ "${#SAMPLES[@]}" -lt 2 ]; then
        sleep "$INTERVAL"
        continue
    fi

    first="${SAMPLES[0]}"
    last="${SAMPLES[-1]}"

    IFS='|' read -r _ iran_q0 iran_t0 gq0 gt0 sf0 tot0 cached0 gfb0 cl0 lrn0 _ il0 gl0 <<< "$first"
    IFS='|' read -r _ iran_q1 iran_t1 gq1 gt1 sf1 tot1 cached1 gfb1 cl1 lrn1 if1 il1 gl1 <<< "$last"

    d_iran_q=$((iran_q1 - iran_q0))
    d_iran_t=$((iran_t1 - iran_t0))
    d_global_q=$((gq1 - gq0))
    d_global_t=$((gt1 - gt0))
    d_servfail=$((sf1 - sf0))
    d_total=$((tot1 - tot0))
    d_cached=$((cached1 - cached0))
    d_global_fb=$((gfb1 - gfb0))
    d_cleaned=$((cl1 - cl0))
    d_learned=$((lrn1 - lrn0))

    # snapshot values from latest sample
    d_inflight=$if1
    d_iran_avg_lat=$il1
    d_global_avg_lat=$gl1

    iran_timeout_pct=$(awk -v t="$d_iran_t" -v q="$d_iran_q" 'BEGIN{if(q>0) printf "%.1f", t/q*100; else print "0.0"}')
    global_timeout_pct=$(awk -v t="$d_global_t" -v q="$d_global_q" 'BEGIN{if(q>0) printf "%.1f", t/q*100; else print "0.0"}')
    servfail_pct=$(awk -v s="$d_servfail" -v t="$d_total" 'BEGIN{if(t>0) printf "%.1f", s/t*100; else print "0.0"}')
    cache_hit_pct=$(awk -v c="$d_cached" -v t="$d_total" 'BEGIN{if(t>0) printf "%.1f", c/t*100; else print "0.0"}')

    # track upstream health
    if [ "$d_iran_q" -gt 0 ] || [ "$d_global_q" -gt 0 ]; then
        LAST_UPSTREAM_OK=$now
    fi

    # ---- threshold checks ----
    ALERTS=()
    if awk "BEGIN{exit($iran_timeout_pct > $IRAN_TIMEOUT_PCT) ? 0 : 1}"; then
        ALERTS+=("IRAN_TIMEOUT_HIGH: ${iran_timeout_pct}% > ${IRAN_TIMEOUT_PCT}%")
    fi
    if awk "BEGIN{exit($global_timeout_pct > $GLOBAL_TIMEOUT_PCT) ? 0 : 1}"; then
        ALERTS+=("GLOBAL_TIMEOUT_HIGH: ${global_timeout_pct}% > ${GLOBAL_TIMEOUT_PCT}%")
    fi
    if [ "$d_inflight" -gt "$INFLIGHT_MAX" ]; then
        ALERTS+=("INFLIGHT_HIGH: $d_inflight > $INFLIGHT_MAX")
    fi
    if awk "BEGIN{exit($servfail_pct > $SERVFAIL_RATE) ? 0 : 1}"; then
        ALERTS+=("SERVFAIL_HIGH: ${servfail_pct}% > ${SERVFAIL_RATE}%")
    fi
    if awk -v v="$d_iran_avg_lat" -v t="$IRAN_LATENCY_MS" 'BEGIN{if(v+0 > t+0) exit 0; exit 1}'; then
        ALERTS+=("IRAN_LATENCY_HIGH: ${d_iran_avg_lat}ms > ${IRAN_LATENCY_MS}ms")
    fi
    if awk -v v="$d_global_avg_lat" -v t="$GLOBAL_LATENCY_MS" 'BEGIN{if(v+0 > t+0) exit 0; exit 1}'; then
        ALERTS+=("GLOBAL_LATENCY_HIGH: ${d_global_avg_lat}ms > ${GLOBAL_LATENCY_MS}ms")
    fi
    upstream_dead=$((now - LAST_UPSTREAM_OK))
    if [ "$upstream_dead" -gt "$UPSTREAM_DEAD_SEC" ]; then
        ALERTS+=("UPSTREAM_DEAD: ${upstream_dead}s > ${UPSTREAM_DEAD_SEC}s since last successful query")
    fi

    # ---- build JSON output ----
    alerts_json='[]'
    if [ "${#ALERTS[@]}" -gt 0 ]; then
        alerts_json='['
        sep=''
        for a in "${ALERTS[@]}"; do
            alerts_json+="${sep}\"${a}\""
            sep=','
        done
        alerts_json+=']'
    fi

    entry=$(printf '{"time":"%s","uptime_sec":"%s","iran_timeout_pct":%s,"global_timeout_pct":%s,"in_flight":%s,"iran_avg_latency_ms":%s,"global_avg_latency_ms":%s,"global_fallback_count":%s,"alerts":%s}' \
        "$(date -Iseconds)" \
        "$uptime" \
        "$iran_timeout_pct" \
        "$global_timeout_pct" \
        "$d_inflight" \
        "$d_iran_avg_lat" \
        "$d_global_avg_lat" \
        "$d_global_fb" \
        "$alerts_json")
    echo "$entry" >> "$LOG"
    echo "$entry" >&2

    # print alerts to stderr for journald/capture
    if [ "${#ALERTS[@]}" -gt 0 ]; then
        for a in "${ALERTS[@]}"; do
            alert "WARN" "$a"
        done
    fi

    sleep "$INTERVAL"
done

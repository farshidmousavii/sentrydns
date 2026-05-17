#!/bin/bash
# sentrydps — sentrydns diagnostic probe & threshold watcher
#
# Polls :9153/metrics, tracks a rolling window of counters,
# logs to sentrydps.json, and raises alerts on stderr when
# thresholds are breached.
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

# ---- dependencies ----
require_cmd curl
require_cmd jq

# ---- mode dispatcher ----
MODE="${1:-}"

case "$MODE" in
    --daemon)
        # double-fork daemonize
        (umask 0; exec >/dev/null 2>&1 </dev/null)
        setsid "$0" --daemon-child &
        exit 0
        ;;
    --daemon-child)
        # child continues below
        ;;
    --check)
        # one-shot below
        ;;
    --help|-h)
        echo "sentrydps v$VERSION — sentrydns diagnostic probe"
        echo ""
        echo "Usage: $0 [--daemon|--check|--help]"
        echo ""
        echo "  (no args)     Run in foreground, polling forever"
        echo "  --daemon      Fork into background"
        echo "  --check       One-shot: poll once, print metrics, exit"
        echo ""
        echo "Thresholds (override via env):"
        echo "  SENTRY_IRAN_TIMEOUT_PCT    (default 60)"
        echo "  SENTRY_GLOBAL_TIMEOUT_PCT  (default 60)"
        echo "  SENTRY_INFLIGHT_MAX        (default 100)"
        echo "  SENTRY_SERVFAIL_RATE       (default 10)"
        echo "  SENTRY_IRAN_LATENCY_MS     (default 2000)"
        echo "  SENTRY_GLOBAL_LATENCY_MS   (default 2000)"
        echo "  SENTRY_UPSTREAM_DEAD_SEC   (default 120)"
        exit 0
        ;;
esac

mkdir -p "$(dirname "$LOG")" "$(dirname "$PIDFILE")" 2>/dev/null || true

# ---- one-shot mode ----
if [ "$MODE" = "--check" ]; then
    data=$(curl -sf --max-time 5 "$METRICS_URL" 2>/dev/null) || die "cannot reach $METRICS_URL"
    echo "$data" | jq '{
        uptime,
        iran_timeout_pct: (if .iran_query_count > 0 then (.iran_timeouts / .iran_query_count * 100 | floor / 10) else 0 end),
        global_timeout_pct: (if .global_query_count > 0 then (.global_timeouts / .global_query_count * 100 | floor / 10) else 0 end),
        servfail_rate: (if .queries_total > 0 then (.queries_servfail / .queries_total * 100 | floor / 10) else 0 end),
        cache_hit_ratio,
        in_flight_queries,
        iran_avg_latency_ms,
        global_avg_latency_ms,
        global_fallback_count,
        store_cleaned
    }'
    exit 0
fi

# ---- daemon mode: write PID file ----
echo $$ > "$PIDFILE"
trap cleanup SIGINT SIGTERM SIGHUP

log_entry "info" "started (interval=${INTERVAL}s, window=${WINDOW}s, pid=$$)"

# rolling window buffer
declare -a SAMPLES=()
LAST_UPSTREAM_OK=$(date +%s)

while true; do
    data=$(curl -sf --max-time 5 "$METRICS_URL" 2>/dev/null) || {
        sleep "$INTERVAL"
        continue
    }

    iran_q=$(jq '.iran_query_count // 0' <<< "$data")
    iran_t=$(jq '.iran_timeouts // 0' <<< "$data")
    global_q=$(jq '.global_query_count // 0' <<< "$data")
    global_t=$(jq '.global_timeouts // 0' <<< "$data")
    iran_lat=$(jq '.iran_latency_total // 0' <<< "$data")
    global_lat=$(jq '.global_latency_total // 0' <<< "$data")
    servfail=$(jq '.queries_servfail // 0' <<< "$data")
    total=$(jq '.queries_total // 0' <<< "$data")
    inflight=$(jq '.in_flight_queries // 0' <<< "$data")
    uptime=$(jq '.uptime // 0' <<< "$data")

    now=$(date +%s)
    SAMPLES+=("$now|$iran_q|$iran_t|$global_q|$global_t|$iran_lat|$global_lat|$servfail|$total")

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

    IFS='|' read -r _ iran_q0 iran_t0 gq0 gt0 il0 gl0 sf0 tot0 <<< "$first"
    IFS='|' read -r _ iran_q1 iran_t1 gq1 gt1 il1 gl1 sf1 tot1 <<< "$last"

    d_iran_q=$((iran_q1 - iran_q0))
    d_iran_t=$((iran_t1 - iran_t0))
    d_global_q=$((gq1 - gq0))
    d_global_t=$((gt1 - gt0))
    d_iran_lat=$((il1 - il0))
    d_global_lat=$((gl1 - gl0))
    d_servfail=$((sf1 - sf0))
    d_total=$((tot1 - tot0))

    iran_timeout_pct=$(awk -v t="$d_iran_t" -v q="$d_iran_q" 'BEGIN{if(q>0) printf "%.1f", t/q*100; else print "0.0"}')
    global_timeout_pct=$(awk -v t="$d_global_t" -v q="$d_global_q" 'BEGIN{if(q>0) printf "%.1f", t/q*100; else print "0.0"}')
    iran_avg_lat_ms=$(awk -v l="$d_iran_lat" -v q="$d_iran_q" 'BEGIN{if(q>0) printf "%.0f", l/q/1e6; else print "0"}')
    global_avg_lat_ms=$(awk -v l="$d_global_lat" -v q="$d_global_q" 'BEGIN{if(q>0) printf "%.0f", l/q/1e6; else print "0"}')
    servfail_pct=$(awk -v s="$d_servfail" -v t="$d_total" 'BEGIN{if(t>0) printf "%.1f", s/t*100; else print "0.0"}')

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
    if [ "$inflight" -gt "$INFLIGHT_MAX" ]; then
        ALERTS+=("INFLIGHT_HIGH: $inflight > $INFLIGHT_MAX")
    fi
    if awk "BEGIN{exit($servfail_pct > $SERVFAIL_RATE) ? 0 : 1}"; then
        ALERTS+=("SERVFAIL_HIGH: ${servfail_pct}% > ${SERVFAIL_RATE}%")
    fi
    if awk -v v="$iran_avg_lat_ms" -v t="$IRAN_LATENCY_MS" 'BEGIN{if(v+0 > t+0) exit 0; exit 1}'; then
        ALERTS+=("IRAN_LATENCY_HIGH: ${iran_avg_lat_ms}ms > ${IRAN_LATENCY_MS}ms")
    fi
    if awk -v v="$global_avg_lat_ms" -v t="$GLOBAL_LATENCY_MS" 'BEGIN{if(v+0 > t+0) exit 0; exit 1}'; then
        ALERTS+=("GLOBAL_LATENCY_HIGH: ${global_avg_lat_ms}ms > ${GLOBAL_LATENCY_MS}ms")
    fi
    upstream_dead=$((now - LAST_UPSTREAM_OK))
    if [ "$upstream_dead" -gt "$UPSTREAM_DEAD_SEC" ]; then
        ALERTS+=("UPSTREAM_DEAD: ${upstream_dead}s > ${UPSTREAM_DEAD_SEC}s since last successful query")
    fi

    # ---- build JSON output ----
    alert_json=$(printf '%s\n' "${ALERTS[@]}" | jq -R -s 'split("\n") | map(select(length > 0))' 2>/dev/null || echo '[]')

    entry=$(jq -n \
        --arg t "$(date -Iseconds)" \
        --argjson uptime "$uptime" \
        --argjson iran_tp "$iran_timeout_pct" \
        --argjson global_tp "$global_timeout_pct" \
        --argjson inflight "$inflight" \
        --argjson sf "$servfail_pct" \
        --argjson iran_lat "$iran_avg_lat_ms" \
        --argjson global_lat "$global_avg_lat_ms" \
        --argjson uptime_sec "$uptime" \
        --argjson alerts "$alert_json" \
        '{time: $t, uptime_sec: $uptime_sec, iran_timeout_pct: ($iran_tp | tonumber), global_timeout_pct: ($global_tp | tonumber), in_flight: $inflight, servfail_rate: ($sf | tonumber), iran_avg_latency_ms: ($iran_lat | tonumber), global_avg_latency_ms: ($global_lat | tonumber), alerts: $alerts}')

    echo "$entry" >> "$LOG"

    # print alerts to stderr for journald/capture
    if [ "${#ALERTS[@]}" -gt 0 ]; then
        for a in "${ALERTS[@]}"; do
            alert "WARN" "$a"
        done
    fi

    sleep "$INTERVAL"
done

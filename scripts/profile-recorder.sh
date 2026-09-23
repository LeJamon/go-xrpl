#!/usr/bin/env bash
# Record an already-running node; never restarts it or changes profiling rates.
set -Eeuo pipefail
umask 077

output=''
pprof_url='http://127.0.0.1:6060'
rpc_url='http://127.0.0.1:5005'
container='goxrpl-testnet-validator'
trace_seconds=30
trace_interval=300
stack_interval=30
contention_interval=60
cpu_seconds=30
profile_interval=300
max_gib=200
reserve_gib=25
check_only=false

usage() {
    printf '%s\n' \
      'Usage: profile-recorder.sh --output DIR [options]' \
      '  --pprof-url URL          Loopback pprof endpoint (default: http://127.0.0.1:6060)' \
      '  --rpc-url URL            Loopback JSON-RPC endpoint (default: http://127.0.0.1:5005)' \
      '  --container NAME         Docker container for read-only resource samples' \
      '  --trace-seconds N        Execution trace length (default: 30 seconds)' \
      '  --trace-interval N       Execution trace interval (default: 300 seconds)' \
      '  --stack-interval N       Grouped goroutine snapshot interval (default: 30 seconds)' \
      '  --contention-interval N  Block/mutex snapshot interval (default: 60 seconds)' \
      '  --cpu-seconds N          CPU sample length (default: 30)' \
      '  --profile-interval N     CPU/heap/allocs interval (default: 300 seconds)' \
      '  --max-gib N              Pause at approximate archive budget (default: 200 GiB)' \
      '  --reserve-gib N          Pause below filesystem reserve (default: 25 GiB)' \
      '  --check                 Validate configuration/storage only, without capturing' \
      '  -h, --help              Show help' \
      '' \
      'All timestamps are UTC. Completed files are published by atomic rename.' \
      'Goroutine collection uses debug=1, never runtime.Stack(all=true)/debug=2.' \
      'Existing captures are NEVER deleted.' \
      'Storage limits are soft: in-flight captures finish; archive size is checked every 5 minutes.'
}

while (($#)); do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --check) check_only=true; shift ;;
        --output|--pprof-url|--rpc-url|--container|--trace-seconds|--trace-interval|--stack-interval|--contention-interval|--cpu-seconds|--profile-interval|--max-gib|--reserve-gib)
            (($# >= 2)) || { printf 'Missing value for %s\n' "$1" >&2; exit 2; }
            case "$1" in
                --output) output=$2 ;;
                --pprof-url) pprof_url=${2%/} ;;
                --rpc-url) rpc_url=${2%/} ;;
                --container) container=$2 ;;
                --trace-seconds) trace_seconds=$2 ;;
                --trace-interval) trace_interval=$2 ;;
                --stack-interval) stack_interval=$2 ;;
                --contention-interval) contention_interval=$2 ;;
                --cpu-seconds) cpu_seconds=$2 ;;
                --profile-interval) profile_interval=$2 ;;
                --max-gib) max_gib=$2 ;;
                --reserve-gib) reserve_gib=$2 ;;
            esac
            shift 2 ;;
        *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
    esac
done
[[ "$output" = /* && "$output" != / ]] || { echo '--output must be a dedicated absolute directory' >&2; exit 2; }
for value in "$trace_seconds" "$trace_interval" "$stack_interval" "$contention_interval" "$cpu_seconds" "$profile_interval" "$max_gib" "$reserve_gib"; do
    [[ "$value" =~ ^[1-9][0-9]{0,5}$ ]] || { echo 'Intervals/budgets must be positive decimal integers' >&2; exit 2; }
done
((trace_seconds <= 60 && trace_seconds < trace_interval && cpu_seconds <= 60 && cpu_seconds < profile_interval)) || {
    echo 'Trace/CPU samples must be <=60 seconds; intervals must exceed sample lengths' >&2; exit 2;
}
for url in "$pprof_url" "$rpc_url"; do
    [[ "$url" =~ ^http://(127\.0\.0\.1|localhost|\[::1\]):[0-9]+$ ]] || {
        echo 'Diagnostic endpoints must be explicit loopback HTTP URLs without credentials or paths' >&2; exit 2;
    }
done
[[ "$container" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo 'Invalid container name' >&2; exit 2; }
for executable in curl gzip flock date stat df du timeout docker; do
    command -v "$executable" >/dev/null || { printf 'Missing command: %s\n' "$executable" >&2; exit 2; }
done

mkdir -p "$output"
output=$(cd "$output" && pwd -P)
exec 9>"$output/.recorder.lock"
flock -n 9 || { echo 'A recorder already owns this output directory' >&2; exit 3; }
max_bytes=$((max_gib * 1024 * 1024 * 1024))
reserve_kib=$((reserve_gib * 1024 * 1024))
archive_bytes=0
last_archive_check=-300
pause_reason=''

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" "$*" >>"$output/recorder.log"; }

check_storage() {
    local free_kib measured_bytes reason=''
    free_kib=$(df -Pk "$output" | awk 'END {print $4}')
    if ((SECONDS - last_archive_check >= 300)); then
        # Captures are atomically renamed while this scan runs. Retain the
        # last valid measurement on a transient scan error instead of killing
        # an active trace; the free-space reserve is checked independently.
        if measured_bytes=$(du -s --block-size=1 "$output" 2>>"$output/recorder.log" | awk '{print $1}') &&
            [[ "$measured_bytes" =~ ^[0-9]+$ ]]; then
            archive_bytes=$measured_bytes
        else
            log 'archive size scan incomplete; keeping previous measurement'
        fi
        last_archive_check=$SECONDS
    fi
    if ((free_kib < reserve_kib)); then
        reason="filesystem free space below ${reserve_gib} GiB reserve"
    elif ((archive_bytes >= max_bytes)); then
        reason="archive budget of ${max_gib} GiB reached"
    fi
    if [[ -n "$reason" ]]; then
        # Control marker only; captures are never removed.
        printf '%s\n' "$reason" >"$output/.paused"
        if [[ "$reason" != "$pause_reason" ]]; then log "PAUSED: $reason"; fi
    else
        if [[ -f "$output/.paused" ]]; then
            rm -f -- "$output/.paused"
            log 'RESUMED: storage budgets permit capture'
        fi
    fi
    pause_reason=$reason
}

check_storage
if "$check_only"; then
    printf 'output=%s archive_bytes=%s max_gib=%s reserve_gib=%s paused=%s\n' \
        "$output" "$archive_bytes" "$max_gib" "$reserve_gib" "${pause_reason:-no}"
    [[ -z "$pause_reason" ]]
    exit
fi

ready() { while [[ -f "$output/.paused" ]]; do sleep 5; done; }
pace() {
    local began=$1 interval=$2 remaining
    remaining=$((interval - (SECONDS - began)))
    if ((remaining > 0)); then sleep "$remaining"; fi
}

# Each artifact has request start/end metadata. Failed/incomplete captures keep
# their .partial suffix and are never mistaken for a completed trace/profile.
capture() {
    local kind=$1 endpoint=$2 limit=$3 compress=$4
    shift 4
    local start end stamp folder base part final code bytes
    start=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
    stamp=$(date -u +%Y%m%dT%H%M%S.%NZ)
    folder="$output/$(date -u +%Y-%m-%d)/$(date -u +%H)/$kind"
    mkdir -p "$folder" || return 1
    base="$folder/$stamp"
    part="$base.partial"
    if curl --silent --show-error --fail --connect-timeout 3 --max-time "$limit" \
        "$@" "$endpoint" -o "$part" 2>>"$output/recorder.log"; then
        end=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
        if [[ "$compress" = gzip ]]; then
            gzip -1 <"$part" >"$base.gz.partial" || return 1
            mv -- "$base.gz.partial" "$base.gz" || return 1
            # Only remove this capture's redundant raw temporary file after
            # successful compression; the completed compressed data is retained.
            rm -f -- "$part"
            final="$base.gz"
        else
            mv -- "$part" "$base.pb.gz" || return 1
            final="$base.pb.gz"
        fi
        bytes=$(stat -c %s "$final")
        printf 'kind=%s\nstarted=%s\nfinished=%s\nbytes=%s\nstatus=complete\n' \
            "$kind" "$start" "$end" "$bytes" >"$base.meta"
        return 0
    else
        code=$?
        end=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
        printf 'kind=%s\nstarted=%s\nfinished=%s\nstatus=failed\ncurl_exit=%s\n' \
            "$kind" "$start" "$end" "$code" >"$base.meta"
        log "capture failed: kind=$kind curl_exit=$code artifact=$base"
        return 1
    fi
}

trace_worker() {
    local began
    while :; do
        ready; began=$SECONDS
        capture trace "$pprof_url/debug/pprof/trace?seconds=$trace_seconds" "$((trace_seconds + 15))" gzip || sleep 5
        pace "$began" "$trace_interval"
    done
}
stack_worker() {
    local began
    while :; do
        ready; began=$SECONDS
        capture goroutines "$pprof_url/debug/pprof/goroutine?debug=1" 10 gzip || true
        pace "$began" "$stack_interval"
    done
}
contention_worker() {
    local began
    while :; do
        ready; began=$SECONDS
        capture block "$pprof_url/debug/pprof/block" 10 native || true
        capture mutex "$pprof_url/debug/pprof/mutex" 10 native || true
        pace "$began" "$contention_interval"
    done
}
profile_worker() {
    local began
    while :; do
        ready; began=$SECONDS
        capture cpu "$pprof_url/debug/pprof/profile?seconds=$cpu_seconds" "$((cpu_seconds + 15))" native || true
        # No gc=1: do not force a garbage collection just for diagnostics.
        capture heap "$pprof_url/debug/pprof/heap" 15 native || true
        capture allocs "$pprof_url/debug/pprof/allocs" 15 native || true
        pace "$began" "$profile_interval"
    done
}
status_worker() {
    local began stamp folder
    while :; do
        ready; began=$SECONDS
        capture rpc "$rpc_url/" 10 gzip -H 'Content-Type: application/json' \
            -d '{"method":"server_info","params":[{"counters":true}]}' || true
        stamp=$(date -u +%Y%m%dT%H%M%S.%NZ)
        folder="$output/$(date -u +%Y-%m-%d)/$(date -u +%H)/resources"
        mkdir -p "$folder"
        {
            date -u +%Y-%m-%dT%H:%M:%S.%NZ
            timeout 10 docker inspect --format 'id={{.Id}} image={{.Image}} status={{.State.Status}} started={{.State.StartedAt}} pid={{.State.Pid}} restarts={{.RestartCount}} oom={{.State.OOMKilled}}' "$container" || true
            timeout 10 docker stats --no-stream --format '{{json .}}' "$container" || true
            for pressure in cpu io memory; do
                if [[ -r "/proc/pressure/$pressure" ]]; then
                    printf '\n%s pressure\n' "$pressure"
                    sed -n '1,3p' "/proc/pressure/$pressure"
                fi
            done
            df -Pk "$output"
        } >"$folder/$stamp.txt.partial" 2>>"$output/recorder.log"
        mv -- "$folder/$stamp.txt.partial" "$folder/$stamp.txt"
        pace "$began" 60
    done
}
storage_worker() { while :; do check_storage; sleep 10; done; }

log "START pprof=$pprof_url rpc=$rpc_url container=$container trace_seconds=$trace_seconds trace_interval=$trace_interval stack_interval=$stack_interval goroutine_debug=1 contention_interval=$contention_interval cpu_seconds=$cpu_seconds profile_interval=$profile_interval max_gib=$max_gib reserve_gib=$reserve_gib"
workers=()
stop() {
    trap - TERM INT
    log 'STOP requested; stopping recorder workers, leaving node and completed captures untouched'
    kill "${workers[@]}" 2>/dev/null || true
    wait "${workers[@]}" 2>/dev/null || true
    exit 0
}
trap stop TERM INT
for worker in trace_worker stack_worker contention_worker profile_worker status_worker storage_worker; do
    "$worker" &
    workers+=("$!")
done
if wait -n "${workers[@]}"; then status=0; else status=$?; fi
log "worker exited unexpectedly (status=$status); supervisor should restart recorder"
kill "${workers[@]}" 2>/dev/null || true
wait "${workers[@]}" 2>/dev/null || true
exit 1

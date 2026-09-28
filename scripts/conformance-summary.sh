#!/usr/bin/env bash
# Run the required final-rippled conformance target and print a compact summary.
#
# Usage:
#   ./scripts/conformance-summary.sh --corpus PATH
#   GOXRPL_FIXTURES_DIR=PATH ./scripts/conformance-summary.sh
#   ./scripts/conformance-summary.sh [SUITE_FILTER] [--failing] [--list-fail]

set -euo pipefail
cd "$(dirname "$0")/.."

FILTER=""
CORPUS="${GOXRPL_FIXTURES_DIR:-}"
LIST_FAIL=false
ONLY_FAILING=false
TIMEOUT="${CONFORMANCE_TIMEOUT:-300s}"
SCOPE_FILE="scripts/conformance-out-of-scope.txt"

usage() {
    echo "Usage: $0 --corpus PATH [SUITE_FILTER] [--failing] [--list-fail]"
    echo "       GOXRPL_FIXTURES_DIR=PATH $0 [SUITE_FILTER] [--failing] [--list-fail]"
    echo ""
    echo "  --corpus PATH  Required final-rippled corpus directory"
    echo "  SUITE_FILTER   Restrict TestConformance subtests (e.g. TxQ, AMM)"
    echo "  --failing      Only show suites that have failures"
    echo "  --list-fail    List every failing test name"
    echo ""
    echo "  CONFORMANCE_TIMEOUT  Test timeout (default: 300s)"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --corpus)
            if [[ $# -lt 2 || -z "$2" ]]; then
                echo "error: --corpus requires a path" >&2
                exit 2
            fi
            CORPUS="$2"
            shift 2
            ;;
        --corpus=*)
            CORPUS="${1#*=}"
            if [[ -z "$CORPUS" ]]; then
                echo "error: --corpus requires a path" >&2
                exit 2
            fi
            shift
            ;;
        --list-fail)
            LIST_FAIL=true
            shift
            ;;
        --failing)
            ONLY_FAILING=true
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        --*)
            echo "error: unknown option $1" >&2
            usage >&2
            exit 2
            ;;
        *)
            if [[ -n "$FILTER" ]]; then
                echo "error: only one suite filter is supported" >&2
                exit 2
            fi
            FILTER="$1"
            shift
            ;;
    esac
done

if [[ -z "$CORPUS" ]]; then
    echo "error: a final-rippled corpus is required; use --corpus PATH or GOXRPL_FIXTURES_DIR" >&2
    exit 2
fi

if [[ -t 1 ]]; then
    C_GREEN=$'\033[0;32m'
    C_RED=$'\033[0;31m'
    C_YELLOW=$'\033[0;33m'
    C_DIM=$'\033[2m'
    C_BOLD=$'\033[1m'
    C_RESET=$'\033[0m'
else
    C_GREEN='' C_RED='' C_YELLOW='' C_DIM='' C_BOLD='' C_RESET=''
fi

OOS_FILE=$(mktemp)
TMPFILE=$(mktemp)
RESULTS=$(mktemp)
SUITE_DATA=$(mktemp)
trap 'rm -f "$TMPFILE" "$RESULTS" "$SUITE_DATA" "$OOS_FILE"' EXIT

if [[ -f "$SCOPE_FILE" ]]; then
    awk 'NF && $1 !~ /^#/' "$SCOPE_FILE" | tr -d ' ' > "$OOS_FILE"
else
    : > "$OOS_FILE"
fi

is_out_of_scope() {
    grep -qxF "$1" "$OOS_FILE"
}

if [[ -n "$FILTER" ]]; then
    RUN_PATTERN="^TestConformance/app/${FILTER}"
else
    RUN_PATTERN='^TestConformance($|/)'
fi

echo "Running final-rippled conformance tests (timeout=${TIMEOUT})..."
GO_TEST_EXIT=0
GOXRPL_CONFORMANCE_REQUIRED=1 GOXRPL_FIXTURES_DIR="$CORPUS" \
    go test -count=1 ./internal/testing/conformance \
    -run "$RUN_PATTERN" -timeout "$TIMEOUT" -v > "$TMPFILE" 2>&1 || GO_TEST_EXIT=$?

fail_loud() {
    echo ""
    echo "${C_RED}${C_BOLD}=========================================${C_RESET}"
    echo "${C_RED}${C_BOLD} CONFORMANCE RUN FAILED: $1${C_RESET}"
    echo "${C_RED}${C_BOLD}=========================================${C_RESET}"
    echo "${C_DIM}--- last 30 lines of test output ---${C_RESET}"
    tail -n 30 "$TMPFILE"
    exit 1
}

if grep -qE 'build failed|cannot find package|\[setup failed\]' "$TMPFILE"; then
    fail_loud "package failed to build"
fi
if grep -qE 'test timed out|test (binary|process|executable) killed|^panic: ' "$TMPFILE"; then
    fail_loud "test run panicked or timed out (results truncated)"
fi

# Only subtests represent fixture executions. The top-level TestConformance
# line is deliberately excluded from these counts.
grep 'TestConformance/' "$TMPFILE" | grep -E -- '--- (PASS|FAIL):' > "$RESULTS" || true

TOTAL_PASS=$(grep -c -- '--- PASS:' "$RESULTS" || true)
TOTAL_FAIL=$(grep -c -- '--- FAIL:' "$RESULTS" || true)
TOTAL=$((TOTAL_PASS + TOTAL_FAIL))
TOTAL_SKIP=$(grep 'TestConformance/' "$TMPFILE" | grep -c -- '--- SKIP:' || true)

if [[ "$TOTAL" -eq 0 ]]; then
    fail_loud "zero executed fixtures (${TOTAL_SKIP} skipped, go test exit ${GO_TEST_EXIT})"
fi

awk '{
    tag = ($0 ~ /--- PASS:/) ? "P" : "F"
    sub(/.*TestConformance\//, "")
    sub(/ \(.*/, "")
    n = split($0, parts, "/")
    suite = (n >= 2) ? parts[1] "/" parts[2] : parts[1]
    if (tag == "P") p[suite]++; else f[suite]++
    t[suite]++
}
END {
    for (s in t) {
        pass = (s in p) ? p[s] : 0
        fail = (s in f) ? f[s] : 0
        total = t[s]
        rate = (total > 0) ? int(pass * 100 / total) : 0
        print s, pass, fail, total, rate
    }
}' "$RESULTS" | sort > "$SUITE_DATA"

IN_SCOPE_PASS=0
IN_SCOPE_FAIL=0
OUT_SCOPE_PASS=0
OUT_SCOPE_FAIL=0
while read -r suite pass fail total rate; do
    [[ -z "$suite" ]] && continue
    if is_out_of_scope "$suite"; then
        OUT_SCOPE_PASS=$((OUT_SCOPE_PASS + pass))
        OUT_SCOPE_FAIL=$((OUT_SCOPE_FAIL + fail))
    else
        IN_SCOPE_PASS=$((IN_SCOPE_PASS + pass))
        IN_SCOPE_FAIL=$((IN_SCOPE_FAIL + fail))
    fi
done < "$SUITE_DATA"
IN_SCOPE_TOTAL=$((IN_SCOPE_PASS + IN_SCOPE_FAIL))
OUT_SCOPE_TOTAL=$((OUT_SCOPE_PASS + OUT_SCOPE_FAIL))

echo ""
echo "${C_BOLD}=========================================${C_RESET}"
echo "${C_BOLD} CONFORMANCE SUMMARY${C_RESET}"
echo "${C_BOLD}=========================================${C_RESET}"
TOTAL_PCT=$((TOTAL_PASS * 100 / TOTAL))
printf " Total:    %4d pass / %4d fail / %4d  (%d%%)\n" \
    "$TOTAL_PASS" "$TOTAL_FAIL" "$TOTAL" "$TOTAL_PCT"
if [[ "$TOTAL_SKIP" -gt 0 ]]; then
    printf " ${C_YELLOW}Skipped:  %4d${C_RESET}\n" "$TOTAL_SKIP"
fi
if [[ "$IN_SCOPE_TOTAL" -gt 0 ]]; then
    IN_PCT=$((IN_SCOPE_PASS * 100 / IN_SCOPE_TOTAL))
    printf " ${C_GREEN}In scope: %4d pass / %4d fail / %4d  (%d%%)${C_RESET}\n" \
        "$IN_SCOPE_PASS" "$IN_SCOPE_FAIL" "$IN_SCOPE_TOTAL" "$IN_PCT"
fi
if [[ "$OUT_SCOPE_TOTAL" -gt 0 ]]; then
    printf " ${C_DIM}Out:      %4d pass / %4d fail / %4d${C_RESET}\n" \
        "$OUT_SCOPE_PASS" "$OUT_SCOPE_FAIL" "$OUT_SCOPE_TOTAL"
fi
echo "${C_BOLD}=========================================${C_RESET}"
echo ""

echo "Per-suite breakdown:"
echo ""
printf "%-45s %5s %5s %5s %6s\n" "Suite" "Pass" "Fail" "Total" "Rate"
printf "%-45s %5s %5s %5s %6s\n" "-----" "----" "----" "-----" "----"
while read -r suite pass fail total rate; do
    [[ -z "$suite" ]] && continue
    if $ONLY_FAILING && [[ "$fail" -eq 0 ]]; then
        continue
    fi
    line=$(printf "%-45s %5d %5d %5d %5d%%" "$suite" "$pass" "$fail" "$total" "$rate")
    if is_out_of_scope "$suite"; then
        echo "${C_DIM}${line}${C_RESET}"
    elif [[ "$fail" -eq 0 ]]; then
        echo "${C_GREEN}${line}${C_RESET}"
    elif [[ "$pass" -eq 0 ]]; then
        echo "${C_RED}${line}${C_RESET}"
    else
        echo "${C_YELLOW}${line}${C_RESET}"
    fi
done < "$SUITE_DATA"

if $LIST_FAIL; then
    echo ""
    echo "Failing tests ($TOTAL_FAIL):"
    grep 'FAIL:' "$RESULTS" | sed -E 's/.*FAIL: ([^ ]+).*/  \1/' || true
fi

if [[ "$GO_TEST_EXIT" -ne 0 || "$IN_SCOPE_FAIL" -ne 0 ]]; then
    exit 1
fi

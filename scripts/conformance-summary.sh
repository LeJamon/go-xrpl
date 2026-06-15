#!/usr/bin/env bash
# conformance-summary.sh — Run conformance tests and print a compact summary.
#
# Usage:
#   ./scripts/conformance-summary.sh              # full suite
#   ./scripts/conformance-summary.sh TxQ           # filter by suite name
#   ./scripts/conformance-summary.sh --failing     # show only suites with failures
#   ./scripts/conformance-summary.sh --list-fail   # list every failing test
#   ./scripts/conformance-summary.sh TxQ --list-fail

set -euo pipefail
cd "$(dirname "$0")/.."

FILTER=""
LIST_FAIL=false
ONLY_FAILING=false
RUN_PATTERN=""
TIMEOUT="${CONFORMANCE_TIMEOUT:-300s}"
SCOPE_FILE="scripts/conformance-out-of-scope.txt"

for arg in "$@"; do
    case "$arg" in
        --list-fail)  LIST_FAIL=true ;;
        --failing)    ONLY_FAILING=true ;;
        --help|-h)
            echo "Usage: $0 [SUITE_FILTER] [--failing] [--list-fail]"
            echo ""
            echo "  SUITE_FILTER   Only run tests matching this pattern (e.g. TxQ, AMM, Vault)"
            echo "  --failing      Only show suites that have failures"
            echo "  --list-fail    List every failing test name"
            echo ""
            echo "  Out-of-scope suites are defined in scripts/conformance-out-of-scope.txt"
            echo ""
            echo "Environment:"
            echo "  CONFORMANCE_TIMEOUT  Test timeout (default: 300s)"
            exit 0
            ;;
        *)            FILTER="$arg" ;;
    esac
done

if [[ -n "$FILTER" ]]; then
    RUN_PATTERN="-run TestConformance/app/${FILTER}"
fi

# Colors (only when stdout is a terminal)
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

# Load out-of-scope suites into a file for grep matching
OOS_FILE=$(mktemp)
if [[ -f "$SCOPE_FILE" ]]; then
    grep -v '^#' "$SCOPE_FILE" | grep -v '^$' | sed 's/ //g' > "$OOS_FILE"
else
    : > "$OOS_FILE"
fi

is_out_of_scope() {
    grep -qxF "$1" "$OOS_FILE"
}

# Run tests, capture output
TMPFILE=$(mktemp)
RESULTS=$(mktemp)
SUITE_DATA=$(mktemp)
trap 'rm -f "$TMPFILE" "$RESULTS" "$SUITE_DATA" "$OOS_FILE"' EXIT

echo "Running conformance tests (timeout=${TIMEOUT})..."
# Capture the exit status and BOTH streams (stderr carries build errors and
# panics). The previous `2>&1 > file` order sent stderr to the terminal, not
# the file, so compile failures escaped the result accounting below.
GO_TEST_EXIT=0
go test -count=1 ./internal/testing/conformance/... \
    $RUN_PATTERN -timeout "$TIMEOUT" -v > "$TMPFILE" 2>&1 || GO_TEST_EXIT=$?

# fail_loud prints a red banner plus the tail of the captured output and aborts
# non-zero. Used whenever the run ended abnormally (build failure, panic,
# timeout, wholesale skip): in those cases the PASS/FAIL tallies below are taken
# over a silently truncated denominator and must not be presented as the suite.
fail_loud() {
    echo ""
    echo "${C_RED}${C_BOLD}=========================================${C_RESET}"
    echo "${C_RED}${C_BOLD} CONFORMANCE RUN FAILED: $1${C_RESET}"
    echo "${C_RED}${C_BOLD}=========================================${C_RESET}"
    echo "${C_DIM}--- last 30 lines of test output ---${C_RESET}"
    tail -n 30 "$TMPFILE"
    exit 1
}

# A build failure, panic, or timeout truncates results without any PASS/FAIL
# bookkeeping — detect the markers go test emits in those cases.
if grep -qE 'build failed|cannot find package|\[setup failed\]' "$TMPFILE"; then
    fail_loud "package failed to build"
fi
if grep -qE 'test timed out|test (binary|process|executable) killed|^panic: ' "$TMPFILE"; then
    fail_loud "test run panicked or timed out (results truncated)"
fi

# Extract only subtest PASS/FAIL lines
grep 'TestConformance/' "$TMPFILE" | grep -E 'PASS:|FAIL:' > "$RESULTS" || true

TOTAL_PASS=$(grep -c 'PASS:' "$RESULTS" || true)
TOTAL_FAIL=$(grep -c 'FAIL:' "$RESULTS" || true)
TOTAL=$((TOTAL_PASS + TOTAL_FAIL))
TOTAL_SKIP=$(grep -c -- '--- SKIP:' "$TMPFILE" || true)

# Zero PASS/FAIL results means the run produced no data at all (missing/empty
# fixtures, a wholesale t.Skip, or an early abort). Don't print an empty,
# misleadingly-green summary. A run that is legitimately all-skips is reported,
# not failed; anything else aborts loudly.
if [[ "$TOTAL" -eq 0 ]]; then
    if [[ "$TOTAL_SKIP" -gt 0 ]]; then
        echo "${C_YELLOW}All conformance tests were skipped (${TOTAL_SKIP} skipped, 0 run).${C_RESET}"
        exit 0
    fi
    fail_loud "produced zero PASS/FAIL results (go test exit ${GO_TEST_EXIT})"
fi

# A non-zero exit with no failing subtests recorded means the process died for a
# reason the PASS/FAIL counters can't see (e.g. a late panic between subtests).
if [[ "$GO_TEST_EXIT" -ne 0 && "$TOTAL_FAIL" -eq 0 ]]; then
    fail_loud "go test exited ${GO_TEST_EXIT} with no failing subtests recorded"
fi

# Build per-suite data: "suite pass fail total rate"
awk '{
    if ($0 ~ /PASS:/) tag="P"; else tag="F"
    sub(/.*TestConformance\//, "")
    sub(/ \(.*/, "")
    n = split($0, parts, "/")
    suite = parts[1] "/" parts[2]
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

# Compute in-scope / out-of-scope totals
IN_SCOPE_PASS=0; IN_SCOPE_FAIL=0
OUT_SCOPE_PASS=0; OUT_SCOPE_FAIL=0
while read -r suite pass fail total rate; do
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

# Print summary
echo ""
echo "${C_BOLD}=========================================${C_RESET}"
echo "${C_BOLD} CONFORMANCE SUMMARY${C_RESET}"
echo "${C_BOLD}=========================================${C_RESET}"
if [[ "$TOTAL" -gt 0 ]]; then
    PCT=$(echo "scale=1; $TOTAL_PASS * 100 / $TOTAL" | bc 2>/dev/null || echo "?")
    printf " Total:    %4d pass / %4d fail / %4d  (%s%%)\n" \
        "$TOTAL_PASS" "$TOTAL_FAIL" "$TOTAL" "$PCT"
fi
if [[ "$TOTAL_SKIP" -gt 0 ]]; then
    printf " ${C_YELLOW}Skipped:  %4d${C_RESET}\n" "$TOTAL_SKIP"
fi
if [[ "$IN_SCOPE_TOTAL" -gt 0 ]]; then
    IN_PCT=$(echo "scale=1; $IN_SCOPE_PASS * 100 / $IN_SCOPE_TOTAL" | bc 2>/dev/null || echo "?")
    printf " ${C_GREEN}In scope: %4d pass / %4d fail / %4d  (%s%%)${C_RESET}\n" \
        "$IN_SCOPE_PASS" "$IN_SCOPE_FAIL" "$IN_SCOPE_TOTAL" "$IN_PCT"
fi
if [[ "$OUT_SCOPE_TOTAL" -gt 0 ]]; then
    printf " ${C_DIM}Out:      %4d pass / %4d fail / %4d${C_RESET}\n" \
        "$OUT_SCOPE_PASS" "$OUT_SCOPE_FAIL" "$OUT_SCOPE_TOTAL"
fi
echo "${C_BOLD}=========================================${C_RESET}"
echo ""

# Per-suite breakdown
echo "Per-suite breakdown:"
echo ""
printf "%-45s %5s %5s %5s %6s\n" "Suite" "Pass" "Fail" "Total" "Rate"
printf "%-45s %5s %5s %5s %6s\n" "-----" "----" "----" "-----" "----"

while read -r suite pass fail total rate; do
    # Apply --failing filter
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

echo ""

# List failing tests
if $LIST_FAIL; then
    echo "Failing tests ($TOTAL_FAIL):"
    echo ""
    while IFS= read -r line; do
        test_path=$(echo "$line" | sed 's/.*FAIL: //' | sed 's/ (.*//')
        suite=$(echo "$test_path" | sed 's/TestConformance\///' | awk -F'/' '{print $1"/"$2}')
        if is_out_of_scope "$suite"; then
            echo "${C_DIM}  ${test_path}${C_RESET}"
        else
            echo "  ${test_path}"
        fi
    done < <(grep 'FAIL:' "$RESULTS")
    echo ""
fi

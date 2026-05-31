#!/usr/bin/env bash
#
# Drive goXRPL's native Go fuzz targets (func Fuzz* in *_test.go). Two modes:
#
#   scripts/fuzz.sh --corpus [pkg ...]   Deterministic: replay the committed seed
#                                        corpus (testdata/fuzz + f.Add seeds) with
#                                        no mutation. Fast, reproducible — this is
#                                        the PR gate, and it fails if a committed
#                                        crasher ever regresses.
#
#   scripts/fuzz.sh [pkg ...]            Discovery: fuzz each target for FUZZTIME
#   FUZZTIME=2m scripts/fuzz.sh          (default 30s) with mutation, failing on
#                                        any new crasher. Open-ended and therefore
#                                        non-deterministic, so it runs on a
#                                        schedule, not as a required PR check.
#
# A new crasher found by the discovery run is minimised by Go into
# <pkg>/testdata/fuzz/<Func>/. Triage it, fix the bug, then commit the reproducer
# so the corpus replay guards against regressions forever after.
#
# With no package arguments both modes operate on every package that declares a
# fuzz target. Each target is fuzzed independently because `go test -fuzz`
# refuses to run when its regexp matches more than one target.

set -uo pipefail

corpus=0
if [ "${1:-}" = "--corpus" ]; then
    corpus=1
    shift
fi

FUZZTIME="${FUZZTIME:-30s}"

# Packages to operate on: the arguments if given, else every package that
# declares at least one fuzz target. `grep -r` emits paths with or without a
# leading "./" depending on the platform, so normalise to exactly one.
if [ "$#" -gt 0 ]; then
    pkgs=$(printf '%s\n' "$@")
else
    pkgs=$(grep -rl --include='*_test.go' '^func Fuzz' . \
        | xargs -n1 dirname \
        | sed 's#^\./##; s#^#./#' \
        | sort -u)
fi

fail=0
total=0
crashed=""

while IFS= read -r pkg; do
    [ -z "$pkg" ] && continue

    # `go test -list` compiles the test binary and prints the matching target
    # names (plus an "ok ..." summary line we drop with the ^Fuzz filter).
    funcs=$(go test -list '^Fuzz' "$pkg" 2>/dev/null | grep '^Fuzz' || true)
    [ -z "$funcs" ] && continue

    if [ "$corpus" -eq 1 ]; then
        # -run='^Fuzz' replays each target's seed corpus once, no mutation.
        echo "==> replaying corpus: ${pkg}"
        if ! go test -run='^Fuzz' "$pkg"; then
            crashed="${crashed}\n  ${pkg}"
            fail=1
        fi
        continue
    fi

    while IFS= read -r fn; do
        [ -z "$fn" ] && continue
        total=$((total + 1))
        echo "==> fuzzing ${pkg} ${fn} for ${FUZZTIME}"
        # -run='^$' matches no unit test, so only the fuzz target executes.
        if ! go test -run='^$' -fuzz="^${fn}$" -fuzztime="$FUZZTIME" "$pkg"; then
            echo "::error::fuzz target ${pkg} ${fn} failed — see ${pkg#./}/testdata/fuzz/${fn}/"
            crashed="${crashed}\n  ${pkg} ${fn}"
            fail=1
        fi
    done <<EOF
$funcs
EOF
done <<EOF
$pkgs
EOF

if [ "$corpus" -eq 0 ]; then
    echo "==> ran ${total} fuzz target(s)"
fi
if [ "$fail" -ne 0 ]; then
    echo "==> FAILED:"
    printf '%b\n' "$crashed"
fi
exit "$fail"

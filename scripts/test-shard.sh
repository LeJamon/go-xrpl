#!/usr/bin/env bash
# Prints the packages belonging to one or more test shards.
#
# The shards partition `go list ./...`: every package lands in exactly one, and
# a package matching no explicit rule falls into core (internal) or libs
# (public), so a new package is tested without anyone editing a list.
#
#   scripts/test-shard.sh core
#   scripts/test-shard.sh integration-offer integration
#   scripts/test-shard.sh --list
set -euo pipefail

shards=(integration-offer integration tx core libs)

if [ "$#" -eq 0 ]; then
    echo "usage: $0 --list | <shard>..." >&2
    exit 2
fi
if [ "$1" = "--list" ]; then
    printf '%s\n' "${shards[@]}"
    exit 0
fi
for want in "$@"; do
    case " ${shards[*]} " in
        *" $want "*) ;;
        *) echo "unknown shard: $want (have: ${shards[*]})" >&2; exit 2 ;;
    esac
done

cd "$(dirname "$0")/.."
module=$(go list -m)

shard_of() {
    case "$1" in
        internal/testing/offer | internal/testing/offer/*) echo integration-offer ;;
        internal/testing | internal/testing/*) echo integration ;;
        internal/tx | internal/tx/*) echo tx ;;
        # Runs with libs because that shard checks out the rippled oracles.
        internal/testutil | internal/testutil/*) echo libs ;;
        internal/* | cmd/* | scripts/*) echo core ;;
        *) echo libs ;;
    esac
}

go list ./... | while read -r pkg; do
    rel=${pkg#"$module"}
    rel=${rel#/}
    shard=$(shard_of "$rel")
    for want in "$@"; do
        if [ "$shard" = "$want" ]; then
            echo "$pkg"
        fi
    done
done

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
if [ "$#" -eq 1 ] && [ "$1" = "--list" ]; then
    printf '%s\n' "${shards[@]}"
    exit 0
fi
for want in "$@"; do
    for shard in "${shards[@]}"; do
        [[ $shard == "$want" ]] && continue 2
    done
    echo "unknown shard: $want (have: ${shards[*]})" >&2
    exit 2
done

cd "$(dirname "$0")/.."
packages=$(go list -f '{{.ImportPath}} {{.Module.Path}}' ./...)
matched=false
while read -r pkg module; do
    [[ -n $pkg ]] || continue
    case "${pkg#"$module"/}" in
        internal/testing/offer | internal/testing/offer/*) shard=integration-offer ;;
        internal/testing | internal/testing/*) shard=integration ;;
        internal/tx | internal/tx/*) shard=tx ;;
        # Runs with libs because that shard checks out the rippled oracles.
        internal/testutil | internal/testutil/*) shard=libs ;;
        internal | internal/* | cmd | cmd/* | scripts | scripts/*) shard=core ;;
        *) shard=libs ;;
    esac
    for want in "$@"; do
        if [ "$shard" = "$want" ]; then
            echo "$pkg"
            matched=true
            break
        fi
    done
done <<< "$packages"

if [[ $matched == false ]]; then
    echo "no packages in requested shards: $*" >&2
    exit 1
fi

#!/usr/bin/env bash
# Reports how the exported API of the public packages changed since a base ref.
# Packages under internal/ are not part of the API and are skipped.
#
#   scripts/apidiff.sh origin/main
set -euo pipefail

base=${1:?usage: $0 <base-ref>}
apidiff="golang.org/x/exp/cmd/apidiff@v0.0.0-20261109185158-a52e3aef5920"

cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/base"
git archive --format=tar "$base" | tar -x -C "$tmp/base"
(cd "$tmp/base" && go run "$apidiff" -m -w "$tmp/base.export" . 2>/dev/null)

report=$(go run "$apidiff" -m "$tmp/base.export" . 2>/dev/null)
if [ -z "$report" ]; then
    echo "No changes to the public API since $base."
else
    echo "$report"
fi

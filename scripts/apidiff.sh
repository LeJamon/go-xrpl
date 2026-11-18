#!/usr/bin/env bash
# Reports how the exported API of the supported public packages changed since a
# base ref.
#
#   scripts/apidiff.sh origin/main
set -euo pipefail

base=${1:?usage: $0 <base-ref>}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/base"
git -C "$root" archive --format=tar "$base" | tar -x -C "$tmp/base"
cd "$root"
GOWORK=off go run ./scripts/apidiff --base "$tmp/base" --new "$root"

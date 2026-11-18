#!/usr/bin/env bash
# Reports how the exported API of the supported public packages changed since a
# base ref.
#
#   scripts/apidiff.sh origin/main
set -euo pipefail

base=${1:?usage: $0 <base-ref>}
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/base"
git archive --format=tar "$base" | tar -x -C "$tmp/base"
GOWORK=off go run ./scripts/apidiff "$tmp/base" "$PWD"

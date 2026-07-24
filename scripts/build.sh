#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

build_version="${VERSION:-$(git describe --tags --always --dirty)}"
if [[ "$build_version" =~ [[:space:]] ]]; then
    echo "VERSION must not contain whitespace" >&2
    exit 2
fi

mkdir -p ../tmp
go build -v \
    -ldflags="-X=github.com/LeJamon/go-xrpl/version.Version=${build_version}" \
    -o ../tmp/main ./cmd/xrpld

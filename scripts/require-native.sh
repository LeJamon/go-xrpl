#!/usr/bin/env bash
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
    printf '%s\n' 'native build requires the Go toolchain (go was not found)' >&2
    exit 2
fi

cgo_enabled="$(go env CGO_ENABLED)"
if [[ "$cgo_enabled" != "1" ]]; then
    printf '%s\n' \
        'native build requires CGO_ENABLED=1; install the native dependencies and rebuild with CGO enabled' >&2
    exit 2
fi

if ! command -v pkg-config >/dev/null 2>&1; then
    printf '%s\n' \
        'native build requires pkg-config to locate OpenSSL and libsecp256k1' >&2
    exit 2
fi

missing=()
for package in libssl libcrypto libsecp256k1; do
    if ! pkg-config --exists "$package"; then
        missing+=("$package")
    fi
done

if ((${#missing[@]} != 0)); then
    printf 'native build cannot find pkg-config package(s): %s\n' \
        "${missing[*]}" >&2
    printf '%s\n' \
        'Install OpenSSL 3, libsecp256k1, and pkg-config, or set PKG_CONFIG_PATH to their .pc files.' >&2
    exit 2
fi

printf 'native CGO dependencies: OpenSSL %s, libsecp256k1 %s\n' \
    "$(pkg-config --modversion openssl 2>/dev/null || pkg-config --modversion libssl)" \
    "$(pkg-config --modversion libsecp256k1)"

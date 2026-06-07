#!/usr/bin/env bash
#
# Fetches the wasmi C library the goXRPL WASM engine links against, using the
# same Conan package rippled's smart-escrow branch depends on (wasmi/1.0.9 from
# the XRPLF remote). Building the exact engine guarantees an identical
# per-instruction fuel model — consensus-critical, since a different version
# forks the network.
#
# Produces:
#   artifacts/lib/libwasmi.a
#   artifacts/include/{wasm.h,wasmi.h,wasmi/*}
#
# Requirements: conan 2.x (with the `xrplf` remote configured), plus a Rust
# toolchain + cmake when Conan has to build the package from source.
# Idempotent: skips when artifacts already exist unless WASMI_FORCE=1.

set -euo pipefail

WASMI_REF="${WASMI_REF:-wasmi/1.0.9}"
WASMI_REMOTE="${WASMI_REMOTE:-xrplf}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARTIFACTS="$HERE/artifacts"
DEPLOY="$HERE/.deploy"

if [[ "${WASMI_FORCE:-0}" != "1" && -f "$ARTIFACTS/lib/libwasmi.a" && -f "$ARTIFACTS/include/wasmi.h" ]]; then
  echo "wasmi: artifacts already present at $ARTIFACTS (set WASMI_FORCE=1 to rebuild)"
  exit 0
fi

command -v conan >/dev/null 2>&1 || {
  echo "wasmi: conan not found in PATH (install conan 2.x and configure the '$WASMI_REMOTE' remote)" >&2
  exit 1
}

echo "wasmi: conan install $WASMI_REF from remote '$WASMI_REMOTE' (Release)"
rm -rf "$DEPLOY"
conan install \
  --requires="$WASMI_REF" \
  -r "$WASMI_REMOTE" \
  -s build_type=Release \
  --deployer=direct_deploy \
  -of "$DEPLOY" \
  --build=missing

SRC="$DEPLOY/direct_deploy/wasmi"
test -f "$SRC/lib/libwasmi.a" || {
  echo "wasmi: conan deploy did not produce libwasmi.a under $SRC" >&2
  find "$DEPLOY" -maxdepth 4 -name '*.a' >&2 || true
  exit 1
}

rm -rf "$ARTIFACTS"
mkdir -p "$ARTIFACTS"
cp -R "$SRC/include" "$ARTIFACTS/include"
cp -R "$SRC/lib" "$ARTIFACTS/lib"
rm -rf "$DEPLOY"

for sym in wasm_store_set_fuel wasm_store_new_with_memory_max_pages; do
  grep -q "$sym" "$ARTIFACTS/include/wasm.h" || {
    echo "wasmi: expected fuel symbol '$sym' missing from installed wasm.h" >&2
    exit 1
  }
done

echo "wasmi: done ($(grep -E 'WASMI_VERSION ' "$ARTIFACTS/include/wasmi.h"))"
find "$ARTIFACTS" -maxdepth 2 -type f

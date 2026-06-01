#!/usr/bin/env bash
#
# Builds wasmi's C API as a static library for cgo linkage.
#
# Produces:
#   artifacts/lib/libwasmi.a
#   artifacts/include/{wasm.h,wasmi.h,wasmi/*}
#
# Consensus parity requires the EXACT engine rippled uses. Rippled depends on
# the Conan package wasmi/0.42.1, which is upstream wasmi v0.42.1 PLUS an XRPLF
# patch (patches/0001-xrplf-0.42.1.patch) that adds a fuel-metered C API
# (wasm_store_new_with_memory_max_pages / wasm_store_set_fuel /
# wasm_store_get_fuel) on top of the wasmi 0.42.1 core. The per-instruction
# fuel model lives in that core crate, so building this exact source guarantees
# identical gas costs — a different tag or an unpatched build would fork.
#
# Requirements: git, a Rust toolchain (cargo/rustc), cmake.
# Re-running is cheap: it skips the build when artifacts already exist unless
# WASMI_FORCE=1 is set.

set -euo pipefail

WASMI_TAG="${WASMI_TAG:-v0.42.1}"
WASMI_REPO="${WASMI_REPO:-https://github.com/wasmi-labs/wasmi.git}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$HERE/.build"
SRC_DIR="$BUILD_DIR/wasmi"
ARTIFACTS="$HERE/artifacts"
PATCH="$HERE/patches/0001-xrplf-0.42.1.patch"

if [[ "${WASMI_FORCE:-0}" != "1" && -f "$ARTIFACTS/lib/libwasmi.a" && -f "$ARTIFACTS/include/wasmi.h" ]]; then
  echo "wasmi: artifacts already present at $ARTIFACTS (set WASMI_FORCE=1 to rebuild)"
  exit 0
fi

for tool in git cargo cmake; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "wasmi: required tool '$tool' not found in PATH" >&2
    exit 1
  }
done

test -f "$PATCH" || {
  echo "wasmi: missing XRPLF patch at $PATCH" >&2
  exit 1
}

mkdir -p "$BUILD_DIR"

if [[ ! -d "$SRC_DIR/.git" ]]; then
  echo "wasmi: cloning $WASMI_REPO @ $WASMI_TAG"
  git clone --depth 1 --branch "$WASMI_TAG" "$WASMI_REPO" "$SRC_DIR"
else
  echo "wasmi: reusing checkout at $SRC_DIR"
  git -C "$SRC_DIR" fetch --depth 1 origin "$WASMI_TAG"
  git -C "$SRC_DIR" checkout -q FETCH_HEAD
fi

# Reset to a pristine tag checkout so the patch always applies idempotently.
echo "wasmi: applying XRPLF fuel-API patch"
git -C "$SRC_DIR" reset --hard -q HEAD
git -C "$SRC_DIR" clean -fdq
git -C "$SRC_DIR" apply "$PATCH"

echo "wasmi: configuring c_api (cmake)"
cmake -S "$SRC_DIR/crates/c_api" -B "$BUILD_DIR/c_api" \
  -DCMAKE_BUILD_TYPE=Release \
  --install-prefix "$ARTIFACTS"

echo "wasmi: building + installing"
cmake --build "$BUILD_DIR/c_api" --target install

for sym in wasm_store_set_fuel wasm_store_new_with_memory_max_pages; do
  grep -q "$sym" "$ARTIFACTS/include/wasm.h" || {
    echo "wasmi: patched symbol '$sym' missing from installed wasm.h — patch did not take" >&2
    exit 1
  }
done

test -f "$ARTIFACTS/lib/libwasmi.a" || {
  echo "wasmi: expected static lib not produced at $ARTIFACTS/lib/libwasmi.a" >&2
  find "$ARTIFACTS" -maxdepth 2 >&2 || true
  exit 1
}

echo "wasmi: done"
find "$ARTIFACTS" -maxdepth 2 -type f

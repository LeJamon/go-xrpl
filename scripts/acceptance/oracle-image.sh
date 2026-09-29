#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'oracle-image: %s\n' "$*" >&2
  exit 1
}

: "${ORACLE_IMAGE:?ORACLE_IMAGE is required}"
: "${EVIDENCE_DIR:?EVIDENCE_DIR is required}"
mkdir -p "$EVIDENCE_DIR"
rm -f "$EVIDENCE_DIR/oracle-artifact.txt" "$EVIDENCE_DIR/oracle-image-id.txt"

image_id="$(docker image inspect "$ORACLE_IMAGE" --format '{{.Id}}')"
[[ "$image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || die "not an immutable image ID: $image_id"
platform="$(docker image inspect "$image_id" --format '{{.Os}}/{{.Architecture}}')"
[[ "$platform" == linux/amd64 ]] || die "expected linux/amd64, got $platform"

docker run --rm --platform linux/amd64 --entrypoint /usr/bin/sha256sum \
  "$image_id" /usr/bin/xrpld > "$EVIDENCE_DIR/oracle-binary.sha256"
grep --fixed-strings --line-regexp \
  'fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa  /usr/bin/xrpld' \
  "$EVIDENCE_DIR/oracle-binary.sha256"

docker run --rm --platform linux/amd64 --entrypoint /usr/bin/xrpld \
  "$image_id" --version > "$EVIDENCE_DIR/oracle-version.txt"
grep --fixed-strings --line-regexp 'xrpld version 3.4.1' "$EVIDENCE_DIR/oracle-version.txt"
grep --fixed-strings --line-regexp \
  'Git commit hash: d147fccf54a500fce586522f28d6044c37fd8d29' "$EVIDENCE_DIR/oracle-version.txt"

{
  printf 'oracle_repository=XRPLF/xrpld-private\n'
  printf 'oracle_tag=3.4.1\n'
  printf 'oracle_commit=d147fccf54a500fce586522f28d6044c37fd8d29\n'
  printf 'package_url=https://packages.xrplf.org/repository/deb-stable/pool/x/xrpld/xrpld_3.4.1-1_amd64.deb\n'
  printf 'package_sha256=cae8ce3b9bc9451b19975c890714ba789d2004987cdfff9cbd55522c612c6f26\n'
  printf 'binary_sha256=fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa\n'
  printf 'image_id=%s\nplatform=%s\n' "$image_id" "$platform"
} > "$EVIDENCE_DIR/oracle-artifact.txt"
printf '%s\n' "$image_id" > "$EVIDENCE_DIR/oracle-image-id.txt"

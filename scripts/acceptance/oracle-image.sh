#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'oracle-image: %s\n' "$*" >&2
  exit 1
}

pin_file="$(dirname "${BASH_SOURCE[0]}")/../../internal/testutil/rippled/pin.env"
pin() {
  local value
  value="$(sed -n "s/^$1=//p" "$pin_file")"
  [[ -n "$value" ]] || die "$1 is missing from $pin_file"
  printf '%s' "$value"
}
oracle_repository="$(pin ORACLE_REPOSITORY)"
oracle_tag="$(pin ORACLE_TAG)"
oracle_commit="$(pin ORACLE_COMMIT)"
package_url="$(pin ORACLE_PACKAGE_URL)"
package_sha256="$(pin ORACLE_PACKAGE_SHA256)"
binary_sha256="$(pin ORACLE_BINARY_SHA256)"

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
  "$binary_sha256  /usr/bin/xrpld" \
  "$EVIDENCE_DIR/oracle-binary.sha256"

docker run --rm --platform linux/amd64 --entrypoint /usr/bin/xrpld \
  "$image_id" --version > "$EVIDENCE_DIR/oracle-version.txt"
grep --fixed-strings --line-regexp "xrpld version $oracle_tag" "$EVIDENCE_DIR/oracle-version.txt"
grep --fixed-strings --line-regexp \
  "Git commit hash: $oracle_commit" "$EVIDENCE_DIR/oracle-version.txt"

{
  printf 'oracle_repository=%s\n' "$oracle_repository"
  printf 'oracle_tag=%s\n' "$oracle_tag"
  printf 'oracle_commit=%s\n' "$oracle_commit"
  printf 'package_url=%s\n' "$package_url"
  printf 'package_sha256=%s\n' "$package_sha256"
  printf 'binary_sha256=%s\n' "$binary_sha256"
  printf 'image_id=%s\nplatform=%s\n' "$image_id" "$platform"
} > "$EVIDENCE_DIR/oracle-artifact.txt"
printf '%s\n' "$image_id" > "$EVIDENCE_DIR/oracle-image-id.txt"

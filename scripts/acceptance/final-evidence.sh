#!/usr/bin/env bash
# Record producer results and assemble the final release evidence manifest.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage:
  final-evidence.sh producer
  final-evidence.sh source
  final-evidence.sh conformance-source
  final-evidence.sh conformance
  final-evidence.sh aggregate
EOF
  exit 2
}

die() {
  printf 'final-evidence: %s\n' "$*" >&2
  exit 1
}

mode="${1:-}"
[[ -n "$mode" ]] || usage

evidence_dir="${EVIDENCE_DIR:-}"
[[ -n "$evidence_dir" ]] || die 'EVIDENCE_DIR is required'
mkdir -p "$evidence_dir"

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[[ -n "$repo_root" ]] || die 'must run inside the Go repository'
cd "$repo_root"

expected_sha="${EXPECTED_SHA:-${GITHUB_SHA:-}}"
go_sha="$(git rev-parse HEAD 2>/dev/null || true)"
[[ -n "$go_sha" ]] || die 'could not determine the tested Go SHA'

worktree_status() {
  # The oracle is a deliberately nested checkout. Exclude it from the Go
  # checkout status, then inspect it separately in the consensus producer.
  git status --porcelain=v1 --untracked-files=all -- . \
    ':(exclude)rippled-worktrees/v3.4.0-oracle' \
    ':(exclude)fixtures/rippled-3.4.0-v3' 2>/dev/null || true
}

write_common_metadata() {
  local output="$1"
  local dirty_status
  dirty_status="$(worktree_status)"
  {
    printf 'evidence_version=1\n'
    printf 'tested_sha=%s\n' "$go_sha"
    printf 'expected_sha=%s\n' "$expected_sha"
    printf 'go_dirty=%s\n' "$([[ -n "$dirty_status" ]] && printf true || printf false)"
    printf 'workflow=%s\n' "${GITHUB_WORKFLOW:-}"
    printf 'run_id=%s\n' "${GITHUB_RUN_ID:-}"
    printf 'job=%s\n' "${GITHUB_JOB:-}"
    printf 'repository=%s\n' "${GITHUB_REPOSITORY:-}"
    printf 'ref=%s\n' "${GITHUB_REF:-}"
  } > "$output"
  if [[ -n "$dirty_status" ]]; then
    printf '%s\n' "$dirty_status" > "${output%.txt}.git-status.txt"
  fi
}

case "$mode" in
  producer)
    label="${EVIDENCE_LABEL:-}"
    command="${EVIDENCE_COMMAND:-}"
    status="${EVIDENCE_STATUS:-}"
    [[ -n "$label" ]] || die 'EVIDENCE_LABEL is required'
    [[ -n "$command" ]] || die 'EVIDENCE_COMMAND is required'
    [[ -n "$status" ]] || die 'EVIDENCE_STATUS is required'

    output="$evidence_dir/producer-${label}.txt"
    write_common_metadata "$output"
    {
      printf 'producer=%s\n' "$label"
      printf 'status=%s\n' "$status"
      printf 'command=%s\n' "$command"
    } >> "$output"
    ;;

  source)
    output="$evidence_dir/source-provenance.txt"
    write_common_metadata "$output"
    if [[ -n "$expected_sha" && "$go_sha" != "$expected_sha" ]]; then
      printf 'sha_validation=failed\n' >> "$output"
      die "tested SHA $go_sha does not match expected SHA $expected_sha"
    fi
    printf 'sha_validation=passed\n' >> "$output"
    ;;

  conformance-source)
    source_repository="${FINAL_CONFORMANCE_REPOSITORY:-}"
    source_commit="${FINAL_CONFORMANCE_COMMIT:-}"
    source_manifest="${FINAL_CONFORMANCE_MANIFEST:-}"
    oracle_repository="${ORACLE_REPOSITORY:-XRPLF/rippled}"
    oracle_tag="${ORACLE_TAG:-}"
    oracle_commit="${ORACLE_COMMIT:-}"
    output="$evidence_dir/conformance-source.txt"
    write_common_metadata "$output"
    {
      printf 'source_repository=%s\n' "$source_repository"
      printf 'source_commit=%s\n' "$source_commit"
      printf 'manifest=%s\n' "$source_manifest"
      printf 'oracle_repository=%s\n' "$oracle_repository"
      printf 'oracle_tag=%s\n' "$oracle_tag"
      printf 'oracle_commit=%s\n' "$oracle_commit"
    } >> "$output"
    [[ "$source_repository" =~ ^[^/[:space:]]+/[^/[:space:]]+$ ]] ||
      die 'FINAL_CONFORMANCE_REPOSITORY must be owner/repository'
    [[ "$source_commit" =~ ^[0-9a-fA-F]{40}$ ]] ||
      die 'FINAL_CONFORMANCE_COMMIT must be a full 40-character commit SHA'
    [[ -n "${FINAL_CONFORMANCE_CORPUS:-}" ]] ||
      die 'FINAL_CONFORMANCE_CORPUS must name the checkout target'
    [[ -n "$source_manifest" ]] ||
      die 'FINAL_CONFORMANCE_MANIFEST must identify corpus provenance'
    [[ "$oracle_tag" == 3.4.0 ]] || die 'ORACLE_TAG must be 3.4.0'
    [[ "$oracle_commit" =~ ^[0-9a-fA-F]{40}$ ]] ||
      die 'ORACLE_COMMIT must be a full 40-character SHA'
    ;;

  conformance)
    corpus="${FINAL_CONFORMANCE_CORPUS:-}"
    source_repository="${FINAL_CONFORMANCE_REPOSITORY:-}"
    source_commit="${FINAL_CONFORMANCE_COMMIT:-}"
    source_manifest="${FINAL_CONFORMANCE_MANIFEST:-}"
    oracle_repository="${ORACLE_REPOSITORY:-XRPLF/rippled}"
    oracle_tag="${ORACLE_TAG:-}"
    oracle_commit="${ORACLE_COMMIT:-}"
    output="$evidence_dir/conformance-provenance.txt"
    write_common_metadata "$output"
    {
      printf 'oracle_tag=%s\n' "$oracle_tag"
      printf 'oracle_commit=%s\n' "$oracle_commit"
      printf 'oracle_repository=%s\n' "$oracle_repository"
      printf 'corpus=%s\n' "$corpus"
      printf 'source_repository=%s\n' "$source_repository"
      printf 'source_commit=%s\n' "$source_commit"
      printf 'manifest=%s\n' "$source_manifest"
    } >> "$output"

    [[ "$source_repository" =~ ^[^/[:space:]]+/[^/[:space:]]+$ ]] ||
      die 'FINAL_CONFORMANCE_REPOSITORY is not configured; refusing to run an unpinned corpus'
    [[ "$source_commit" =~ ^[0-9a-fA-F]{40}$ ]] ||
      die 'FINAL_CONFORMANCE_COMMIT is not a full 40-character SHA'
    [[ "$oracle_tag" == 3.4.0 ]] || die 'ORACLE_TAG must be 3.4.0'
    [[ "$oracle_commit" =~ ^[0-9a-fA-F]{40}$ ]] ||
      die 'ORACLE_COMMIT must be a full 40-character SHA'
    [[ -n "$corpus" ]] || die 'FINAL_CONFORMANCE_CORPUS is not configured; refusing to run a non-final corpus'
    [[ -d "$corpus" ]] || die "final conformance corpus is not a directory: $corpus"
    [[ -s "$source_manifest" ]] || die "final conformance provenance manifest is missing: $source_manifest"
    corpus_commit="$(git -C "$corpus" rev-parse HEAD 2>/dev/null || true)"
    printf 'corpus_commit=%s\n' "$corpus_commit" >> "$output"
    [[ "$corpus_commit" == "$source_commit" ]] ||
      die "checked-out corpus commit $corpus_commit does not match configured commit $source_commit"
    command -v jq >/dev/null 2>&1 || die 'jq is required to validate conformance results'
    jq -e \
      --arg oracle_repository "$oracle_repository" \
      --arg oracle_tag "$oracle_tag" \
      --arg oracle_commit "$oracle_commit" \
      '.oracle_repository == $oracle_repository and
       .rippled_tag == $oracle_tag and
       .rippled_commit == $oracle_commit and
       (.recorder_commit | strings | test("^[0-9a-fA-F]{40}$"))' \
      "$source_manifest" >/dev/null || die 'final corpus provenance manifest is incomplete or unpinned'
    cp "$source_manifest" "$evidence_dir/conformance-manifest.json"
    recorder_commit="$(jq -r '.recorder_commit' "$source_manifest")"
    {
      printf 'recorder_commit=%s\n' "$recorder_commit"
      sha256sum "$source_manifest"
    } >> "$output"

    json_count="$(find "$corpus" -type f -name '*.json' ! -path "$source_manifest" -print | wc -l | tr -d ' ')"
    [[ "$json_count" =~ ^[1-9][0-9]*$ ]] || die 'final conformance corpus contains no JSON fixtures'

    result_log="$evidence_dir/conformance-replay.log"
    replay_command='GOXRPL_FIXTURES_DIR="$FINAL_CONFORMANCE_CORPUS" GOXRPL_CONFORMANCE_REQUIRED=1 go test -count=1 -timeout 30m -v ./internal/testing/conformance/...'
    printf 'command=%s\nfixture_count=%s\n' "$replay_command" "$json_count" >> "$output"
    set +e
    GOXRPL_FIXTURES_DIR="$corpus" GOXRPL_CONFORMANCE_REQUIRED=1 \
      go test -count=1 -timeout 30m -v ./internal/testing/conformance/... > "$result_log" 2>&1
    replay_status=$?
    set -e
    cat "$result_log"
    pass_count="$(grep -cE -- '^[[:space:]]+--- PASS: TestConformance/' "$result_log" || true)"
    fail_count="$(grep -cE -- '^[[:space:]]+--- FAIL: TestConformance/' "$result_log" || true)"
    skip_count="$(grep -cE -- '^[[:space:]]+--- SKIP: TestConformance/' "$result_log" || true)"
    {
      printf 'replay_status=%s\n' "$replay_status"
      printf 'passed=%s\n' "$pass_count"
      printf 'failed=%s\n' "$fail_count"
      printf 'skipped=%s\n' "$skip_count"
    } >> "$output"
    (( replay_status == 0 && pass_count > 0 && fail_count == 0 )) ||
      die 'final conformance replay failed or produced no passing fixtures'
    ;;

  aggregate)
    results_file="$evidence_dir/needs-results.txt"
    [[ -s "$results_file" ]] || die 'needs-results.txt is missing or empty'
    output="$evidence_dir/final-acceptance.txt"
    write_common_metadata "$output"
    status_failed=0
    {
      printf 'required_jobs=lint,generate,build,build-386,postgres,test,test-purego,test-mpt-crypto,peer-interop,peer-interop-final,consensus-smoke,consensus-smoke-final,test-repeated,conformance-final\n'
      printf 'needs_results=%s\n' "$results_file"
      printf '\n[producer-evidence]\n'
    } >> "$output"

    if [[ -s "$evidence_dir/workflow-commands.txt" ]]; then
      {
        printf '\n[workflow-commands]\n'
        cat "$evidence_dir/workflow-commands.txt"
      } >> "$output"
    else
      printf 'workflow_commands=missing\n' >> "$output"
      status_failed=1
    fi

    required_jobs=(
      lint generate build build-386 postgres test test-purego test-mpt-crypto
      peer-interop peer-interop-final consensus-smoke consensus-smoke-final
      test-repeated conformance-final
    )
    for job in "${required_jobs[@]}"; do
      result="$(awk -F= -v key="$job" '$1 == key {print $2; exit}' "$results_file")"
      printf 'job=%s status=%s\n' "$job" "${result:-missing}" >> "$output"
      if [[ "$result" != success ]]; then
        status_failed=1
      fi
    done

    shopt -s nullglob
    producer_files=("$evidence_dir"/producers/producer-*.txt)
    if (( ${#producer_files[@]} == 0 )); then
      printf 'producer_evidence=missing\n' >> "$output"
      status_failed=1
    else
      for producer_file in "${producer_files[@]}"; do
        cat "$producer_file" >> "$output"
        printf '\n' >> "$output"
        if [[ -n "$expected_sha" ]] &&
          ! grep --fixed-strings --line-regexp "tested_sha=$expected_sha" "$producer_file" >/dev/null; then
          printf 'producer_sha_mismatch=%s\n' "$producer_file" >> "$output"
          status_failed=1
        fi
        if ! grep --fixed-strings --line-regexp 'go_dirty=false' "$producer_file" >/dev/null; then
          printf 'producer_dirty=%s\n' "$producer_file" >> "$output"
          status_failed=1
        fi
      done
      producer_patterns=(
        lint generate build build-386 postgres test-integration-offer.txt test-integration.txt
        test-tx.txt test-core.txt test-libs.txt test-purego.txt 'test-mpt-crypto-*'
        peer-interop.txt peer-interop-final.txt 'consensus-smoke-*' consensus-smoke-final.txt
        test-repeated conformance-final
      )
      for pattern in "${producer_patterns[@]}"; do
        if [[ "$pattern" == *.txt ]]; then
          matches=("$evidence_dir/producers/producer-$pattern")
        else
          matches=("$evidence_dir"/producers/producer-${pattern})
        fi
        if (( ${#matches[@]} == 0 )); then
          printf 'producer_evidence_missing=%s\n' "$pattern" >> "$output"
          status_failed=1
        fi
      done
    fi

    if [[ -n "$expected_sha" ]]; then
      if ! grep --fixed-strings --line-regexp "tested_sha=$expected_sha" "$output" >/dev/null; then
        printf 'sha_validation=failed\n' >> "$output"
        status_failed=1
      else
        printf 'sha_validation=passed\n' >> "$output"
      fi
    fi
    printf 'overall_status=%s\n' "$status_failed" >> "$output"
    (( status_failed == 0 )) || exit 1
    ;;

  *)
    usage
    ;;
esac

#!/usr/bin/env bash
# The required run executes the complete corpus; filters affect reporting only.
set -euo pipefail
cd "$(dirname "$0")/.."
corpus="${GOXRPL_FIXTURES_DIR:-}"
filter=""
failing=false
list_fail=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --corpus)
      [[ $# -ge 2 && -n "$2" ]] || { echo 'error: --corpus requires a path' >&2; exit 2; }
      corpus="$2"; shift 2 ;;
    --corpus=*) corpus="${1#*=}"; shift ;;
    --failing) failing=true; shift ;;
    --list-fail) list_fail=true; shift ;;
    --help|-h)
      echo "Usage: $0 --corpus PATH [SUITE_FILTER] [--failing] [--list-fail]"
      echo 'The complete pinned corpus always executes. SUITE_FILTER filters the report.'
      exit 0 ;;
    --*) echo "error: unknown option $1" >&2; exit 2 ;;
    *) [[ -z "$filter" ]] || { echo 'error: only one suite filter is supported' >&2; exit 2; }
       filter="$1"; shift ;;
  esac
done
[[ -n "$corpus" ]] || { echo 'error: --corpus PATH or GOXRPL_FIXTURES_DIR is required' >&2; exit 2; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
report="${GOXRPL_CONFORMANCE_REPORT:-$work/report.json}"
: > "$report"
status=0
GOXRPL_FIXTURES_DIR="$corpus" GOXRPL_CONFORMANCE_REQUIRED=1 GOXRPL_CONFORMANCE_REPORT="$report" \
  go test -count=1 -timeout "${CONFORMANCE_TIMEOUT:-300s}" -run '^TestConformance$' -v \
  ./internal/testing/conformance > "$work/run.log" 2>&1 || status=$?
if [[ ! -s "$report" || "$status" -ne 0 ]]; then
  tail -n 40 "$work/run.log"
fi
[[ -s "$report" ]] || { echo 'error: conformance produced no execution report' >&2; exit 1; }
python3 - "$report" "$filter" "$failing" "$list_fail" <<'PY'
import json
import sys
from pathlib import Path
report = json.loads(Path(sys.argv[1]).read_text())
columns = ('discovered', 'executed', 'passed', 'failed', 'skipped', 'excluded')
def empty_counts():
    return dict.fromkeys(columns, 0)
observed = {group: {name: empty_counts() for name in report[group]}
            for group in ('suites', 'families', 'profiles')}
total = empty_counts()
seen = set()
for case in report['cases']:
    if case['name'] in seen:
        sys.exit('error: duplicate case in conformance report')
    seen.add(case['name'])
    status = case['status']
    if status not in ('passed', 'failed', 'skipped', 'excluded'):
        sys.exit('error: unknown case status')
    if status in ('skipped', 'excluded') and not case.get('reason'):
        sys.exit('error: non-executed case has no reason')
    counters = [total]
    for group, field in (('suites', 'suite'), ('families', 'family'), ('profiles', 'profile')):
        if case[field] not in observed[group]:
            sys.exit('error: case has an unknown report dimension')
        counters.append(observed[group][case[field]])
    for counts in counters:
        counts['discovered'] += 1
        counts[status] += 1
        counts['executed'] += int(status in ('passed', 'failed'))
if total != report['total'] or any(observed[group] != report[group] for group in observed):
    sys.exit('error: reported dimensions do not reconcile with case observations')
for profile, reason in report['unsupported_profiles'].items():
    if not reason or report['profiles'].get(profile) != empty_counts():
        sys.exit('error: unsupported profile has observations or no reason')
print(f"Oracle: {report['oracle_repository']} {report['oracle_tag']} {report['oracle_commit']}")
print('Manifest SHA-256:', report['manifest_sha256'])
def row(name, counts):
    print(f"{name:46}" + ''.join(f"{counts[c]:11}" for c in columns))
print(f"{'':46}" + ''.join(f"{c:>11}" for c in columns))
row('Total', report['total'])
for group in ('suites', 'families', 'profiles'):
    print('\n' + group.capitalize())
    for name, counts in sorted(report[group].items()):
        if group == 'suites' and sys.argv[2] and sys.argv[2] not in name:
            continue
        if sys.argv[3] == 'true' and not counts['failed']:
            continue
        row(name, counts)
for case in report['cases']:
    if case['status'] in ('skipped', 'excluded') or (sys.argv[4] == 'true' and case['status'] == 'failed'):
        print(f"{case['status']}: {case['name']}: {case.get('reason', '')}")
for name, reason in sorted(report['unsupported_profiles'].items()):
    print(f"unsupported profile: {name}: {reason}")
print('\nCoverage limits:')
for limit in report['coverage_limits']:
    print('- ' + limit)
t = report['total']
if not t['executed'] or t['failed'] or t['skipped'] or t['discovered'] != t['executed'] + t['excluded'] or t['executed'] != t['passed'] + t['failed']:
    sys.exit('error: incomplete or failed conformance execution')
PY
exit "$status"

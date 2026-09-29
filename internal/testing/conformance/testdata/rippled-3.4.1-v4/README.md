# Private rippled 3.4.1 signed snapshot corpus

These files were emitted by an executed C++ recorder linked against
`XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`.
`manifest.json` pins every fixture, all four recorder source files, the recorder
commit, binary, build identity, configuration and amendment matrix.

Manifest schema 4 validates the historical recorder sources in
`scripts/oracle/recorded/<recorder_commit>/`, using their original paths and
checksums. These snapshots preserve the exact recording sources as the current
tooling evolves. The schema migration changed no fixture, recorded source hash,
recorder commit or binary identity.

The final recorder at `33836340bb531f260c7bad897e54e5483bacd479` produced **108 fixtures**,
**3,542 assertions and zero failures**. The 108 primary observations contain
80 `tesSUCCESS`, five applied `tec` results, one `terQUEUED`, and 22 other
rejections. Three applied prior submissions bring the total to **111 signed
submissions**. The independent close sets contain **88 transactions**, producing
**176 closed transaction leaves** including Batch inners. These are C++ recording
counts; the required Go execution reports provide the separate parity result.

| Submitted transaction family | Cases |
| --- | ---: |
| AccountSet | 25 |
| Batch | 48 |
| EscrowCancel | 1 |
| LoanBrokerSet | 1 |
| NFTokenAcceptOffer | 1 |
| OfferCreate | 1 |
| Payment | 18 |
| TicketCreate | 5 |
| TrustSet | 4 |
| VaultCreate | 4 |

All cases belong to `app/StrictOracleRecorder`. All 16 cleanup/lending/Batch/fix
boolean profiles execute; none is excluded or declared unsupported. The matrix
contains canonical and poisoned Batch wrappers plus all-or-nothing, only-one,
independent and until-failure cases on both fix states where Batch is enabled.
Common-engine scenarios cover flags/network ID, first-error ordering, single and
multisign authorization, master/regular keys, delegates/sponsors, fees, reserves,
sequence/tickets, expiry and prior-transaction checks.

## Recorded boundary

Fresh genesis persists the Amendments singleton. The recorder asserts that it
matches the configured supported profile; Go requires recorded effective rules
to equal rules loaded from authenticated ledger state, including permanent rules.
The parent includes its full header, rules, fees, state and transaction leaves.

Submission uses the C++ submit RPC with real signature checks. Each signed blob
is submitted once; the queue-pressure case first applies three recorded signed
submissions through the same open ledger and queue. Complete open-ledger state
is recorded even for rejection. The independent consensus transaction set is
captured before close. Close time derives from the pre-close header/resolution
and is checked against the result, not copied from the expected closed ledger.

The queue starts empty with explicitly recorded standalone thresholds. One case
creates real escalation and queues its primary transaction. Default network
thresholds, multiple retained queued candidates, eviction/order and multi-ledger
queue history remain unexecuted. Service consensus-mode tests preserve these
recorded queue settings while exercising real signature and consensus boundaries.

Persistent cleanup cases assert that an expired Offer/NFTokenOffer exists in the
parent and is absent after both submission and close. The Offer case also asserts
that a funded offer survives the failed fill-or-kill rollback. The invariant case
uses an explicitly malformed but replayable Escrow parent and observes
`tecINVARIANT_FAILED` fee-only recovery. Fatal `tefINVARIANT_FAILED` is unexecuted.

Seed `2016` produces four bounded signed XRP Payment samples: base-fee success,
insufficient balance, above-base fee and future sequence. This narrow generator
is separate from the Go soak, which permutes/replays recorded cases against their
C++ outputs. Neither establishes exhaustive state-space coverage.

## Executed build and provenance

The recording used the verified object-reuse build on macOS arm64. Production
objects came from the exact private commit; the tool verified the source copy
against the clean oracle, allowing only the existing test-only Batch recorder
instrumentation. It compiled the new recorder object and linked a separate
binary. The clean oracle and original binary were not modified.

The current recorder additionally requires reused production objects to relink
to the pinned production binary before compiling the recorder. The historical
recording predates that guard; its retained build logs and matching object files
were inspected independently during review.

| Identity | SHA-256 |
| --- | --- |
| Original exact-commit binary | `f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113` |
| Prior instrumented Batch test | `c4b44f108cbdc9cc03ceb55d30e3bf7452406ebc82efed9314247e18abf75b91` |
| Recorder binary | `e396315799376c609a2b887880db62e1e7dbdd056de808495603d07f551dfe5d` |
| Recorder C++ source | `044769fed7eb673961d5cc45b119f7ad5487046d850dc37de0cddff7e5886fd4` |
| Recorder configuration | `0faeb90f5ba7793217e873a85370f079bd6f7cae25e18145bca7307aaf8f326d` |
| Recorder compile command | `ebc54200f341c60562c8583a3649434b0e477bb040e70670554c3f1342afeb86` |
| Recorder link command | `3e3923ceba096fff03058cc9c69636a4d48f8f7af07a998bed40d5176d221985` |

Executed after committing the recorder sources:

```sh
python3 scripts/oracle/record-v4.py \
  --oracle /Users/thomashussenet/Documents/project_goXRPL/rippled-worktrees/v3.4.1-oracle \
  --old-build /private/tmp/issue-2011-oracle-build \
  --build-root /private/tmp/issue-2016-oracle-build \
  --fixture-dir /private/tmp/issue-2016-recordings --record
```

The tool writes `build-identity.json` and `record-v4.log` under the build root.
It executes `xrpld --unittest=StrictOracleRecorder --unittest-jobs 1` with the
selected fixture directory. The final log reports:

```text
xrpl.app.StrictOracleRecorder had 0 failures.
11.7s, 1 suite, 1 case, 3542 tests total, 0 failures
```

The expected invariant-failure diagnostics in this log belong to the deliberate
malformed Escrow case; the suite itself passes.

## Regeneration and replay

With an authorized clean private checkout and its native build dependencies:

```sh
python3 scripts/oracle/record-v4.py --clean-build \
  --oracle /path/to/clean/private-3.4.1 \
  --build-root /tmp/new-private-3.4.1-recorder --record
```

The committed recording used object reuse; clean-build mode was not executed
for it. That mode archives the exact commit, adds the recorder, resolves the
pinned Conan lockfile and builds `xrpld`. Keep regenerated files separate until
the manifest is rebuilt and required replay passes. Binary hashes vary by build.

Commit the four recorder files before recording. Then invoke
`scripts/recorded-corpus-manifest.py --help` and supply the actual recorder commit,
binary, build identity, configuration and coverage limits. List each full path
`scripts/oracle/record-v4.py`, `scripts/oracle/record-v4.sh`,
`scripts/oracle/strict-corpus-config.json` and `scripts/oracle/strict_recorder.cpp`
with a separate `--recorder-source` argument. Manifest generation checks source
bytes against that commit and never rewrites fixture inputs or observations.
It also saves the four sources under `scripts/oracle/recorded/<recorder_commit>/`
and rejects any conflicting archived bytes.

Replay needs no private checkout or C++ build:

```sh
GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4 \
  GOXRPL_CONFORMANCE_REQUIRED=1 GOXRPL_CONFORMANCE_REPORT=/tmp/conformance-report.json \
  GOXRPL_ENGINE_EVIDENCE_REPORT=/tmp/engine-report.json GOXRPL_ENGINE_SOAK_SECONDS=10 \
  go test -race -count=1 -v ./internal/testing/conformance

go test -race -count=1 -v ./internal/ledger/service \
  -run '^TestServiceSnapshotExecutionFromDurableParent$'
```

See [execution evidence](../../../../../docs/engine-execution-evidence.md),
[conformance documentation](../../../../../docs/conformance.md), and the
[runner audit](../../../../../docs/conformance-runner-audit.md). Historical AMM
fidelity and transient open-view fixture gaps remain release coverage blockers.
These bounded cases do not establish whole-release parity.

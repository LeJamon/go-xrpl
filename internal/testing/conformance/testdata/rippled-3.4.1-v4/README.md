# Private rippled 3.4.1 signed snapshot corpus

These files were emitted by an executed C++ recorder linked against
`XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`. They are fresh recordings, not
renamed 3.4.0 fixtures. `manifest.json` pins every fixture and all four recorder
source files, the recorder commit, binary, build, configuration and matrix.

The C++ run produced **45 fixtures** with **1,354 assertions and zero failures**.
Its observations are 41 `tesSUCCESS` and four `temMALFORMED` results. Go replay
results are recorded separately by the mandatory conformance job and its JSON
report; the C++ assertion count is not a Go parity result.

| Transaction family | Cases |
| --- | ---: |
| Payment | 12 |
| Batch | 16 |
| AccountSet | 4 |
| TrustSet | 4 |
| TicketCreate | 4 |
| VaultCreate | 4 |
| LoanBrokerSet | 1 |

All cases belong to the actual C++ suite `app/StrictOracleRecorder`. All 16
cleanup/lending/Batch/fix boolean profiles execute; none is excluded or declared
unsupported. Batch-enabled profiles each contain a canonical Batch and a
CreatedNode-wrapped Batch. The latter succeeds with `fixBatchV1_2` disabled and
is rejected without ledger mutation with the fix enabled. Payment covers
Batch-disabled profiles. Account, trust, ticket and vault cases cover all four
cleanup/lending combinations with Batch and its fix enabled. The closed-ended
vault LoanBrokerSet case records its prerequisite VaultCreate in the parent
ledger and runs under the fully enabled profile.

## Recorded boundary and scope

Each case captures the complete closed parent before submission, including its
effective rules and fee schedule. The top-level signed transaction and the
pre-close consensus transaction set are independent inputs. Submission uses
the C++ RPC with real signature checks; observations name its `open_ledger`
engine boundary. Complete open-ledger state is recorded even for rejection.
Close time is computed from the pre-close header/resolution and checked against
the resulting ledger, rather than copied from that expected ledger.

The queue starts empty at its configured minimum fee metrics. The recorder
asserts this precondition and sets every declared queue option explicitly.
This corpus does not cover historical queue contents, load/escalation history,
RPC/parser rejection boundaries, every Batch wrapper, every loan operation or
all other transaction families. The existing Batch wrapper corpus remains a
separate regression suite. These 45 cases establish only their recorded
behaviors; whole-release parity is tracked by #2017.

## Executed build

The recording host used macOS arm64, Apple Clang 17.0.0, Conan 2.18.1,
CMake 4.1.2 and Ninja 1.13.0, with a Release `xrpld` target and tests enabled.
Production objects were reused from the already built exact private commit.
The source copy was checked against the clean oracle; its only prior change
was test-only Batch recorder instrumentation. A new StrictOracleRecorder object
was compiled and linked into a separate binary. The clean oracle and original
binary were not modified.

| Identity | SHA-256 |
| --- | --- |
| Original exact-commit binary | `f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113` |
| Conan lockfile | `9d5e382cce56445d65694ed3add13b0bccea58ff9d4e2aad65ac3c9c54089bc5` |
| Prior instrumented Batch test | `c4b44f108cbdc9cc03ceb55d30e3bf7452406ebc82efed9314247e18abf75b91` |
| Recorder binary | `164d20cd3247b82fa89f24330fce365ce5d19acef3d2b7657e9bb87cd7a35f24` |
| Recorder C++ source | `5451931911f0c45b788f0f249d694ac94fec7f77863a0bbc7dd5e23a86f9fea6` |
| Recorder configuration | `d72cdc5d25206979632f3a52b1c9c45223f37e93ae61948f01b50c5fcaed7461` |
| Recorder compile command | `4c17f299a3b36fd40d95bdacc1b30e9049e5eb95e4e38d1721dd2cb0bbdf1032` |
| Recorder link command | `3e3923ceba096fff03058cc9c69636a4d48f8f7af07a998bed40d5176d221985` |

The executed build/record command, from the recorder checkout, was:

```sh
python3 scripts/oracle/record-v4.py \
  --oracle /Users/thomashussenet/Documents/project_goXRPL/rippled-worktrees/v3.4.1-oracle \
  --old-build /private/tmp/issue-2011-oracle-build \
  --build-root /private/tmp/issue-2015-oracle-build --record
```

The command checks the private source identity and configuration before building.
It writes `build-identity.json` and `record-v4.log` under the selected build root.
The recording command inside it is:

```sh
GOXRPL_V4_FIXTURE_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4 \
  /private/tmp/issue-2015-oracle-build/build/xrpld \
  --unittest=StrictOracleRecorder --unittest-jobs 1
```

Its terminal result was:

```text
xrpl.app.StrictOracleRecorder had 0 failures.
1 suite, 1 case, 1354 tests total, 0 failures
```

## Regeneration and replay

With an authorized clean private checkout, Conan, CMake, Ninja and the native
compiler installed, a separate clean-build mode avoids the local object cache:

```sh
python3 scripts/oracle/record-v4.py --clean-build \
  --oracle /path/to/clean/private-3.4.1 \
  --build-root /tmp/new-private-3.4.1-recorder --record
```

The committed recordings used the verified object-reuse mode above; the
clean-build mode was reviewed but was not executed for this recording. It is
provided for independent reproduction. It archives the exact
commit, adds the recorder test, resolves the pinned Conan lockfile, and builds
only `xrpld`. Keep regenerated files separate until their manifest is rebuilt
and the required replay passes. Binary hashes depend on the platform/build;
regeneration must record its actual new identity.

Commit the four recorder files before recording, then use
`scripts/recorded-corpus-manifest.py --help` to inventory the generated fixtures.
List each of `scripts/oracle/record-v4.py`, `record-v4.sh`,
`strict-corpus-config.json` and `strict_recorder.cpp` with a separate
`--recorder-source` argument, using their full repository-relative paths. Supply
the recorded binary, configuration, recorder commit, build identity and explicit
coverage limits. The generator validates source bytes against that commit and
does not rewrite fixture inputs or observations.

Replay requires no private checkout or C++ build:

```sh
GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4 \
  GOXRPL_CONFORMANCE_REQUIRED=1 GOXRPL_CONFORMANCE_REPORT=/tmp/conformance-report.json \
  go test -race -count=1 -v ./internal/testing/conformance
just conformance --corpus internal/testing/conformance/testdata/rippled-3.4.1-v4
```

See [conformance documentation](../../../../../docs/conformance.md) and the
[#1503 fidelity audit](../../../../../docs/conformance-runner-audit.md) for
validation, negative controls, reporting, and the legacy runner's disposition.

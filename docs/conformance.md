# Conformance

The release oracle is private **XRPLF/xrpld-private**, tag **3.4.1**, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`. The clean local checkout is
`rippled-worktrees/v3.4.1-oracle`. A public 3.4.0 corpus or a version string
alone cannot establish parity with that source. [Release sign-off](release-3.4.1.md)
remains a separate gate.

## Required signed snapshot corpus

`internal/testing/conformance` executes the committed v4 corpus under
`testdata/rippled-3.4.1-v4` during ordinary package tests. Required commands
select the corpus explicitly and reject an absent selection:

Relative corpus paths are resolved from the repository root, including when
`go test` runs from the package directory. Absolute paths select external corpora.

```sh
just conformance --corpus internal/testing/conformance/testdata/rippled-3.4.1-v4
GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4 \
  GOXRPL_CONFORMANCE_REQUIRED=1 GOXRPL_CONFORMANCE_REPORT=/tmp/conformance-report.json \
  go test -race -count=1 -v ./internal/testing/conformance
```

Each case starts from the C++ recorder's complete parent ledger, captured before
the tested submission: header, state entries, transaction/metadata leaves,
effective rules and fees. Explicit rules must equal the rules loaded from the
authenticated Amendments SLE, including permanent rules. The signed transaction
bytes, optional `pre_submit` history and close inputs are recorded separately
from expectations. Prior submissions execute in order through the same queue. Go verifies the parent roots, submits
those unchanged bytes with signature checks enabled, and compares the first
observed boundary, symbolic/numeric TER, applied/queued flags, fees and state.
Closing the recorded transaction set must reproduce every state and
transaction/metadata byte, AccountHash, TxHash, full header and ledger hash.
The same closed leaves also execute through production inbound replay. Applied
submissions must retain their signed bytes and returned diagnostic metadata in
the open transaction map; queued submissions must remain in the actual queue.
Rejected transactions must preserve state. Inputs are never autofunded,
renumbered, re-signed, rewritten, or retried to fit an expected result.

The manifest binds fixture bytes and recorder source to SHA-256 checksums. It
records oracle repository/tag/commit, recorder commit, binary hash, build
identity, configuration hash and all 16 combinations of `fixCleanup3_4_0`,
`LendingProtocolV1_1`, `BatchV1_1` and `fixBatchV1_2`. Actual parent rules
must match the advertised profile. Supported profiles must each have executable
cases; incomplete matrices and wholly excluded corpora fail.
Unsupported profiles must have a reason and no fixture files; the generator
accepts `--unsupported-profile ID=REASON` only for fix-enabled, Batch-disabled
combinations. The committed corpus records every combination as supported.

Unknown fields, duplicate JSON keys, unknown amendments, dependency/step fields,
malformed snapshots, changed checksums, mixed oracle identities, missing files,
unreadable files and symlinks fail before replay. Contract tests exercise these
controls and deliberately change expected outcomes/state/metadata to prove the
comparator reports mismatches.

The recorded open-state comparison exposed premature transaction threading in
Go. The shared commit path now leaves `PreviousTxnID`/`PreviousTxnLgrSeq` and
total XRP unchanged during open submission, while charging the account fee and
consuming its sequence or ticket. Closed application performs threading and
fee destruction. Go retains its diagnostic metadata API without committing
previewed threading to the open view. Success, fee-claim recovery, dry runs and
new account creation have regression coverage. Existing threading and metadata
regressions explicitly select closed application or rebuild their setup at
close, preserving their original byte and hash expectations.

## Reading the evidence

The required CI job retains the corpus and engine JSON reports, deterministic
order/soak execution logs, and durable ledger service execution logs. See
[engine evidence](engine-execution-evidence.md) for the execution stages and
limits. Corpus reports reconcile
`discovered = executed + skipped + excluded` and
`executed = passed + failed`, both overall and by suite, transaction family
and amendment profile. Every exclusion or skipped case has a reason. Zero
execution and filtered-out cases fail the required run. The summary command's
optional suite filter, `--failing` and `--list-fail` affect presentation;
the complete corpus still executes. `CONFORMANCE_TIMEOUT` defaults to 300s.

A successful run proves the recorded cases under their recorded profiles.
The manifest's `coverage_limits` and the corpus README describe unrecorded
surfaces. This corpus does not by itself establish full release parity. The private 3.4.1
peer and consensus jobs verify the pinned oracle artifact and runtime definitions
separately. Historical public-oracle checks remain compatibility checks; they
cannot substitute for private 3.4.1 evidence in the release gate.

## Recorder and legacy runner

The [runner fidelity audit](conformance-runner-audit.md) reconciles the already
completed safeguards and remaining v3 assumptions with the required v4 path.

See the corpus README for the exact executed C++ build, recording commands,
checksums and coverage inventory. `scripts/recorded-corpus-manifest.py` verifies
the clean private checkout and the recorder commit, inventories already-recorded
files, and never rewrites fixture inputs or expected outputs. Regeneration needs
authorized access to the private oracle; replay of the committed corpus does not.

The v3 runner remains for diagnostic contract tests only. Its historical funding,
AMM-address rewriting, dependency and named-step override machinery is not used
by required conformance or differential fuzz replay. The old
`scripts/conformance-out-of-scope.txt` policy applies only to that legacy runner;
it cannot silently exclude Batch, Vault or other v4 cases.

- [Architecture](architecture.md) describes the transaction pipeline.
- [Contributing](../CONTRIBUTING.md) describes the oracle-based implementation workflow.

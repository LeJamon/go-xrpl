# Transaction engine execution evidence for private rippled 3.4.1

This records bounded engine evidence for [#2016](https://github.com/LeJamon/go-xrpl/issues/2016).
It does not establish whole-node or exhaustive transaction-engine parity.
The release gate remains [#2017](https://github.com/LeJamon/go-xrpl/issues/2017).

## Candidate and reference

The initial Go candidate is `v3.4.1` at
`02f4f17c7d1c1b5676b112ddfa669655bcff4415`. The reference is the clean private
`XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`. The local reference worktree is
`rippled-worktrees/v3.4.1-oracle`; its common Git directory belongs to
`xrpld-private`. Older repository instructions and historical 3.4.0 fixtures
are not the oracle for this work.

The reference binary used for the checks below has SHA-256
`f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113`.
Its exact-commit production build provenance is recorded by the
[#2015 corpus work](https://github.com/LeJamon/go-xrpl/pull/2022).
Checking the version string alone is insufficient.

PRs #2018, #2019 and #2020 are in the release base. This branch also includes
the prerequisite commits from [#2021](https://github.com/LeJamon/go-xrpl/pull/2021)
and [#2022](https://github.com/LeJamon/go-xrpl/pull/2022), integrated locally;
no hosted prerequisite PR was merged by this work. The requested PR base is
`v3.4.1`.

The required conformance CI job records `tested_sha`, working-tree cleanliness,
oracle commit and manifest SHA-256 with the execution counts. Its uploaded
report identifies the actual tested candidate; the starting revision above is
not a claim about the final head.

## Demonstrated mismatch and repair

The signed AccountSet scenario paying one drop returned `terQUEUED` in Go and
`telINSUF_FEE_P` in C++. [#2024](https://github.com/LeJamon/go-xrpl/issues/2024)
was attached to the parent release tracker and repaired in a separate commit.
Go now enforces the transaction-specific, load-scaled base fee on every open
view, including queue admission at normal load. Closed-ledger execution still
skips fee adequacy; the eligible zero-fee SetRegularKey operation remains valid.

Signed queue regressions reject fees of zero, one and nine drops against a
ten-drop base. Restoring the old fee predicate makes all three fail with
`terQUEUED`. The ten-drop and contextual free-key controls pass. The repaired
candidate passes the expanded diagnostic oracle replay; only the final pinned
corpus report below is published execution evidence.

## Executed checks

The Go commands below ran with `GOCACHE=/private/tmp/issue-2016-go-cache` because
the sandbox could not write the host's default Go cache. `just` supplies the
repository's CGO and native library environment.

| Check | Result | Evidentiary limit |
| --- | --- | --- |
| `just test-pkg './internal/ledger/inbound/... ./internal/ledger/openledger/... -count=1'` | Pass on initial candidate | Existing Go production-path regressions; no newly recorded C++ input |
| `just test-pkg './internal/testing/accountset/... ./internal/testing/multisign/... ./internal/testing/networkid/... ./internal/testing/ticket/... -race -count=1'` | Pass on initial candidate | Independent Go integration cases |
| Private `xrpld --unittest=NetworkID,AccountSet,Ticket,MultiSign --unittest-jobs 1` | 4 suites, 43 cases, 4,979 assertions; zero failures | Oracle-only execution; not a shared-input comparison |
| `just test-pkg './internal/tx/xchain/... -race -count=1'` with the added `TestXChainModifyBridgeBinaryRoundTrip` | Pass | Typed binary parsing and round-trip regression; no signature or engine-result claim |
| `just test` | 164 packages passed, zero cached package results | Full current Go regression suite; independent Go tests do not establish C++ parity |
| `just build-all`, `just vet`, strict `golangci-lint run --config .golangci.yml` | Pass | Compile/static checks on the implementation candidate |
| Same XChain regression with a temporary Go overlay restoring the old direct `ReflectFlatten` call | All four bridge combinations reject parsing with `not a valid json` | Negative control proving that the regression detects the original defect; tracked production files were unchanged |

The new XChain cases cover XRP/XRP, XRP/IOU, IOU/XRP and IOU/IOU bridge field
representations, raw-byte preservation, canonical-field matching and binary
re-encoding. They reconstruct the field shape described in #1737. They do not
replay its original transaction hash, whose complete signed blob was not
provided in that issue.

## Historical gap disposition

| Issue | Candidate evidence | Disposition for this gate |
| --- | --- | --- |
| [#1737](https://github.com/LeJamon/go-xrpl/issues/1737), typed XChain parsing | `internal/tx/xchain/helpers.go:flattenXChain` converts the typed bridge to the four-field map; the new direct ModifyBridge regression passes and detects removal of that conversion | Original typed-struct defect is repaired. Exact historical transaction execution remains unexecuted |
| [#1498](https://github.com/LeJamon/go-xrpl/issues/1498), AMM test fidelity | `internal/testing/amm/amm_clawback_test.go` logs several expected balance/deletion outcomes without asserting them; `amm_calc_test.go` contains failure-path skips; helpers include approximate comparisons | Required coverage gap. The existing [#1639–#1643](https://github.com/LeJamon/go-xrpl/pull/1639) repair stack is not in this candidate. Passing the existing suite cannot resolve the gap |
| [#1126](https://github.com/LeJamon/go-xrpl/issues/1126), transient fixture state | The legacy fixture schema has no general serialized open-view SLE insert/erase operation; isolated closed-parent snapshots alone do not represent transient changes after opening the ledger | Required coverage gap for those oracle scenarios. Restore and execute the affected open-view cases before claiming coverage |
| [#1508](https://github.com/LeJamon/go-xrpl/issues/1508), engine fuzz coverage | Existing property harness generates six transaction kinds, ignores most TERs, and compares two Go executions without C++ | Supplementary property evidence only. Required differential soaks must report real executed cases/stages/profiles, reject zero meaningful execution and compare against the private oracle |

These are coverage findings, not newly demonstrated ledger divergences.
Any supported behavior gap exposed while closing them must be tracked as a
blocking child of #2010, repaired, and rerun on the merged candidate.

## Inherited evidence outside the common engine

| Surface | Bounded existing evidence | Unverified here |
| --- | --- | --- |
| Codec | `codec/binarycodec/definitions/final_inventory_test.go`, amount wire vectors, bridge wire validation and hash-prefix tests | Current private-oracle execution of every type/field and parser rejection boundary |
| Crypto | Address/seed derivation vectors, Ed25519 tests, secp256k1 oracle/Wycheproof cases and native backend CI | Full signature matrix and native MPT capability profiles on this candidate |
| Ledger and SHAMap | SHAMap model fuzzing, prefix/node-placement/fetch-pack/sync validation tests; inbound/open-ledger baseline above | Complete ledger behavior beyond the bounded signed C++ snapshots and replay cases below |
| Consensus and peer | Router transaction-set learning/retry/replay tests, ledger acceptance and peer wire/relay tests | Live private-3.4.1 peer/consensus interoperability; historical Docker tags do not establish it |
| RPC | Transport, account, ledger, transaction, subscription and aggregate-price regression suites | Complete current RPC-versus-oracle boundary matrix |

The existence of these tests is an evidence index, not a statement that every
listed test ran in this work or that the surface is fully covered.

## Execution contract

Every pinned case goes through signed binary parsing, `openledger.SubmitDetailed`
with an explicit transaction queue, `openledger.BuildClosedLedger`, and
`inbound.ReplayDelta.GotResponse/Apply/Result`. Signature verification stays on.
The parent bytes, enabled rules, fees, network ID, close time and transaction
inputs come from the recording. Closed-ledger expectations never supply missing
transaction inputs or repair their sequence numbers.
Explicit rules must equal the rules loaded from the authenticated Amendments
SLE, including permanent rules; a recorded configuration override cannot silently
enable another amendment. Applied submissions must retain the original signed
bytes and the returned diagnostic metadata in the open transaction map. Queued
submissions must retain those signed bytes in the actual queue. Exact oracle
metadata comparisons apply to the closed ledger and replay.

Comparison includes the submission TER/code, applied/queued decision and fee,
post-submission state, every closed SLE and transaction/metadata leaf, total XRP,
AccountHash, TxHash and the complete ledger header hash. Replay must remain
incomplete before execution, complete afterward, return the compared ledger,
and preserve the parent.

`TestEngineExecutionOrder` repeats every case with seeds `0`, `1`, `2016` and
`18446744073709551615`. It permutes SLE insertion, parent transaction insertion,
rule-list order, proposed close-set order and received replay-leaf order. It
preserves signed bytes and compares every execution with the recorded C++
outputs. A bounded additional replay soak uses seed `2016`; CI requests ten
seconds. `FuzzEngineDifferentialOrder` exposes those same permutations to Go's
fuzzer. This loop exercises recorded cases; any separately generated C++ cases are
accounted for in the corpus provenance. It is not exhaustive state-space exploration.

The report separates completed cases, wire transaction types, TERs, profiles,
close inputs and replay leaves. Common-field presence and enabled-rule counts
are observations, not evidence that every behavior of that field or amendment
was exercised. Zero execution, exclusions, mismatches, missing stages, dirty
CI candidates and inconsistent accounting fail the required check.

## Ledger service boundaries

The service harness persists each recorded parent through the real ledger
persistence path, restarts with `StartupLoad`, and checks the loaded header,
rules, fees and maps. It executes prior submission history and the primary
signed submission through `Service.SubmitTransaction`, then calls
`AcceptConsensusResult` with the independently recorded close set and time.
Only after verifying the closed candidate does it mark that candidate validated
and check complete durable transaction history.

Cases with an applied submitted close-set transaction also run the standalone
`acceptLedgerAt` seam at the recorded time. Standalone service submission
intentionally bypasses signature verification; the non-standalone run and the
engine corpus retain signature verification. The required conformance CI job
retains `service-execution.log` alongside both JSON evidence reports.

## Replay negative controls

The committed inbound controls alter an ordinary transaction's result or affected
nodes, a Batch inner's result or affected nodes, or Batch inner presence/order.
They rebuild the peer transaction root and header hash while preserving the
account-state root. Rejection must leave the parent unchanged, retain failed
state, and make `Result` unavailable. A fresh retry must reconstruct authenticated
inputs and cannot promote the failed attempt.

These small adversarial fixtures are generated by Go with signature checks
explicitly disabled; they isolate replay verification. The signed C++ snapshot
replays above supply independent successful execution evidence.

A temporary Go overlay disabled the two generated-versus-peer metadata checks.
All four ordinary/Batch result-only and affected-node-only controls then failed
because the altered peer ledger was accepted. The overlay changed no tracked
production file. This proves the tests detect loss of the #1997 repair.

The existing service regression
`TestReplayFaultPersistsReproducedMetadataDisagreement` additionally sends altered
metadata through `Service.ApplyReplay`, persists the disagreement, preserves the
closed ledger, rejects recovery revalidation, and keeps replay blocked. It passed
in the module-wide run.

## Release limits

The source inventory and runtime report intentionally retain unexecuted rows.
Missing cases cannot be inferred from similarly named Go tests or from an
enabled amendment. The historical AMM fidelity and transient open-view gaps
above remain release blockers for the affected coverage in #2017. An unchanged
issue status alone is not a demonstrated 3.4.1 protocol defect.

Expanded signed C++ recordings, service-startup comparisons and final-head
results are being assembled in this branch. The final PR must publish their
actual counts and tested SHA before claiming completion of the execution work.

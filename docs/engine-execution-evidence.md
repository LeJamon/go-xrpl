# Transaction engine execution evidence for private rippled 3.4.1

This records bounded engine evidence from [#2016](https://github.com/LeJamon/go-xrpl/issues/2016)
and the remaining-gap repairs in [#2017](https://github.com/LeJamon/go-xrpl/issues/2017).
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

The original reference binary used for the oracle-only unit checks has SHA-256
`f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113`.
Its exact-commit production build provenance is recorded by the
[#2015 corpus work](https://github.com/LeJamon/go-xrpl/pull/2022).
Checking the version string alone is insufficient.

PRs #2018–#2023 and #2025 are merged into `v3.4.1`. The final-gap work
starts from release commit `8093a8e6c9a55c17ef19cd8c677522c8c5f1c33f`.
The historical checks below identify their original candidate; final acceptance
requires the separate evidence artifact for the resulting release commit.

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

## Historical #2016 execution

The Go commands below ran with `GOCACHE=/private/tmp/issue-2016-go-cache` because
the sandbox could not write the host's default Go cache. `just` supplies the
repository's CGO and native library environment.

| Check | Result | Evidentiary limit |
| --- | --- | --- |
| `just test-pkg './internal/ledger/inbound/... ./internal/ledger/openledger/... -count=1'` | Pass on initial candidate | Existing Go production-path regressions; no newly recorded C++ input |
| `just test-pkg './internal/testing/accountset/... ./internal/testing/multisign/... ./internal/testing/networkid/... ./internal/testing/ticket/... -race -count=1'` | Pass on initial candidate | Independent Go integration cases |
| Private `xrpld --unittest=NetworkID,AccountSet,Ticket,MultiSign --unittest-jobs 1` | 4 suites, 43 cases, 4,979 assertions; zero failures | Oracle-only execution; not a shared-input comparison |
| `just test-pkg './internal/tx/xchain/... -race -count=1'` with the added `TestXChainModifyBridgeBinaryRoundTrip` | Pass | Typed binary parsing and round-trip regression; no signature or engine-result claim |
| `just test` | 164 packages passed; 59 executed, 105 reused unchanged cached results | Full combined candidate regression suite; independent Go tests do not establish C++ parity |
| `just build-all`, `just vet`, strict `golangci-lint run --config .golangci.yml` | Pass | Compile/static checks on the implementation candidate |
| Same XChain regression with a temporary Go overlay restoring the old direct `ReflectFlatten` call | All four bridge combinations reject parsing with `not a valid json` | Negative control proving that the regression detects the original defect; tracked production files were unchanged |

The new XChain cases cover XRP/XRP, XRP/IOU, IOU/XRP and IOU/IOU bridge field
representations, raw-byte preservation, canonical-field matching and binary
re-encoding. They reconstruct the field shape described in #1737. They do not
replay its original transaction hash, whose complete signed blob was not
provided in that issue.

## Historical gap disposition

| Issue | Current disposition | Evidence limit |
| --- | --- | --- |
| [#1737](https://github.com/LeJamon/go-xrpl/issues/1737), typed XChain parsing | The merged bridge conversion and four asset-combination regressions preserve canonical fields and original bytes. Removing the conversion makes every regression fail. | The issue does not contain the original signed blob, so its historical transaction hash has not been replayed. |
| [#1498](https://github.com/LeJamon/go-xrpl/issues/1498), AMM test fidelity | Exact pool, LP, auction, clawback, freeze and path vectors replace permissive assertions. Ledger helpers fail on read/parse corruption, fees are filled dynamically, and translated suites are split by behavior. Offer funding uses the actual offer output asset and excludes frozen LP liquidity. | Source-derived Go regressions are distinct from shared signed C++ snapshots. MPTokensV2 is unsupported by both release registries; forced-enabled development tests do not establish shipped AMM/MPT support. |
| [#1126](https://github.com/LeJamon/go-xrpl/issues/1126), transient state | The v4 schema records serialized open-view insertions and erasures, including before prior submissions, without closing the ledger. The four NFT authorization and two MPT escrow cases are restored. | These deliberately altered open views are diagnostic inputs; they are not claims about reachable healthy-chain state. |
| [#1503](https://github.com/LeJamon/go-xrpl/issues/1503), runner fidelity | Required v4 execution uses authenticated independent parents, signed inputs, exact observations and fail-closed accounting. The [runner audit](conformance-runner-audit.md) dispositions legacy assumptions. | Legacy v3 setup/rewriting machinery remains diagnostic-only. |
| [#1508](https://github.com/LeJamon/go-xrpl/issues/1508), engine properties | Named stateful and phase traces, exact outcome/rollback/fee properties, overflow-safe supply, mandatory closes and specialized lifecycle targets replace permissive result handling. | Go property fuzzing is supplementary to the C++ corpus, not a differential oracle. |

Newly demonstrated supported-behavior mismatches are blocking children of
[#2010](https://github.com/LeJamon/go-xrpl/issues/2010). The final release run must
include their repairs; earlier green corpus runs do not resolve new cases.

## Go property targets

`internal/testing/enginefuzz` separates valid state transitions from explicit
common-field and apply-flag cases. The stable `go-v3.4.1-supported` and AMM-off
profiles are encoded in each input and have checked fingerprints. They retain
Go compatibility-only registrations and therefore are not described as exact
C++ capability sets. Native confidential MPT support has separate tagged tests.

The harness rejects unknown, internal, exception, bad-ledger and invariant-failure
results. It observes handler and invariant calls, checks non-applied state/fee
atomicity, claimed-fee and sequence effects, and overflow-safe XRP totals after
submission and every close, including a mandatory final close. Forced invariant,
catastrophic-result and accumulator-overflow controls prove these checks fail.

Named traces exercise every generated transaction kind and require applied
execution. Specialized traces cover AMM, ordinary MPT, NFToken, PermissionedDomain
and DEX, AccountDelete, Vault, Loan/Broker, Escrow, Batch inner execution, and
pseudo-account effects. Signing and retry cases use real signed input or explicit
apply flags. The Loan trace includes a counterparty-signed LoanSet, installments,
loan deletion and broker cover lifecycle. Batch verifies exact inner balances,
metadata and outer/inner phase counts through close replay.

The specialized generator varies amounts in AMM, MPT, NFT and Batch families;
other families select fixed stateful sequences. This is bounded reachability and
property coverage, not exhaustive state generation or all-amendment proof.

## Inherited evidence outside the common engine

| Surface | Bounded existing evidence | Unverified here |
| --- | --- | --- |
| Codec | `codec/binarycodec/definitions/final_inventory_test.go`, amount wire vectors, bridge wire validation and hash-prefix tests | Current private-oracle execution of every type/field and parser rejection boundary |
| Crypto | Address/seed derivation vectors, Ed25519 tests, secp256k1 oracle/Wycheproof cases and native backend CI | Full signature matrix beyond the native capability checks required by release CI |
| Ledger and SHAMap | SHAMap model fuzzing, prefix/node-placement/fetch-pack/sync validation tests; inbound/open-ledger baseline above | Complete ledger behavior beyond the bounded signed C++ snapshots and replay cases below |
| Consensus and peer | Router transaction-set learning/retry/replay tests, ledger acceptance and peer wire/relay tests | Complete consensus behavior beyond the live private-3.4.1 interop and smoke checks required by release CI |
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
rule-list order, proposed close-set order and received replay-leaf order,
including each historical transition. It preserves submission and history
sequence, signed bytes and compares every execution with the recorded C++
outputs. A bounded additional replay soak uses seed `2016`; CI requests ten
seconds. `FuzzEngineDifferentialOrder` exposes those same permutations to Go's
fuzzer. This loop exercises recorded cases; any separately generated C++ cases are
accounted for in the corpus provenance. It is not exhaustive state-space exploration.

A temporary overlay removing the retry loop makes the seeded insufficient-balance
Payment fail because a retriable transaction remains at close. The same signed
case passes normally with the oracle fee-only result. This confirms retry-pass
execution; the queue-pressure case alone does not establish it.

The report separates completed cases, wire transaction types, TERs, profiles,
close inputs and replay leaves. Totals include historical submissions and
transitions, with separate history and exact queue-comparison counts. Common-field presence and enabled-rule counts
are observations, not evidence that every behavior of that field or amendment
was exercised. Zero execution, exclusions, mismatches, missing stages, dirty
CI candidates and inconsistent accounting fail the required check.

The invariant recovery scenarios deliberately record malformed closed parents.
An Escrow amount at the native-supply bound triggers `tecINVARIANT_FAILED` and
fee-only recovery on signed cancellation. A positive AccountRoot balance one
drop above the initial supply remains invalid after fee-only recovery and
triggers `tefINVARIANT_FAILED`, without applying the transaction or charging a
fee. Ledger-entry decoding preserves that representable native wire amount;
ordinary JSON amount validation retains its supply cap. The account state,
headers and replay inputs are authenticated even though the state is deliberately
invalid. These cases do not claim that a healthy chain can create either parent.

## Ledger service boundaries

The service harness persists each recorded parent through the real ledger
persistence path, restarts with `StartupLoad`, and checks the loaded header,
rules, fees and maps. It executes prior submission history and the primary
signed submission through `Service.SubmitTransaction`, then calls
`AcceptConsensusResult` with the independently recorded close set and time.
Only after verifying the closed candidate does it mark that candidate validated
and check complete durable transaction history.

The queue configuration is preserved exactly in both service runs. Cases with
recorded history restart from the earliest authenticated parent and execute
each submission and close before reaching the primary case. The history sets
fee metrics through actual closes and carries retained local transactions
through open-ledger acceptance. Queue checks compare complete signed membership,
fee levels, ledger occupancy and the optional maximum size. Expected metrics
never initialize the queue.

The expanded cases exercise fee-based eviction, default network thresholds and
three linked closes with queue promotion and local transaction re-admission.
The service regressions also cover current-view queries and standalone inclusion
of peer submissions and queue-promoted transactions; see
[#2028](https://github.com/LeJamon/go-xrpl/issues/2028).

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
enabled amendment. The historical AMM assertions, transient open-view cases,
queue-history inputs and engine property gaps are addressed above; an unchanged
issue status alone is not a demonstrated 3.4.1 protocol defect.

The expanded recording contains 118 cases across 16 amendment profiles. It adds
four NFT authorization and two missing-MPT escrow cases, persistent invariant
failure, queue eviction, default network fee thresholds and linked queue history.
Its source commit, archived recorder, binary and complete build identity are in
the [corpus manifest](../internal/testing/conformance/testdata/rippled-3.4.1-v4/manifest.json).

Required runs must execute every case with zero failures, skips or exclusions.
Four fixed seeds produce 472 complete case executions, followed by the requested
ten-second seed-2016 soak. All history transitions also execute signed submission,
close and inbound replay; their transaction and leaf counts are included in the
runtime totals. Durable service execution covers the same corpus through
consensus close and explicitly reports the cases applicable to standalone close.

The retained JSON reports identify the exact clean tested head, completed stages,
transaction counts and measured soak count. Those reports and the explicit
coverage limits above define the bounded result. Post-merge release evidence is
required before #2017 or the parent tracker can close.

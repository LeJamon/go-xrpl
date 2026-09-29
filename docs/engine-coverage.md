# Engine coverage inventory

`[inventory.json](../scripts/engine-coverage/inventory.json)` is a checked-in
source and registry audit for issue #2016. It is pinned to
`XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`, and records hashes for the oracle
registry sources. The protocol base parent is
`02f4f17c7d1c1b5676b112ddfa669655bcff4415`. The merged #2021/#2022 revision
`b002da57fb415b40a1150f0e0dc82f764a340556` is recorded only as the prerequisite
baseline used while refreshing this snapshot; it is not asserted to be the
current working-tree `HEAD`. Current Go source bytes are identified by the
snapshot's `source_sha256` entries, while the runtime `HEAD` belongs in a
separate execution report.

Generate or check the snapshot from the Go repository root. With no argument,
the generator resolves the sibling `../rippled-worktrees/v3.4.1-oracle`
checkout and rejects a checkout whose `HEAD` is not the pinned commit.

```sh
python3 scripts/engine-coverage/generate.py
python3 scripts/engine-coverage/generate.py --check
```

The generator reads the oracle transaction and amendment registries, common
field declarations, Go constants, runtime registration files, templates,
method receivers, and the checked-in v4 manifest. It fails on missing or
duplicate registry rows, common-field drift, missing stage source files or
symbols, and stale output. It does not run a transaction engine and does not
turn a named test into an execution claim.

The registry snapshot contains:

| Registry | Rows |
| --- | ---: |
| Oracle active transaction macro | 82 |
| Go runtime transaction registry | 82 |
| Go historical enum rows absent from the oracle macro | 4 |
| Oracle amendment macro | 108 |
| Go amendment registry | 112 |
| Go-only amendment rows | 4 |
| Common transaction fields | 20 |

The four historical Go enum rows are `NickNameSet` (6), `Contract` (9),
`SpinalTap` (11), and `HookSet` (22). The four Go-only amendment rows are
`InvariantsV1_1`, `NonFungibleTokensV1`, `fixNFTokenDirV1`, and
`fixNFTokenNegOffer`; there are no oracle-only amendments. Each transaction
row includes both field lists, source paths, typed method locations, static
amendment references, and explicit status values. Static
`go_required_amendment_references` can be a union collected from conditional
branches; it is a source reference, not an unconditional requirement.

The engine map records these dispatch and stage seams:

| Path | Go symbols | Oracle symbols |
| --- | --- | --- |
| Registry and template | `Register`, `NewFromType`, `SupportedTypes`, `commonFields`, `FormatCommonFields` | `TxFormats::getCommonFields`, `STTx::STTx`, `getTxFormat` |
| Normal entry | `Engine.Apply`, `Engine.ApplyWithContext`, `Engine.applyWithContext` | `apply`, `preflight`, `preclaim`, `doApply` in `src/libxrpl/tx/apply.cpp` and `applySteps.cpp` |
| Normal preflight | `Engine.preflight`, `preflightStructure`, `Engine.preflight1`, `Engine.preflight0`, `runTypePreflight`, `Engine.verifySignatures` | `preflight0`, `Transactor::preflight1`, `Transactor::preflight2`, `invokePreflight` |
| Normal preclaim | `Engine.preclaim`, `Engine.checkSeqProxy`, `Engine.checkPriorTxAndLastLedger`, `Engine.checkPermission`, `Engine.checkSign`, `Engine.checkMultiSign` | `invokePreclaim`, `Transactor::checkSeqProxy`, `Transactor::checkPriorTxAndLastLedger`, `Transactor::checkSign` |
| Normal apply and invariants | `Engine.doApply`, `Engine.invokeApply`, `Engine.invokeApplyInner`, `Engine.runInvariants`, `Engine.CheckInnerInvariants` | `Transactor::apply`, `Transactor::doApply`, `InvariantCheck` |
| Pseudo path | `Engine.ApplyPseudo`, `Engine.pseudoPreflight`, `Engine.pseudoPreclaim`, `Engine.applyPseudoTransaction` | pseudo transaction dispatch in `applySteps.cpp` |
| Batch inner path | `Engine.preflightInner`, `Engine.preclaimInner`, `Engine.ApplyInnerTransaction`, `BatchInnerApplier.ApplyInnerTransactions` | `invokePreflight`/`invokePreclaim` with `parentBatchId`, Batch inner apply and invariant calls |

The engine map is a path inventory. It does not assert that each symbol or
each transaction row was executed in the same run.

## Common fields and stage accounting

The following table accounts for all 20 common fields in oracle order. The same
mapping is machine-readable under `common_fields.stage_mapping`. `Go` and
`oracle` entries there contain exact source paths and symbols; the source links
below are convenient entry points.

| Field | Go stage/source | Oracle stage/source | Classification and uncovered edge |
| --- | --- | --- | --- |
| `TransactionType` | `registry.go`: `FromJSON`, `NewFromType`; `template.go`: `commonFields` | `STTx.cpp`: `STTx::STTx`, `getTxFormat`; `TxFormats.cpp`: `TxFormats::getCommonFields` | Dispatch/template. Unknown type, template rejection, and every type dispatch need runtime evidence. |
| `Flags` | `engine/preflight.go`: `Engine.preflight0`, `checkFlagsMask` | `Transactor.cpp`: `preflight0` | Preflight mask, then type-specific use. Invalid-mask precedence and each permitted bit remain unexecuted. |
| `SourceTag` | `transaction.go`: `Common.ToMap`; `paychan/helpers.go`: `newPayChannelData` | `CheckCreate::doApply`, `EscrowCreate::doApply`, `PaymentChannelCreate::doApply` | Wire plus selected ledger effects. Presence/absence and every consuming handler need execution evidence. |
| `Account` | `preflight.go`: `checkAccountPresent`; `preclaim.go`: `Engine.preclaimLoadAccount`, `Engine.checkSign` | `Transactor::preflight1`, `Transactor::checkSign`, `invokePreclaim` | Source identity. Zero, missing, delegated, pseudo, and uncreated-account edges remain. |
| `Sequence` | `preflight.go`: `Engine.preflightSequence`; `preclaim.go`: `Engine.checkSeqProxy`; `do_apply.go`: `Engine.applyPreApplyAccountChanges` | `STTx::getSeqProxy`; `Transactor::checkSeqProxy`, `consumeSeqProxy` | Sequence proxy and account mutation. Mismatch, retry, and increment cases remain. |
| `PreviousTxnID` | `transaction.go`: `Common.ToMap`; `metadata.go`: `buildAffectedNodeInner` | `STLedgerEntry::thread`; `ApplyStateTable::apply` | Wire plus ledger metadata; no common transaction-engine gate in this oracle. Transaction presence, stamping, and zero-hash omission are separate. |
| `LastLedgerSequence` | `preclaim.go`: `Engine.checkPriorTxAndLastLedger` | `Transactor::checkPriorTxAndLastLedger` | Preclaim expiry. Boundary equality and expired/account-hash interaction remain. |
| `AccountTxnID` | `preflight.go`: `Engine.preflightSequence`; `preclaim.go`: `Engine.checkPriorTxAndLastLedger`; `do_apply.go`: `Engine.applyPreApplyAccountChanges` | `Transactor::preflight1`, `checkPriorTxAndLastLedger`, `apply` | Preclaim guard plus account tracking. Mismatch, ticket prohibition, and successful update remain. |
| `Fee` | `preflight.go`: `Engine.validateFee`; `preclaim.go`: `Engine.checkFee`; `do_apply.go`: `Engine.applyPreApplyAccountChanges` | `Transactor::preflight1`, `checkFee`, `payFee`, `apply`; `applySteps.cpp`: `calculateBaseFee`, `doApply` | Validation, payer charge, and invariants. Malformed, floor, sponsored, retry, tec, and invariant cases remain. |
| `OperationLimit` | `template.go`: `commonFields`; `transaction.go`: `Common.ToMap` | `TxFormats.cpp`: `TxFormats::getCommonFields` | Wire/template only in v3.4.1; no common engine check or mutation found. Do not call value behavior covered. |
| `Memos` | `local_checks.go`: `PassesTransactionLocalChecks`, `LocalChecksFailureReason`, `serializedMemosLength` | `STTx.cpp`: `passesLocalChecks`, `isMemoOkay`; `apply.cpp`: `checkValidity` | Local submission check before consensus apply. Oversize, malformed child, parser, and cache edges remain. |
| `SigningPubKey` | `preflight.go`: `checkSigningKeyShape`, `Engine.verifySignatures`; `sign/signature.go`: `VerifySignature` | `Transactor.cpp`: `preflightCheckSigningKey`, `preflight2`, `checkSign` | Key shape then crypto/account authorization. Empty, malformed, regular, delegate, and dry-run cases remain. |
| `TicketSequence` | `preflight.go`: `Engine.preflightSequence`; `preclaim.go`: `Engine.checkSeqProxy`; `do_apply.go`: `Engine.consumeTicket` | `STTx::getSeqProxy`; `Transactor::checkSeqProxy`, `consumeSeqProxy` | Ticket proxy and consumption. Missing ticket, owner, conflict, recovery, and retry remain. |
| `TxnSignature` | `preflight.go`: `Engine.verifySignatures`; `sign/signature.go`: `VerifySignature` | `STTx::checkSign`, `checkSingleSign`; `Transactor::preflight2`, `checkSign` | Single-signature verification. Bad, missing, noncanonical, delegated, and era-prefix cases remain. |
| `Signers` | `preclaim.go`: `Engine.checkMultiSign`, `Engine.checkMultiSignForAccount`; `sign/signature.go`: `VerifyMultiSignatureCrypto`, `VerifyMultiSignature` | `STTx::checkSign`, `checkMultiSign`; `Transactor::checkSign`, `checkMultiSign`; `Sign.cpp`: `buildMultiSigningData` | Crypto then signer-list/quorum checks. Ordering, quorum, unauthorized, duplicate, delegate, and sponsor cases remain. |
| `NetworkID` | `preflight.go`: `Engine.validateNetworkID` | `Transactor.cpp`: `preflight0` | Network gate. Legacy, missing, wrong, matching, and pseudo cases remain. |
| `Delegate` | `preflight.go`: `checkDelegate`; `preclaim.go`: `Engine.checkPermission`, `Engine.checkSign`; `sponsor_fee.go`: `Engine.getFeePayer` | `Transactor::preflight1`, `checkSign`, `getFeePayer`; `STTx::getInitiator`, `getFeePayerID` | Delegated identity and authorization. Disabled, self, missing permission, signer, and payer failures remain. |
| `Sponsor` | `preflight.go`: `checkSponsorFields`; `preclaim.go`: `Engine.checkSponsor`; `sponsor_fee.go`: `Engine.getFeePayer`; `sponsor_reserve.go`: `readSponsorship`; `invariants/sponsorship.go`: `checkSponsorship` | `preflight1Sponsor`, `Transactor::checkSponsor`, `getFeePayer`; `InvariantCheck` | Sponsorship identity, fee/reserve, and invariant paths. Disabled, malformed, co-signed, prefunded, reserve, and invariant failures remain. |
| `SponsorFlags` | `preflight.go`: `checkSponsorFields`; `preclaim.go`: `Engine.checkSponsor`; `sponsor_fee.go`: `Engine.getFeePayer` | `preflight1Sponsor`, `Transactor::checkSponsor`, `getFeePayer` | Fee/reserve mode gate. Zero, masked, unsupported-type, and changing-sponsorship cases remain. |
| `SponsorSignature` | `preclaim.go`: `Engine.checkSign`; `sign/signature.go`: `VerifySponsorSignatureWithRules`, `VerifyMultiSignatureCrypto` | `STTx::checkSign`; `Sign.cpp`: `signatureField`, `signingPrefix`; `Transactor::checkSign` | Nested role signature. Missing sponsor, role prefix, single/multi, canonicality, and era cases remain. |

`SourceTag`, `Memos`, `OperationLimit`, and `PreviousTxnID` are therefore not
all equivalent to common preflight gates: the first has selected apply effects,
the second is a local-check surface, the third is wire/template-only in this
oracle, and the fourth is also used for ledger metadata threading. Field-list
parity alone does not cover these behaviors.

## Execution evidence and exclusions

The initial prerequisite corpus is pinned by
`internal/testing/conformance/testdata/rippled-3.4.1-v4/manifest.json`. The
generator validates every manifest fixture and records its raw
`submit.engine_result` and `submit.engine_result_code` distributions. These
are C++ fixture observations; they are not Go parity results. The inventory
also carries the manifest's recorder commit, source hashes, configuration
identity, and recorder-binary SHA-256 so this input remains distinguishable
from a named test or a later regenerated corpus. C++ assertion totals and
expanded runtime case results belong in the separate engine execution report.

Go replay is produced separately by `TestConformance` and the mandatory
conformance command, with an optional JSON path supplied through
`GOXRPL_CONFORMANCE_REPORT`. The generator does not run that test, and every
transaction row deliberately keeps `executed_in_checked_in_corpus` and
`oracle_comparison_executed` false. Presence in the corpus or in a named Go
test is not execution evidence. A replay report must enumerate its own
discovered/executed/passed/failed/skipped rows before those flags can change.

The old `scripts/conformance-out-of-scope.txt` list is retained as a **legacy**
runner record for `app/Batch`, `app/Delegate`, and `app/Vault`. Inventory rows
therefore use `legacy_conformance_excluded` and an explicit reason. Those keys
describe the legacy suite only; they do not exclude the v4 Batch and VaultCreate
fixtures, and they do not claim Delegate execution.

The prerequisite corpus records signed open-ledger submit/close behavior under
its manifest profiles. It does not cover historical transaction-queue contents,
load/escalation/retry transitions, RPC/parser rejection boundaries, every Batch
wrapper, every lending operation, or all transaction families. The inventory
also does not individually prove every amendment branch or transactor handler.
Those gaps are the bounded next cases for execution evidence.

Run the source drift test with:

```sh
GOCACHE=/private/tmp/issue-2016-go-cache \
  go test ./internal/testing/conformance \
  -run '^TestEngineCoverageInventory$' -count=1
```

`TestEngineCoverageInventory` decodes both oracle and Go field rows and compares
them directly, reconciles all runtime transaction and amendment names, checks
conditional support against the known `mptcrypto.Available()` condition, checks
all 20 stage-mapping rows, and rejects duplicate/missing registry rows. It does
not trust a stored field-match boolean or infer parity from test names.

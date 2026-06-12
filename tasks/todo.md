# Issue #887 — invariant checkers, SignerList ordering, AccountSet legacy flags, parser consolidation

Branch: fix/issue-887-invariants-system-divergences (rebased onto origin/main @ 7e0d7001)

## Audit vs issue (issue was written against an older tree)

Already fixed on main before this branch (no work needed):
- [x] #1 SignerListSet byte-order sort (signer_list_set.go uses bytes.Compare)
- [x] #5 NoBadOffers negative-not-zero semantics + IOU sign parsing (offers.go)
- [x] #6 skipFieldBytes type-8 VL prefix
- [x] #8 Batch per-inner-tx invariant passes (CheckInnerInvariants, batch.go); carve-outs removed
- [x] TransfersNotFrozen LowLimit==HighLimit skip (no longer present)

## Done in this branch

### Phase A — consensus-critical
- [x] AccountSet.Apply: legacy tx flags OR'd with asf (RequireDestTag set/clear, DisallowXRP set/clear, RequireAuth clear) per SetAccount.cpp doApply
- [x] AccountSet.Validate: reject SetFlag==ClearFlag only when non-zero
- [x] NFTokenMinter preflight gating note (comment only; pre-amendment divergence)
- [x] Tests: legacy-flag set/clear (internal/testing/accountset/legacyflags_test.go), SetFlag:0+ClearFlag:0 no-op

### Phase B — parser & type-table unification
- [x] One shared SLE field walker: state.WalkFields/WalkFieldsDeep (internal/ledger/state/field_walker.go) — handles MPT 33-byte amounts, 1/2/3-byte VL, nested objects/arrays; errors on unsupported types instead of silently desyncing
- [x] Entry-type extraction unified: state.EntryTypeCode/EntryTypeName/EntryType; deleted the 3 drifted copies (invariants/binary_helpers.go, apply_state_table.go, ledger/service/helpers.go)
- [x] Tx type codes/names moved to protocol/transaction_type.go; internal/tx.Type is now an alias; invariants' drifted private table deleted → ValidNewAccountRoot XChain attestation cases now reachable (test pins it)
- [x] checkXRPBalances: checks both Before and After images
- [x] checkNoXRPTrustLines: tests LowLimit/HighLimit issues (rippled semantics), after image incl. deletes
- [x] finalizeAMMCreate: exact compare attempted; kept 1e-11 tolerance with documented reason (go-xrpl create-time LP-token math drifts 1 ULP from the sqrt reconstruction; exact compare fails all 51 AMM create steps; see issue #857) + synthetic test pinning the tolerance
- [x] Parse-failure policy unified to hard-fail (incl. LedgerEntryTypesMatch on unextractable type; NFTokenPage verified to carry the 0x11 header, no carve-out needed)

### Phase C — reserve & fee
- [x] SignerListSet/TicketCreate/DelegateSet reserve checks use ctx.PriorBalance(actual fee)/CheckReserveWithFee; regression test proves the escalated-fee boundary (ticket_reserve_fee_test.go)
- [x] DID left on post-fee Balance — rippled DID.cpp addSLE checks (*sleAccount)[sfBalance] (post-fee), NOT mPriorBalance; the issue's premise was wrong here
- [x] LedgerStateFix.CalculateBaseFee reads live FeeSettings increment (AccountDelete pattern)

### Phase D — cleanup
- [x] Deleted: did/did_helpers.go, account/apply_account.go, pseudo FindMajority, pseudo bytesEqual (→ bytes.Equal), hand-rolled parseUint64/parseHex (→ strconv.ParseUint), parseDropsAmount double-parse, invariants nftPageMaskMax, unused bool param, isValidPublicKey wrapper, isLikelyAMMBinary + stale AMM-format comments
- [x] do_apply.go logs invariant violations (name + message) instead of discarding
- [x] AccountDelete.Apply table-driven per-type deleters with uniform tefBAD_LEDGER policy; missing dir child → tefBAD_LEDGER (rippled cleanupOnAccountDelete); deleters also fail on DirRemove not-found (Success=false), and Ticket/SignerList use the SLE's real OwnerNode page hint
- [x] Codec misEncodedTypeAliases removed — binarycodec already resolves LedgerEntryType strings via the ledger-entry map (parseSpecialFields), verified DepositPreauth encodes as 0x0070

### Latent consensus bug found & fixed (exposed by the stricter AccountDelete check)
state.GetOwnerNode matched the FIRST 0x34 byte anywhere in the SLE — a ticket whose
TicketSequence value contained 0x34 (e.g. seq 52) yielded a garbage page hint, DirRemove
silently reported not-found (Success=false, nil error), the stale owner-dir entry survived,
and a later AccountDelete hit it. Fixed: GetOwnerNode parses via WalkFields (regression
tests in field_walker_test.go); consumeTicket/consumeTicketForRecovery now treat a failed
dir removal as tefBAD_LEDGER, mirroring rippled Transactor::ticketDelete.

### Conformance-harness fix required by the stricter ticket consumption
The v2 `bump_last_page` fixtures omit the adjust callback rippled's
test::jtx::directory::bumpLastPage always runs (the recorder only captures
directory+target_page), so the replayed state had moved entries whose *Node page
hints pointed at the erased page — a state rippled never produces, exposed as
tefBAD_LEDGER once consumeTicket checks DirRemove. env_directory.go now rewrites
each moved entry's *Node field(s) that point at the old page (exact field when
the fixture names one), mirroring adjustOwnerNode / the Directory_test
sfIssuerNode callback.

## Review

- go build ./... + go vet ./... clean; gofmt clean; golangci-lint clean
- Conformance: branch fails the IDENTICAL 240 pre-existing subtests as origin/main@7e0d7001 (Vault/XChain out-of-scope stubs + known Batch/AMM/NFToken/Offer/TxQ gaps) — zero regressions, zero deltas, verified via side-by-side worktree runs of the full suite
- Full `go test ./...`: green except the pre-existing conformance gaps above
- Rebased onto origin/main 7e0d7001 (one conflict in account_delete.go: main's entry.LsfSellNFToken rename folded into the new deleteNFTokenOffer)

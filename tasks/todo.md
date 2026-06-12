# Issue #887 — invariant checkers, SignerList ordering, AccountSet legacy flags, parser consolidation

Branch: fix/issue-887-invariants-system-divergences (off origin/main @ ce6ba761)

## Audit vs issue (issue written against an older tree)

Already fixed on main (no work needed):
- [x] #1 SignerListSet byte-order sort (signer_list_set.go:383-387 uses bytes.Compare)
- [x] #5 NoBadOffers negative-not-zero semantics + IOU sign parsing (offers.go)
- [x] #6 skipFieldBytes type-8 VL prefix (binary_helpers.go:43-56)
- [x] #8 Batch per-inner-tx invariant passes (CheckInnerInvariants, batch.go:718); carve-outs removed from basic.go
- [x] TransfersNotFrozen LowLimit==HighLimit skip (no longer present in frozen.go)

## Remaining work

### Wave 1A — tx/account (agent A)
- [ ] AccountSet.Apply: legacy tx flags OR'd with asf (RequireDestTag set/clear, DisallowXRP set/clear, RequireAuth clear) per SetAccount.cpp:326-340
- [ ] AccountSet.Validate: reject SetFlag==ClearFlag only when non-zero (account_set.go:164)
- [ ] Note on NFTokenMinter preflight amendment gating (LOW, comment only)
- [ ] Remove isValidPublicKey wrapper; delete empty apply_account.go
- [ ] Table-drive AccountDelete.Apply per-type deletion blocks, uniform error policy
- [ ] Tests: legacy-flag set/clear, SetFlag:0+ClearFlag:0 no-op

### Wave 1B — reserve & fee + pseudo/did cleanup (agent B)
- [ ] CheckReserveWithFee/PriorBalance in signer_list_set.go:360, ticket_create.go:100, delegate_set.go:205, did_set.go:163
- [ ] LedgerStateFix.CalculateBaseFee reads FeeSettings from view (like account_delete.go:58-68)
- [ ] pseudo: bytes.Equal, strconv.ParseUint, collapse parseDropsAmount double-parse, delete FindMajority
- [ ] Delete dead did/did_helpers.go

### Wave 1C — invariants unification (agent C)
- [ ] Dedup getLedgerEntryType/ledgerEntryTypeName (invariants/binary_helpers.go, apply_state_table.go:995/1115, ledger/service/helpers.go:91) into internal/ledger/state; keep most complete variant (MPT 33-byte amount, 3-byte VL)
- [ ] Shared SLE field walker in internal/ledger/state (on parseFieldHeader); rewrite invariants hand parsers on it
- [ ] Move tx type codes/names to a leaf package; delete invariants.TxType + String() (fixes unreachable XChain cases in basic.go:281)
- [ ] checkXRPBalances: check Before and After images
- [ ] checkNoXRPTrustLines: test LowLimit/HighLimit issue == XRP, not Balance.Currency
- [ ] finalizeAMMCreate: exact compare per InvariantCheck.cpp:1849 (or documented tolerance + conformance test)
- [ ] Unify parse-failure policy to hard-fail (NoZeroEscrow non-escrow paths, NoXRPTrustLines, NoDeepFreeze, ValidNFTokenPage, ValidPermissionedDomain)
- [ ] Dead code: nftPageMaskMax alias, unused bool param validatePermissionedDomainCredentials
- [ ] Refresh stale AMM comments; remove redundant isLikelyAMMBinary
- [ ] do_apply.go:726: log invariant violation instead of `_ = violation`

### Wave 2 — codec (after 1C)
- [ ] Fix UInt16.FromJSON LedgerEntryType-vs-TransactionType preference (DepositPreauth SLE writes tx code 19 instead of 0x0070); remove misEncodedTypeAliases + resolveEntryTypeName fallback

### Verify
- [ ] go vet all 8 packages
- [ ] just test-pkg: invariants, account, signerlist, ledgerstatefix, ticket, delegate, did, pseudo + testing/{accountset,accountdelete,multisign,ticket,did,invariants,batch}
- [ ] just conformance — no regressions (SignerList/AccountSet suites)
- [ ] Full just test

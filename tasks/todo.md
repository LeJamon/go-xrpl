# Issue #1357 — AMMClawback pseudo-account metadata

Target: `origin/main` at `25a82672eb73ec5f76d95e00b8ef54d27cab21b2`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate GitHub access, issue state and discussion, linked PRs, active
      release branches, exact base, and clean dedicated worktree.
- [x] Trace AMMClawback pseudo-account persistence and metadata action tracking
      in Go, including genuine XRP-side balance changes.
- [x] Verify the complete corresponding apply path and regression expectations
      against local rippled v3.2.0.
- [x] Add focused regressions for bare threaded IOU/IOU metadata, byte/root
      parity where fixtures permit, and persisted XRP-side balance changes.
- [x] Implement the smallest production-quality parity fix.
- [x] Run formatting, focused and affected tests, race coverage where useful,
      build, vet, strict CI lint, conformance/diff review, and exact-head checks.
- [x] Record the review results, stage intentional files only, commit, push, and
      open the PR against `main`.

## Review

- Removed the redundant unconditional pseudo AccountRoot write after
  `deleteAMMAccountIfEmpty`; that helper already persists serialized-byte changes,
  including real XRP balance changes and the `tecINCOMPLETE` survivor path.
- Rippled v3.2.0 reads the pseudo AccountRoot but changes it economically only for
  native XRP sends. Deleting a holder's LP RippleState threads the unchanged IOU/
  IOU pseudo-account as a bare `ModifiedNode`, while an XRP payout updates its
  `Balance` and emits normal `FinalFields`.
- The IOU/IOU regression reproduces the whole-holder LP-line deletion with a
  surviving AMM, asserts node-level threading without `FinalFields`, pins the
  complete 2,248-byte metadata blob by SHA-512Half, and pins the resulting
  single-transaction SHAMap root. The XRP contrast proves a 500 XRP pseudo-root
  balance reduction remains persisted and represented in metadata.
- Passed focused pre-fix/fixed regression runs, affected AMM transaction and
  integration packages, focused race coverage, `just test-tx`, `just fmt`,
  `just build-all`, `just vet`, tagged PostgreSQL vet, CI-pinned strict lint,
  advisory lint, `git diff --check`, and AMMClawback conformance (12/12).
- The original Devnet checkpoint and the other 12 transaction leaves from ledger
  3,325,984 are not present locally, so its complete 13-transaction root was not
  rerun. The regression directly pins the exact divergent 2,248-byte metadata
  boundary and its canonical transaction leaf/tree hash.

## PR #1358 finalization

- [x] Pin the exact PR head, base, clean worktree, and clean local rippled 3.2.0
      oracle at `3c43f4614f87965298773279ff5b85d4c56c637b`.
- [x] Review the complete diff and adjacent AMM withdrawal, trust-line cleanup,
      bounded deletion, AccountRoot persistence, and metadata paths.
- [x] Resolve the final-LP `tecINCOMPLETE` divergence found during review by
      deleting drained pool lines before bounded cleanup and preserving the
      latest pseudo-account `OwnerCount` while merging XRP balance changes.
- [x] Add a final-share AMMClawback regression with 513 excess LP trust lines.
- [x] Restore pseudo-account `OwnerCount` when an empty AMM recreates a drained
      IOU pool line, and extend the regression through re-seeding and deletion.
- [ ] Repeat independent correctness and rippled-conformance review; pass build,
      vet, lint, whitespace, and exact-head CI gates.
- [ ] Run the required separate comment-cleanup phase, verify final CI, and
      record the local-only conformance audit.

### Finalization review

- Rippled v3.2.0 drains and deletes a zero AMM pool RippleState, including its
  reserve and pseudo-account `OwnerCount`, before applying the 512-entry AMM
  cleanup bound. On `tecINCOMPLETE` it updates only the AMM ledger entry.
- The shared Go debit path now applies the same balance, reserve, owner-count,
  and deletion transition for AMMClawback and AMMWithdraw. AccountRoot balance
  persistence merges into the latest view, and interrupted cleanup no longer
  rewrites an earlier AccountRoot snapshot.
- Re-seeding an interrupted empty AMM now increments `OwnerCount` for each
  recreated IOU pool line, matching rippled's `trustCreate` transition.
- Bounded cleanup now erases the zero line and decrements only the non-AMM
  reserve owner, matching `deleteAMMTrustLine` in the pinned oracle.

# Issue #1349 — LoanSet NUMBER asset association

Target: `origin/main` at `ce13cadd`. Behavioral oracle: clean local rippled
v3.2.0 worktree at `3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Confirm the Loan NUMBER fields carrying `kSmdNeedsAsset` and trace the Go
      association path and all callers.
- [x] Add a focused regression that preserves fractional fee/payment values while
      continuing to associate the three outstanding-value fields.
- [x] Implement the smallest idiomatic Go fix matching rippled v3.2.0 metadata.
- [x] Run formatting, focused and affected tests, race coverage where useful,
      build, vet, lint, and final diff/conformance review.
- [x] Record exact review results, commit only intentional files, push, and open
      a pull request against `main`.

## Review

- `associateLoanAsset` now applies asset precision only to
  `PrincipalOutstanding`, `TotalValueOutstanding`, and
  `ManagementFeeOutstanding`, matching the complete `kSmdNeedsAsset` set for
  Loan entries in rippled v3.2.0. The four fee fields and `PeriodicPayment`
  retain their full NUMBER precision across LoanSet, LoanPay, and LoanManage.
- The focused regression pins the replay's `10.00000001505552512` periodic
  payment, covers all five unassociated Loan fields, and proves the three
  associated fields still round or disappear at their default. It failed on
  the original helper and passes with the fix.
- Passed focused lending transaction and integration tests, focused race tests,
  the full `internal/tx/...` suite, `just fmt`, `just vet`, `just build-all`,
  `just lint` with zero issues, and `git diff --check`.
- Independent final Go-quality and rippled-conformance reviews found no
  Blocking, Minor, or Nit issues. The oracle is the clean local tag `3.2.0` at
  `3c43f4614f87965298773279ff5b85d4c56c637b`.
- The historical devnet checkpoint/debug artifacts referenced by the issue are
  not present in this worktree, so the full ledger replay was not rerun; the
  exact divergent field value and metadata boundary are covered directly.
- Behavior commit `ed568282` is published on
  `fix/issue-1349-loanset-asset-rounding`; PR #1352 targets `main` at
  https://github.com/LeJamon/go-xrpl/pull/1352.

# Issue #1338 — PermissionedDomainDelete owner-directory retention

## Goal

Make `PermissionedDomainDelete` retain an empty page-zero owner-directory root
after deleting its final domain, matching rippled v3.2.0 ledger state and
transaction metadata without changing removal behavior when other entries remain.

## Plan

- [x] Validate GitHub auth, issue state and discussion, linked PRs, current remote
      refs, exact base branch, clean worktree, and local rippled v3.2.0 provenance.
- [x] Read the complete Go delete path, directory-removal implementation, metadata
      stamping path, callers, and existing permissioned-domain tests.
- [x] Read the complete rippled v3.2.0 delete implementation, directory-removal
      semantics, and every relevant PermissionedDomain test case.
- [x] Add focused regressions proving final-entry deletion retains an empty root
      with matching previous-transaction fields and non-final deletion removes
      only the target index.
- [x] Implement the smallest production-quality parity fix and inspect all
      `DirRemove` call sites for accidental behavior expansion.
- [x] Run formatting, focused and affected-package tests, build, vet, strict CI
      lint, diff checks, and a final Go-quality/rippled-conformance review.
- [x] Stage intentional files only, commit, push, open the PR against `main`, and
      verify the published branch, PR head, mergeability, and CI state.

## Review

- `PermissionedDomainDelete.Apply` now uses `tx.DirRemoveOrBadLedger`, matching
  rippled v3.2.0's `keepRoot=true` and `tefBAD_LEDGER` behavior when the recorded
  directory page or item is missing.
- Focused regressions prove the sole-domain root remains a threaded
  `ModifiedNode` with empty on-ledger `Indexes`, deleting one of two domains
  preserves the sibling, and a corrupt missing directory rolls back the delete.
- Oracle coverage maps the Go apply call and shared helper to rippled
  `PermissionedDomainDelete.cpp:55-63` and `ApplyView.cpp:255-392`; the clean
  oracle is tag `3.2.0` at `3c43f4614f87965298773279ff5b85d4c56c637b`.
- Affected packages, race tests, `just test-tx`, PermissionedDomains conformance
  (6/6), build, vet, tagged PostgreSQL vet, strict CI lint v2.11.3, advisory
  lint, formatting, and diff checks pass.
- The historical ledger 3,300,730 checkpoint/debug artifacts referenced by the
  issue are not present in the workspace, so the exact full-ledger root replay
  was not rerun locally; the focused regression directly pins the divergent SLE
  bytes' semantic fields and metadata classification.
- Behavior commit `3821b3ef` is pushed on
  `fix/issue-1338-keep-owner-directory`; PR #1346 targets `main` at
  https://github.com/LeJamon/go-xrpl/pull/1346.

# Issue #1334 — historical retired-amendment replay compatibility

Target: `origin/main` at `f0db8a22e1386116d08eb7efb21889997bd97af4`.
Behavioral oracle: clean local rippled v3.2.0 worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate the open issue, absence of linked PRs, repository authentication,
      active release branches, clean issue worktree, and exact merge base.
- [x] Confirm rippled v3.2.0 unconditionally inserts PaymentChannelCreate into
      the recipient owner directory and serializes `DestinationNode`.
- [x] Add an explicit replay-only historical gate for
      `fixPayChanRecipientOwnerDir`, with current v3.2.0 behavior as the default.
- [x] Thread the compatibility choice only through `replay-range`; leave every
      live, consensus, standalone, and default replay apply on v3.2.0 semantics.
- [x] Add focused command/config and PaymentChannelCreate regressions covering
      default v3.2.0 behavior and the opted-in historical behavior.
- [x] Run formatting, focused and affected tests, race coverage where useful,
      build, vet, strict lint, and final diff/merge-base review.
- [x] Record review results and prepare only the intentional files for commit
      and a pull request against `main`.

## Review

- `replay-range --legacy-paychan-owner-dir-gate` derives the historical
  `fixPayChanRecipientOwnerDir` state from each parent ledger's raw Amendments
  SLE, so a replay crossing activation switches behavior at the correct ledger.
- The default path remains rippled v3.2.0: PaymentChannelCreate always inserts
  the recipient owner-directory backlink and serializes `DestinationNode`.
- The replay-only engine compatibility bit omits that backlink only while the
  opted-in historical gate is disabled; production Rules and ledger loaders are
  unchanged.
- Focused replay, ledger, PayChannel transaction, and PayChannel integration
  suites pass, as do the full transaction tree and focused race suites.
- `just build-all`, `just vet`, CI-pinned golangci-lint v2.11.3 with both strict
  and advisory configs, formatting, and `git diff --check` pass.

# Issue #1337 — LoanSet payment-count rounding

## Goal

- Accept valid LoanSet schedules whose upward-rounded payment quotient remains
  fractionally below the integer payment count.
- Match rippled v3.2.0's ambient upward rounding through the final integer
  conversion.
- Cover the replay values from issue #1337 and audit equivalent guarded
  conversions for the same error pattern.

## Plan

- [x] Validate the issue, linked work, clean worktree, exact base, and local oracle.
- [x] Trace LoanSet payment guards and rippled v3.2.0 Number rounding semantics.
- [x] Add a focused failing regression and implement the minimal parity fix.
- [x] Audit other ports of NumberRoundModeGuard expressions with final conversions.
- [x] Run formatting, focused and broader tests, vet, lint, build, and diff review.
- [x] Record review results, commit intentional files, push, and open the PR.
- [x] Separate outer Batch application from committed inner application so each
      transaction has rippled-compatible state metadata.
- [x] Persist exactly the committed inner transaction leaves atomically with the
      outer transaction and advance transaction indexes for every leaf.
- [x] Make replay consume Batch inner leaves without applying their state twice.
- [x] Split outer-only open-ledger/TxQ application from closed-ledger Batch
      construction, preserving one open transaction and committed inner leaves
      only during ledger build/replay.
- [x] Recover inner preclaim failures per transaction and retain the
      LendingProtocol pseudo-account authorization guard used by rippled 3.2.0.
- [x] Prove Batch build failure is atomic after outer staging and before an
      inner leaf is published.
- [x] Preserve parsed Batch inner transactions and their canonical wire bytes,
      including required empty `SigningPubKey` and ticket `Sequence: 0` fields,
      through hashing and leaves.
- [x] Apply the same required zero-valued fields to single-, multi-, and
      counterparty-signing preimages.
- [x] Re-audit the complete behavior diff against the exact rippled v3.2.0 oracle,
      then run only the finalization skill's build, vet, and lint gates.

## Review

- Rippled v3.2.0 keeps `Number::RoundingMode::Upward` active through both
  Guard 4's division and its `std::int64_t` conversion. Go now applies upward
  rounding to both operations, so the recorded 11.999... quotient computes 12
  payments and returns `tesSUCCESS` across all Number mantissa scales.
- The ambient-rounding audit found and fixed the same mismatch in LoanPay fee
  estimation: overpayments now retain upward rounding through the payment-count
  conversion. LoanPay fee increments now also preserve the normal multisign fee
  multiplier used by rippled.
- Production-path review found and fixed two existing LoanSet divergences: the
  minimum fee now includes counterparty signers, and new Loans are linked to the
  LoanBroker pseudo-account directory used by the deletion path.
- Counterparty signatures now have one typed JSON/wire representation, so fee
  calculation and signature verification use the same parsed object. Direct
  apply, Batch inner transactions, fee autofill, and TxQ all share the same
  per-transaction fee dispatch.
- TxQ now keeps contextual, ordinary, and ledger-reference fees distinct. This
  preserves SetRegularKey's zero-fee waiver and nonzero-paid priority, custom
  transaction normalization, reserve checks, and closed-ledger fee metrics.
  Closed-ledger dispatch carries the full fee schedule and parent close time.
- Inner Batch SetRegularKey transactions cannot receive the top-level password
  change waiver. Batch resynchronization now preserves the complete outer
  AccountRoot after inner mutations while still allowing an inner AccountDelete
  to erase it.
- All three recorded devnet recurrences exercise the payment-count guard across
  every Number mantissa scale. The XRP recurrence also submits a real LoanSet
  and verifies the resulting Loan, Vault, LoanBroker, and borrower state. The
  primary IOU vector reproduces its exact periodic payment, total value, and loan
  scale from the LoanSet inputs. The cited replay artifacts and parent ledger
  state are not present locally, so the ledger/account/transaction roots could
  not be rerun.
- Finalization now separates rippled's low-level outer-only open/TxQ apply from
  its closed-ledger `applyTransaction` Batch wrapper. Committed inners re-enter
  the full engine, receive consecutive indexes and `ParentBatchID` metadata,
  and are staged with the outer state and transaction leaves atomically.
- All-or-nothing Batch state commits without rethreading already-threaded inner
  changes. Per-inner preclaim panics become `tefEXCEPTION`, and inner pseudo-
  account authorization retains the LendingProtocol `tefBAD_AUTH` guard.
- Inbound replay validates the expected inner hash, index, parent Batch ID, and
  result before installing the peer leaf without applying state twice. Open-
  ledger replay/relay enumeration filters inner leaves; closed fee metrics keep
  all committed leaves.
- TestEnv TxQ admission counts only the outer Batch. A close containing a Batch
  rebuilds the ledger before fee metrics, so committed inners affect state and
  the closed count while the next open ledger starts with the correct outer-only
  queue count. Snapshot staging covers inner-leaf serialization failures.
- Binary Batch ingress now reconstructs each nested transaction through the
  normal template-aware parser and retains its canonical bytes. Programmatic
  transactions also serialize required empty `SigningPubKey` and ticket
  `Sequence: 0` fields, so transaction IDs and leaves match rippled rather than
  a self-consistent reduced blob. Every signing and verification path uses the
  same populated map, so ticket signatures cover the canonical zero Sequence.
- Passed `just fmt`, `go test ./internal/tx/lending/... -count=1`,
  `go test ./internal/testing/lending -count=1`, `just test-tx`, `just vet`,
  CI-pinned strict and advisory lint, the tag-gated PostgreSQL vet check,
  `just build-all`, and `git diff --check`.

# Issue #1336 — Escrow replay directory defaults

Target: `origin/main` at `f0db8a22e1386116d08eb7efb21889997bd97af4`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate GitHub state, duplicate PRs, the post-v3.0.0 `main` base, and a
      clean dedicated worktree.
- [x] Trace created-Escrow default restoration and directory placement in Go,
      then verify `DestinationNode` and `IssuerNode` semantics against rippled
      v3.2.0.
- [x] Add focused regressions for cross-account page zero, self-escrow, and an
      explicit nonzero destination page, plus any oracle-confirmed IOU issuer
      default case.
- [x] Implement the minimal deterministic default inference before directory
      placement without extending the inference to PayChannels.
- [x] Run formatting, focused and race tests, vet, strict lint, build, and a
      final correctness/conformance diff review.
- [x] Record review results, commit intentional files, push the branch, open the
      PR, and verify its exact remote head and checks.

## Review

- Created Escrows now recover a missing zero `DestinationNode` only when sender
  and destination differ, and a missing zero `IssuerNode` only for a classic
  IOU whose issuer differs from both. Explicit nonzero pages are preserved;
  XRP, MPT, self, and issuer-equals-destination cases retain rippled's field
  presence semantics. PayChannel inference remains unchanged.
- The restored fields feed the existing directory-placement pass, rebuilding
  page-zero destination and issuer membership as well as the canonical Escrow
  bytes omitted by CreatedNode metadata.
- Regression coverage includes cross-account XRP page zero, XRP and IOU self
  escrow, explicit nonzero destination/issuer pages, third-party IOU issuer page
  zero, issuer-as-destination, and MPT exclusion.
- Independent Go and conformance reviews are clean against local rippled 3.2.0
  at `3c43f4614f87965298773279ff5b85d4c56c637b`.
- Passed `go test ./internal/replaytool -count=1`, focused race tests, `just vet`,
  CI-pinned strict and advisory lint with zero issues, `just build`, formatting,
  and `git diff --check`.
# Issue #1331 — VaultCreate Scale template allowlist

## Goal

- Accept codec-valid `VaultCreate` transactions that carry the optional `Scale`
  field, matching rippled v3.2.0.
- Preserve the existing IOU-only and maximum-scale validation in the VaultCreate
  transactor.
- Cover the binary parse/prepare path that rejected the devnet replay transaction
  before application.

## Plan

- [x] Validate the issue, linked work, clean worktree, exact base, and local oracle.
- [x] Trace the template allowlist, binary parser, and existing VaultCreate validation.
- [x] Add `Scale` as an optional VaultCreate template field and a focused binary regression.
- [x] Run formatting, focused tests, broader transaction tests, vet, lint, build, and diff checks.
- [x] Review the completed change against rippled v3.2.0 and record the results below.

## Review

- Rippled v3.2.0 declares `VaultCreate.sfScale` as `SoeOptional`; its separate
  preflight permits values 0 through 18 only for IOU assets. The existing Go
  transactor already matches those validation and application semantics.
- The missing optional template entry fixes replay parsing. Finalization also
  changed the template registry to retain rippled's declaration order for
  `server_definitions` while deriving lookup maps for admission checks, and
  closed the same stale-template gap for the already-implemented DynamicMPT
  mutation fields.
- Regressions cover binary `ParseAndPrepare`, the ordered VaultCreate format,
  the complete Scale validation matrix, stored values, and byte-exact blob
  preservation.
- Focused and full transaction/Vault tests pass, including the focused race
  test. `just fmt`, `just build-all`, `just vet`, required CI lint, advisory
  lint, and `git diff --check` pass.
- The original devnet replay database is not available in this workspace, so
  the ledger-range replay was not rerun; the reported parse failure is covered
  directly by the serialized transaction regression.

## PR #1335 finalization

- [x] Pin the PR head, merge base, clean worktree, and exact local oracle.
- [x] Review the complete diff for Go correctness and rippled v3.2.0 parity.
- [x] Preserve canonical transaction-format order and close adjacent registry gaps.
- [x] Add exact format, Scale branch, persistence, and serialized admission coverage.
- [x] Pass `just build`, `just vet`, `just lint`, and `git diff --check`.
- [ ] Commit and push the behavior remediation; require exact-head green CI.
- [ ] Run the separate comment-cleanup phase and verify the final CI head.

# Issue #1322 — nodestore fallback for ledger hash lookup

## Goal

Make `Service.GetLedgerByHash` recover persisted ledgers after in-memory history
eviction while preserving fast cache hits, no-nodestore behavior, and true
not-found semantics.

## Plan

- [x] Validate issue state, linked work, intended `v3.0.0` base, and clean worktree.
- [x] Trace the persisted header/SHAMap format and define failure/cache semantics.
- [x] Add focused failing tests for persisted eviction, cache restoration, absent data, and no nodestore.
- [x] Implement the minimal production-quality load and reconstruction path.
- [x] Run formatting, focused/race tests, build, vet, strict lint, and final diff review.
- [x] Commit intentional files, push the branch, open the PR, and verify its remote head.

## Review

- Durable recovery requires the exact validated relational record before loading
  the matching nodestore header and SHAMap roots, matching rippled v3.2.0.
- The restored ledger stays request-local while queries run, retains its exact
  hash selector across JSON-RPC and gRPC, and does not replace canonical
  sequence history.
- Focused coverage includes validated restoration, rejection of nodestore-only
  closed forks, reconstructed fees, missing/corrupt storage, query funnels,
  exact-hash routing, cache isolation, and cache bounds.
- Finalization gates `just build`, `just vet`, and `just lint` pass; test and race
  coverage is delegated to the exact-head GitHub Actions workflow.
- The final conformance review uses clean local rippled tag `3.2.0` at peeled
  commit `3c43f4614f87965298773279ff5b85d4c56c637b` as the behavioral oracle.

## PR #1328 finalization

- [x] Pin the PR head, merge base, clean worktree, and exact local oracle.
- [x] Review the complete diff for Go correctness and rippled v3.2.0 parity.
- [x] Correct relational gating, closed-ledger reconstruction, canonical-chain
      validation, configured fee fallback, exact-hash routing, and storage errors.
- [x] Propagate request contexts through lazy state and transaction SHAMap reads.
- [x] Add historical-chain, cancellation, current-snapshot, fee, and error coverage.
- [x] Pass `just build`, `just vet`, `just lint`, and `git diff --check`.
- [x] Reconcile the advanced `v3.0.0` base and reverify the combined behavior tree.
- [ ] Commit and push the behavior remediation; require exact-head green CI.
- [ ] Run the separate AI-comment cleanup phase and verify the final CI head.

The finalization review uses only the clean local rippled `3.2.0` checkout at
`3c43f4614f87965298773279ff5b85d4c56c637b`. Local tests are intentionally
deferred to GitHub Actions by the finalization workflow; build, vet, strict lint,
formatting, and whitespace gates pass on the reviewed behavior tree.

The first exact-head CI run exposed an unused private SHAMap wrapper and stale
core fixtures after the base merge. The wrapper is removed; missing-ledger mocks
now use the typed service sentinel, open-ledger selector tests assert immutable
snapshots, persisted-ledger fixtures establish canonical validation, and the fee
and timestamp fixtures match rippled v3.2.0 amendment and Ripple-epoch rules.
`just build`, `just vet`, `just lint`, and `git diff --check` pass on the focused
CI-remediation tree; local tests remain intentionally delegated to CI.

# Issue #1161 — full-rippled realignment of the keep-up/self-heal bundle

User decisions (2026-07-01): full rippled on the peer-LCL gate, the watchdog,
and catch-up; sig-cache stays ingress-only (my call); strip all issue-keepup
instrumentation, keep the fatal-path goroutine dump relabeled (my call).

## A. Peer-LCL gate → rippled checkLastClosedLedger semantics
- [x] getNetworkLedger: remove the GetTrustedSupport==0 peer-vote drop and the
      quorumPresent diagnostic machinery
- [x] checkLedger: remove the netSupport>ourSupport switch gate; safety moves
      to acquire-then-verify at the switch site (canSwitchToLedgerLocked =
      canBeCurrent + areCompatible, NetworkOPs.cpp:1948-1962)
- [x] Verify wired into handleWrongLedger AND OnLedger adoption walk
- [x] adaptor.preferredLCL: rippled getPreferredLCL structure (trie-preferred
      w/ stay-switch rules incl. lower-seq different-chain via ancestorOf;
      PreferredFromValidations no longer shadows the peer fallback);
      largestIssued = lastIssuedValidationSeq tracked in BroadcastValidation

## B. Watchdog + expireRound → rippled LoadManager/Consensus semantics
- [x] Removed close-driven fatal "ledger" loop + Service.SetStallPing plumbing;
      kept tick-driven "consensus" loop-liveness heartbeat + fatal abort
- [x] Expired past dwell: leaveConsensusLocked bow-out, accept ONLY behind the
      close-time gate (ResultAbandoned); no CT consensus → wait for checkLedger
- [x] Watchdog first-warn STW dump removed (abort-only), banner relabeled
- [x] Tests reworked: Expired_NoCT_WaitsForResync / Expired_WithCT_Accepts

## C. Catch-up → rippled LedgerMaster::doAdvance semantics
- [x] Timer-driven re-arm in maintenanceTick (+ tests)
- [x] All three cap-bypassing arming sites now honour maxConcurrentCatchup
- [x] History backfill: ReasonHistory serial backward walk after jump-adopt,
      tick-armed, store-only ingest, fixMismatch below-tip guard (+ test)
- [x] Request widths: collect 256 pre-dedup, cap 128 reply / 12 timeout

## D. Robustness
- [x] ErrNodeNotInStore sentinel; strict completeness walk for
      FinishSync/IsComplete (no phantom missing, no false complete);
      lenient request path unchanged (rippled collapse) (+ tests)
- [x] OnLedger-during-build: next-tick checkLedger re-derives via ungated
      votes + locally-held target — no re-delivery needed

## E. Strip issue-keepup instrumentation
- [x] All 8 sites stripped incl. entangled fields/consts/counters + test asserts
- [x] Watchdog dump kept on fatal path, relabeled
- [x] context.TODO(): left as-is — pre-existing repo convention (#185 note)

## F. Verify
- [x] build ./... clean; race clean on rcl/adaptor/inbound/shamap/watchdog/sigcache
- [x] Conformance: in-scope 1260/1260 (100%), fails = known out-of-scope suites
- [x] Primary lint (.golangci.yml): 0 issues on all touched packages
- [ ] Full go test ./... (running)
- [ ] Split into reviewable commits; push to origin/fix/issue-1161-selfheal-finalize
- [ ] Soak: xrpl-confluence 3r2g, 15k governor, ≥10 min lockstep

## G. Soak-driven round 2 (post-review, all pushed)
- [x] Island fix v1 (fcade701): validations-first getNetworkLedger
- [x] Island fix v2 (507f9e67): trie-inconclusive on unplaced majority;
      direct acquire of behind-closed trusted tips; OnLedger different-chain
      rewind; + all 4 review findings (monotonic validated, hook gating,
      unconditional prune, verify-before-wipe, backfill floor)
- [x] Prewarm acquired tx-set signatures (2dd0b6a8)
- [x] Soak iter3: self-heal proven end-to-end (island detected → chased →
      rejoined → full validations → validated 64→92)
- [ ] Soak iter4 (prewarm): verify stall cadence improves

## Review
Verification per commit: full tests, race (rcl/adaptor/service/inbound/shamap/
watchdog/sigcache), primary lint 0, in-scope conformance 1260/1260.
Adversarial diff review (3 lenses + refuters): 1 confirmed bug (validated
rewind) fixed; 2 refuted-but-real hardenings applied; iter27-trap concern
resolved by validations-first precedence.
Remaining known gap: 15k sustained smoothness is paced by build latency on
single-host soaks; prewarm (2dd0b6a8) is the current lever, measured by iter4.

# Issue #1280 — FlagsMasker completion audit

- [x] Inventory all 75 registered transaction types
- [x] Compare all 23 rippled 3.2.0 `getFlagsMask` overrides and the base mask
- [x] Check for invalid-bit masks left solely in `Validate`
- [x] Fix the residual MPTokensV2-dependent Payment mask
- [x] Pin exact masks and bad-fee precedence
- [x] Run transaction tests, vet, and build

## Finalization fixes

- [x] Match the mask predicate to the MPT Amount, not the legacy issuance field
- [x] Preserve rippled's ordered MPTokensV1/V2 Payment preflight semantics
- [x] Route MPTokensV2 Payments through the MPT-capable flow engine
- [x] Port valid-fee flag, path, SendMax, disabled-gate, and routing regressions
- [x] Remove the legacy standalone Payment issuance field and builder plumbing
- [x] Reject malformed MPT JSON, noAccount paths, and invalid MPT offer images
- [x] Preserve internal storage errors through MPT authorization and funding paths
- [x] Run focused tests, full transaction/integration coverage, build, vet, lint,
      and final rippled 3.2.0 review
- [x] Resolve the uncached CI goimports alignment failure and re-run lint
- [x] Fix the terminal resource-manager Start/Stop race exposed by core CI
- [x] Run focused race tests, strict lint, and the core package suite

## Review

Finalized PR #1308 against local rippled v3.2.0. Payment now uses the MPT Amount
as the authoritative asset identity, preserves rippled's V1/V2 preflight order
and TER precedence, and routes MPTokensV2 through Flow while retaining the V1
direct-transfer path. The standalone Go-only issuance field was removed.

The final review also fixed MPT wire/path serialization, cross-asset and
MPT-to-MPT offer crossing, destination checks, noAccount rejection, MPT offer
invariant parsing, pseudo-account authorization gating, canonical integral MPT
JSON parsing, and propagation of ledger storage/parse errors through shared MPT,
Offer, Check, AMM, and FlowCross code.

Verification:

- Focused state, payment, engine, invariant, mptutil, offer, check, AMM, MPT,
  deposit-preauth, and delegate suites pass uncached.
- `just build-all`, `just build-nocgo`, `just vet`, and `just lint` pass.
- The CI-only goimports finding in `strand.go` was corrected and reproduced
  locally with the formatter check before repushing.
- The core CI race exposed a pre-existing terminal lifecycle gap in the resource
  manager. Start/Stop are now serialized against late startup, and the exact
  core `-race` shard plus repeated manager/Components lifecycle tests pass.
- The full module test run passes outside the established out-of-scope
  conformance failures.
- Final conformance: 941 pass / 117 fail overall, 879 pass / 0 fail in scope;
  the 117 failures remain the out-of-scope Batch, Vault, XChain, and XChainSim
  suites.

# Issue #1287 — MPTokensV2 amendment-on engine support

## Plan

- [x] Map PR #1286's nine roadmap points to the v3 Go implementation and the
      rippled 3.2.0 tag, including all reachable call sites and parity tests.
- [x] Extend payment primitives (`EitherAmount`, `Issue`, `Book`, path nodes)
      with a coherent MPT arm and add reusable sandbox-aware MPT credit/funding.
- [x] Implement and wire `MPTEndpointStep`, including strand construction,
      reverse/forward liquidity, transfer fees, authorization, locks, and clawback.
- [x] Implement MPT order books end to end: book keys/directories, offer funding,
      crossing, transfer, rate/auth checks, and offer placement fields.
- [x] Implement amendment-on MPT apply behavior for AMM and Check transactions,
      including `lsfMPTAMM` ledger marking.
- [x] Thread `mpt_issuance_id` through book/path RPC request and response surfaces.
- [x] Port focused rippled parity cases and add Go unit/integration coverage for
      the new type arms, endpoint, book, AMM, Check, and RPC behavior.
- [x] Run formatting, focused tests incrementally, relevant integration suites,
      `just vet`, and `just build`; inspect the final diff for wiring and parity.

## Review

Implemented the coordinated MPT amount, sandbox, endpoint, book, offer, AMM,
Check, and RPC path. The protocol-bearing diff was checked against the local
rippled 3.2.0 implementations and their MPT, AMM-MPT, and Check-MPT test cases.
The rippled C++ tests were used as the parity oracle but were not executed.

Review findings fixed before handoff include zero-issuer asset rejection,
MPT endpoint check ordering, exact transfer-fee rounding and overflow handling,
owner-directory type discrimination, and preservation of issuer-agnostic IOU
rippling while MPT path matching remains issuance-specific.

Verification:

- Focused state, ledger service, payment, pathfinder, offer, AMM, Check, vault,
  RPC, and integration tests pass.
- `just vet`, `just build`, and `just lint` pass (`golangci-lint`: 0 issues).
- Feature and clean-base `just conformance --failing` results are identical:
  941 pass / 117 fail overall, 879 pass / 0 fail in scope. The 117 failures are
  the existing out-of-scope Batch, Vault, XChain, and XChainSim suites.
- The full module test run passes outside those same out-of-scope conformance
  failures.

## PR #1309 conformance remediation — rippled v3.2.0

- [x] Make Payment flags, preflight, preclaim, and Apply select the legacy MPT
      path only before MPTokensV2; route amendment-on MPT payments through Flow.
- [x] Preserve mpt_issuance_id in Payment path validation and serialization.
- [x] Make pathfinder index AMM liquidity in both directions, preserve hidden
      MPT assets on internal account nodes, reject maxed holdings/bad assets,
      and pass ledger timing plus all relevant amendments to every calculation.
- [x] Replace directional MPT transfer-rate rounding and partially funded
      book_offers conversion with exact nearest-even arithmetic.
- [x] Preserve permissioned-book domains and deterministic ordering in
      book_changes; align book_offers proof presence semantics.
- [x] Implement rippled's persistent path_find create/update/status/close state
      machine and canonical response amounts.
- [x] Treat an absent IOU issuer as not globally frozen in CheckCreate; make
      BookBase quality-zero and MPT ID parsing exact.
- [x] Port focused regression cases from rippled v3.2.0 for every finding and
      re-audit all changed production call sites.
- [x] Run formatting, focused/full tests, relevant conformance, vet, lint, and
      build; record exact results below.
- [x] Commit and push the completed remediation to the PR branch.

### Remediation review

- Reviewed against the exact local rippled v3.2.0 snapshot at
  `3c43f4614f87965298773279ff5b85d4c56c637b`. Both independent final audits are
  clean with no remaining blocking, minor, or nit findings.
- Resolved all 16 findings in the original conformance review plus adjacent
  audit findings in simulate validation, cumulative path ranking, exact
  full-liquidity retry selection, large-scale RPC Number precision, stale
  persistent-path updates, replacement ordering, and close latency.
- `just fmt`, focused Payment/engine/MPT/Check/Offer/keylet/ledger/RPC tests, and
  `go test -race ./internal/rpc/...` pass.
- `just vet`, `just lint` (0 issues), `just build-all`, and `just build-nocgo`
  pass.
- `just test` passes every package except the documented out-of-scope
  conformance suites. `just conformance --failing` reports 941 pass / 117 fail
  overall, with 879 pass / 0 fail in scope; the 117 failures remain confined to
  Batch, Vault, XChain, and XChainSim.

## PR #1309 base conflict resolution

- [x] Fetch the current `v3.0.0` base and inventory every conflicting hunk.
- [x] Resolve conflicts without regressing the rippled v3.2.0 behavior fixes.
- [x] Review the merged diff and run CI-pinned lint, vet, and build verification.
- [x] Commit and push the merge resolution; confirm GitHub reports the PR
      mergeable.

### Conflict-resolution review

- Merged the current `v3.0.0` base at
  `e4017e7e49e4e4d111e52343050d347ba0b85ddf`; the base and remote head were
  re-fetched and unchanged immediately before commit.
- Resolved the five conflicts in Payment preflight/apply code, the engine
  dispatch seam, transaction interfaces, and MPTokensV2 regression coverage.
  The combined result uses one `RulesAwarePreflighter` path for standalone,
  Batch-inner, and simulate validation; MPTokensV1 stays on direct transfer and
  MPTokensV2 routes through Flow.
- Rechecked the conflict decisions against rippled v3.2.0 `Payment.cpp` and
  retained both the base branch's broader MPT safety coverage and PR #1309's
  unique destination/Flow-routing regressions.
- `go test ./internal/tx/payment/...` and `go test ./internal/tx/engine/...`
  pass on the resolved tree.
- `just build`, `just vet`, and `just lint` pass; lint reports 0 issues.
- The staged merge has no unmerged paths, conflict markers, or whitespace
  errors.

# Issue #1304 — DRY D1+D2 consolidation

Target: `v3.0.0`. Protocol oracle: local rippled tag `3.2.0`
(`3c43f4614f87965298773279ff5b85d4c56c637b`). The user approved one large PR.

## Plan

- [x] Inventory every D1/D2 duplication named in the issue and record existing
      behavioral differences before selecting shared abstractions.
- [x] Consolidate RPC error construction, handler metadata, account-query error
      mapping, websocket envelopes, and book-offers pagination without changing
      response JSON.
- [x] Add canonical protocol time and Hash256 helpers; remove duplicated
      consensus parameters, frame encoding, ledger-sync tracking, fee defaults,
      dead relational retry settings, and state-diff computation.
- [x] Golden-test ledger-header bytes, then route every serializer through
      `header.AddRaw`.
- [x] Consolidate ledger selection across RPC and gRPC while preserving each
      endpoint's validated/current/error semantics and pagination behavior.
- [x] Consolidate transaction rendering/decoding/sign-submit seams while
      preserving `close_time_iso`, CTID, and response-field compatibility.
- [x] Run formatting and focused tests after each subsystem; compare all
      protocol-bearing behavior with rippled `3.2.0`.
- [x] Run `just build-all`, `just build-nocgo`, `just vet`, `just lint`, full
      tests, and conformance; review the complete diff for correctness and scope.
- [x] Commit only intentional files, push `feat/issue-1304-dry-sweep`, and open
      one PR with base `v3.0.0`.

## Review

- Implemented the complete D1+D2 sweep and integrated the current
  `origin/v3.0.0` at `26d0211ed971c98fe919cb28ed5fed90dd962b67`.
- Verified protocol behavior against the clean local rippled `3.2.0` checkout
  at `3c43f4614f87965298773279ff5b85d4c56c637b`, including ledger lookup and
  freshness, RPC error precedence and response shape, transaction ranges and
  CTIDs, account pagination, ledger-header serialization, message framing,
  consensus parameters, and validation-quorum rechecks.
- `just fmt`, `git diff --check`, `just build-all`, `just build-nocgo`, and
  `just vet` pass. Both the advisory `just lint` recipe and the required CI
  command (`golangci-lint` v2.11.3 with the default strict configuration) report
  0 issues. Go and linter caches were redirected to `/private/tmp` because the
  sandbox cannot write the user cache directories.
- Focused tests pass for all touched core and RPC packages. The race detector
  passes for RPC, ledger service/selector, consensus validation/adaptor, gRPC,
  and node integration.
- `just test` passes every package except `internal/testing/conformance`, whose
  known out-of-scope Batch/Vault/XChain failures make the aggregate command
  return non-zero. `just conformance --failing` reports 941 pass / 117 fail
  overall, with all 879 in-scope tests passing and all 117 failures confined to
  the existing out-of-scope list.
- Integrated `origin/v3.0.0` at
  `26d0211ed971c98fe919cb28ed5fed90dd962b67`, retaining both the base branch's
  durable initial-sync behavior and this PR's validation freshness behavior.
  No conflict markers, whitespace errors, or unrelated worktree changes are
  present.

## PR #1321 finalization

- [x] Pin the PR head, current `v3.0.0` base, feature worktree, and clean local
      rippled `3.2.0` oracle.
- [x] Resolve and push the base-branch conflicts without discarding either
      branch's intended behavior.
- [x] Review the complete merged diff for Go correctness and rippled `3.2.0`
      behavioral parity.
- [x] Correct confirmed transaction lookup, RPC/gRPC response, selector,
      credential, channel, CTID, and compatibility regressions.
- [x] Eliminate the pending-validation lock inversion exposed by sibling-ledger
      recovery and pin ancestry resolution outside the validation tracker lock.
- [ ] Commit and push the conformance corrections; require green CI at the exact
      reviewed remote head.
- [ ] Run the separate AI-comment cleanup phase and require green CI at the exact
      final remote head.
- [ ] Record the final reviewed heads, verification, and audit result below.

# Issue #1301 — testnet readiness R1

## Plan

- [x] Record the exact `origin/v3.0.0` base and clean local rippled v3.2.0
      oracle, then map each of the seven reported defects to existing Go and
      rippled behavior.
- [x] Make peer limits derived from `peers_max` in shipped/generated configs and
      cover small/default limits plus actionable validation errors.
- [x] Emit testnet validator-list defaults, reject an empty trusted validator
      set outside standalone mode, and correct version-appropriate examples.
- [x] Wire the configured data directory through production node startup and
      verify identity, peerfinder cache, and reservation persistence.
- [x] Remove implicit localhost RPC administration while preserving explicit
      admin configuration and covering reverse-proxy requests.
- [x] Correct inbound handshake feature negotiation and compression state,
      including the HTTP 101 response feature header.
- [x] Preserve fixed/bootstrap discovery sources and add bounded failed-dial
      backoff without starving healthy candidates.
- [x] Represent unavailable/expired validator-list quorum as unreachable and
      ensure full-validation gating cannot fire.
- [x] Format and run focused tests, race-sensitive tests where applicable,
      full changed-area suites, vet, lint, build, and relevant conformance.
- [x] Review every changed file and reachable caller for correctness, failure
      paths, lifecycle/concurrency, wiring, and rippled v3.2.0 parity; record the
      coverage matrix and unresolved scope honestly.
- [x] Commit only intentional files, push the issue branch, and open a PR against
      `v3.0.0` with the verified test plan.

## Review

- Base: `origin/v3.0.0` at `48bc716f7f9f9d92a07a494500b7294547f505ac`
  (refetched immediately before commit; worktree merge-base is identical).
- Oracle: clean detached rippled tag `3.2.0` at
  `3c43f4614f87965298773279ff5b85d4c56c637b`.
- Coverage matrix:
  - Peer limits: generated/shipped `peers_max = 21`; default, small, private,
    listenerless, and 100-peer splits cover rippled's 15%/minimum-10 rule.
  - Validator trust: exact mainnet/testnet publisher anchors are emitted beside
    generated configs; non-standalone startup rejects an empty trust source.
  - Persistence: `database_path/peers` now owns node identity, boot-cache, and
    reservation files; identity save failures are fatal and file mode is tested.
  - RPC roles: loopback is guest unless its socket peer matches an explicit
    `admin` network; HTTP, WebSocket, and proxy-header cases are covered.
  - Handshake: outbound offers and inbound 101 responses use request/response
    feature intersection; inbound peers retain the local compression policy.
  - Discovery: configured bootstrap/fixed endpoints survive pruning and cache
    pressure; in-flight reservations and bounded fixed-peer retry delays prevent
    duplicate or hot-looping dials while healthy candidates remain selectable.
  - Validator lists: unavailable-publisher thresholds follow rippled's
    `min(threshold, publishers-threshold+1)` cutoff; unreachable quorum is
    propagated atomically with trust so full validation cannot be declared.
- Verification: `just fmt`, `just vet`, `just build-all`, `just build-nocgo`,
  `just test-core`, `just test`, and `just lint` all pass. Race-enabled tests pass
  for CLI, node, RPC, peer management, validator-list, adaptor, and RCL packages.
  Conformance is 879/879 in scope; the 117 failures are all in the documented
  out-of-scope Batch, Vault, XChain, and XChainSim suites.
- No unresolved in-scope conformance gaps were found. The fail-fast empty-trust
  check is an intentional startup safety requirement from issue #1301.
- Delivery: PR #1317 is open and mergeable from
  `fix/issue-1301-testnet-readiness-r1` into `v3.0.0`.
# Issue #1306 — shared synthetic transaction metadata

## Plan

- [x] Trace every transaction-bearing RPC and stream response through the shared
      JSON rendering path on `v3.0.0`.
- [x] Compare NFT, offer, and MPT synthetic metadata behavior with the exact local
      rippled `3.2.0` tag.
- [x] Move synthetic-field injection to the narrowest shared renderer and remove
      handler-specific duplication.
- [x] Add focused regression tests for the shared renderer and affected RPC
      surfaces.
- [x] Run formatting, focused RPC tests, race coverage, vet, lint, and build.
- [x] Review the final diff for endpoint coverage, mutation safety, and rippled
      parity.

## Review

Implemented one shared JSON metadata enricher for `tx`, `account_tx`, simulate,
and validated transaction/account/book streams. Expanded ledger transactions
receive the MPT issuance ID but not NFT synthetic fields. `transaction_entry`
now leaves metadata untouched; despite the issue text, that is the exact rippled
3.2.0 behavior. API v2 `account_tx` also derives `delivered_amount` from the
unmodified transaction so `DeliverMax` response projection cannot erase the
Payment `Amount` fallback.

Oracle: clean local rippled tag `3.2.0` at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

Verification:

- Focused API v1/v2 synthetic-field and endpoint tests pass.
- `go test ./internal/rpc/... ./internal/node/...` passes.
- `go test -race ./internal/rpc/... ./internal/node/...` passes.
- `just fmt`, `just vet`, `just lint`, `just build-all`, and `just test-core`
  pass; lint reports 0 issues.
- Independent final Go and rippled-conformance reviews found no defects.

## PR #1316 finalization remediation

- [x] Pin the exact PR head, base, clean worktree, and clean local rippled 3.2.0 oracle.
- [x] Review the full diff for Go correctness and protocol-surface conformance.
- [x] Close every confirmed RPC projection, stream, CTID, binary-shape, and historical delivered-amount gap.
- [x] Re-review the completed behavior diff and pass build, vet, and lint.
- [x] Reconcile the advanced `v3.0.0` base and reverify the combined tree.
- [x] Commit and push the behavior fix; require green CI at the exact remote head.
- [x] Run the separate AI-comment cleanup phase, then verify the final remote head and CI.

### Finalization review

Behavior remediation is complete against the pinned PR head and clean local
rippled 3.2.0 oracle at `3c43f4614f87965298773279ff5b85d4c56c637b`.
The review covered all changed RPC and subscription surfaces, ledger-local and
historical transaction lookup, binary parsing, CTID handling, pathfinding, and
synthetic metadata projection. Independent Go and exact-rippled reviews are
clean, including the final zero-`uint256` parsing and ranged-lookup error
propagation edge cases.

Verification on the stable behavior tree: `just build`, `just vet`, and
`just lint` pass; lint reports zero issues. Per the finalization workflow, local
test execution is intentionally deferred to CI. The local audit trail remains
outside the repository until the final remote head and CI result are known.

The advanced bases at `26d0211ed971c98fe919cb28ed5fed90dd962b67` and
`be0e448e95d8e705a19f9d826c352e2f34cd7872` were merged after GitHub reported
successive conflicts. The only textual conflicts were in the task log;
both issue histories were retained. The combined persistence, node wiring, and
RPC behavior tree preserves relational indexing after node-store failures and
guards online-delete notification when rotation is disabled. It passes the same
build, vet, lint, and whitespace gates.

Exact-head CI then exposed stale strict-lint helpers and regression fixtures plus
three parser/runtime defects. Dead helpers were removed, exported keylets now
have required API documentation, account RPC mocks/assertions follow rippled's
lookup and error semantics, XChain vectors and persisted transaction-index
checks use the canonical encodings, and aggregate-price/path-find validation was
corrected. Strict CI lint, repository lint, build, vet, and whitespace checks
pass on the remediation tree; the fixes await a new exact-head CI run.

The next core run exposed failures previously masked by package panics. A stored
transaction projection omitted its API v1 hash and was fixed; the remaining
account, book, deposit-authorized, WebSocket, and simulate failures were stale
ledger-selection or response-decoding fixtures and were aligned with rippled
3.2.0. Strict lint, repository lint, build, vet, and whitespace checks pass on
this second remediation tree.

A third core pass reached previously masked ledger fixtures. Ledger-data limit,
marker, selected-header, warning JSON, and ledger-entry hash expectations were
aligned with rippled 3.2.0 without changing production behavior. All permitted
local gates pass again; exact test verification remains CI-only.

A fourth core pass exposed the remaining shared RPC fixture assumptions. The
ledger, NFT offer, no-ripple, transaction-entry, tx, simulate, and vault fixtures
now model their real lookup and binary contracts. Two production adapters were
also corrected against rippled 3.2.0: simulate restores the JSON transaction type
after STObject validation, and tx emits only the root CTID derived from metadata,
the server network, and rippled's exclusive bounds. A shared partial ledger hash
was restored instead of changing the contract for unrelated suites. Strict lint,
repository lint, build, vet, formatting, and whitespace gates pass; no local
tests were run.

The next core run had one remaining failure: the historical delivered-amount
fixture had been applied to the neighboring MPT projection test. The resolved
modern ledger and sequence now belong to `TestTxProjectsDeliverMax`, while the
unrelated test is restored. Strict lint, repository lint, build, vet, formatting,
and whitespace gates pass; exact test verification remains CI-only.

Exact-head CI passed completely on behavior head `6db8603a`. The separate
comment-only cleanup removed 28 lines of redundant narration and docstrings in
commit `9180fe4c`; strict lint, repository lint, build, vet, formatting, and
whitespace gates passed, followed by a fully green final CI matrix. PR #1316 is
mergeable against unchanged base `be0e448e`.

# Issue #1303 — trust-layer visibility and forward compatibility

Target `origin/v3.0.0`; behavioral oracle is the clean local rippled `3.2.0`
worktree at `3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Match rippled's UNL-blocked operating behavior: immediately demote an
      already-FULL node to CONNECTED and prevent promotion while blocked.
- [x] Surface expired validator-list state in `server_info`, including rippled's
      public warning and admin-only validator-list summary/expiry fields.
- [x] Derive `Ledger.Rules()` from the ledger Amendments object so consensus
      feature gates, especially NegativeUNL round hooks, use ledger truth.
- [x] Accept future unknown overlay message types up to the universal 64 MiB
      ceiling and skip them without disconnecting or resource charges.
- [x] Rewrite Docker handshake interop around the production handshake path,
      pin rippled 3.2.0, and exercise `network_id=1`.
- [x] Add a deterministic, unit-tested manual amendment-registry checker that
      compares a remote `feature` response without claiming scheduled coverage.
- [x] Document host time synchronization and the safe TLS reverse-proxy topology,
      including loopback admin isolation and trusted proxy-header handling.
- [x] Format and run focused package tests after each change group; then run
      build, vet, lint, core/full tests as applicable, Docker interop when the
      local runtime permits it, and inspect the final diff against rippled 3.2.0.
- [x] Record exact verification and review results below, then commit, push, and
      open a PR targeting `v3.0.0`.

## Review

Implemented UNL-blocked mode enforcement and trust-state RPC visibility,
ledger-backed immutable Rules snapshots, forward-compatible overlay framing,
production-path rippled interop, and a manual freshness-checked amendment
registry tool. The operator documentation covers NTP/chrony, TLS termination,
loopback-only admin RPC, and trusted proxy headers. No scheduled checker workflow
was added.

Verification:

- Three independent final audits are clean against the local rippled 3.2.0
  oracle at `3c43f4614f87965298773279ff5b85d4c56c637b`.
- Focused normal and race suites pass for ledger, consensus/adaptor, validator
  lists, RPC, node, overlay, replay, and the amendment checker.
- `just build-all`, `just build-nocgo`, `just vet`, `just lint` (0 issues), and
  `just docs-check` pass.
- The production `Peer.Connect` Docker interop passes against
  `xrpllabsofficial/xrpld:3.2.0` on network ID 1 with the server version checked.
- The live default manual checker reports 98 Testnet amendments on network ID 1
  from a fresh validated ledger close.
- `just test` passes every package except the documented out-of-scope
  conformance suites. `just conformance --failing` reports 941 pass / 117 fail
  overall and 879 pass / 0 fail in scope; failures remain confined to Batch,
  Vault, XChain, and XChainSim.

# Issue #1302 — testnet sync scalability and durability

Target: `v3.0.0`. Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] B1: make missing-node discovery retain full-below knowledge so repeated
      inbound replies resume at unresolved frontiers instead of rescanning the
      state-map root.
- [x] H1: reject stray or failed `liBASE` catch-up adoption unless the acquired
      ledger has complete, verified state matching its header roots; preserve
      acquisition eligibility after rejection.
- [x] B2: persist only state nodes introduced since the validated parent while
      retaining complete ledgers and safe retry/backpressure behavior.
- [x] H4: stamp family-flushed SHAMap nodes with the owning ledger sequence so
      online deletion cannot collect the live content-addressed state tree.
- [x] B3: restore the latest complete durable validated ledger on startup and
      honor configured history bounds rather than resetting to synthetic genesis.
- [x] Discovery: allow peer endpoint exchange to converge during initial sync
      once a peer quorum agrees on a ledger even when the local synthetic
      genesis cannot yet be classified as tracking.
- [x] Add focused regression, lifecycle, persistence, pruning, and restart tests
      for every behavior; use rippled 3.2.0 as the protocol/operational oracle.
- [x] Run format, focused tests, race-sensitive suites where applicable, build,
      vet, lint, and the broad repository test gate.
- [x] Review the full diff for correctness, concurrency, failure paths, and test
      coverage; record exact verification results below.
- [x] Commit only intentional files, push the issue branch, and open a PR with
      base `v3.0.0` and `Fixes #1302`.

## Review

Implemented full-below frontier pruning, complete-state-only initial adoption,
delta node persistence, crash-safe online deletion, validated-tip fast load,
retention-floor wiring, and initial-sync peer tracking. Protocol and operational
behavior was audited against local rippled 3.2.0 at `3c43f4614f87965298773279ff5b85d4c56c637b`.

Verification: focused package tests and race suites pass; `just vet`,
`just build-all`, and `just lint` pass with zero issues. `go test ./...` passes
all packages except the existing `internal/testing/conformance` Vault, Batch,
and XChain backlog, which is outside this diff.

PR: https://github.com/LeJamon/go-xrpl/pull/1319

# Issue #1323 — v3.0.0 Vault replay and transaction conformance

## Plan

- [x] Rebase the work on a clean branch from `origin/v3.0.0`.
- [x] Check pseudo-account, Vault, and directory behavior against local rippled 3.2.0.
- [x] Diagnose every object in the supplied replay findings, including ledger 3025004 and the offer-directory divergences.
- [x] Audit all six Vault transactions and shared helpers against rippled 3.2.0.
- [x] Implement the 22 NUMBER, validation, authorization, rounding, reserve, holding, and cleanup corrections.
- [x] Add focused transaction, invariant, parser, serialization, and replay reconstruction regressions.
- [x] Run focused tests, transaction suites, race tests, full build, vet, lint, and repository tests.
- [x] Complete independent read-only conformance reviews of NUMBER/invariants, create/set/delete, and deposit/withdraw/clawback.

## Review

- Based on `origin/v3.0.0` at `c2c4abfa61c274f6da64451b323bb54fc44ced5b`.
- Checked protocol behavior against a clean local rippled 3.2.0 worktree at
  `3c43f4614f87965298773279ff5b85d4c56c637b`.
- Confirmed that pseudo-account `Balance` and `Sequence`, the XRP Vault `Asset`,
  and both directory memberships in the VM finding are valid rippled state. The
  original engine divergence is the serialized default-zero `AssetsMaximum`.
- Corrected fix-aware large/legacy NUMBER arithmetic and serialization, asset
  association, Vault invariant reconciliation, JSON field presence, and
  malformed `AssetsMaximum` parsing.
- Matched rippled behavior across VaultCreate, VaultSet, VaultDelete,
  VaultDeposit, VaultWithdraw, and VaultClawback, including TER precedence,
  private-domain and MPT authorization, freeze/lock inheritance, share/asset
  rounding, reserve boundaries, owner counts, and ledger-object cleanup.
- Added byte-level reconstruction coverage for ledger 3025004 plus focused
  regressions for every independently identified discrepancy.
- `go test ./internal/tx/... ./internal/testing/vault -count=1`, focused race
  tests, `just vet`, `just lint`, and `just build-all` pass.
- `go test ./... -count=1` passes every package except the existing generated
  `internal/testing/conformance` Vault, Batch, and XChain backlog. The focused
  Vault integration and transaction suites pass.
- A local replay was not rerun because this machine does not contain the VM's
  replay database; the supplied findings are covered with byte-level and
  transaction-level regressions.

PR: https://github.com/LeJamon/go-xrpl/pull/1324

# AMMCreate replay default-field conformance

Target: `origin/v3.0.0` at
`13080bc66fe678314f7633f65caf36f3737e3564`. Behavioral oracle: clean local
rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Trace AMMCreate construction of the AMM and both RippleState objects.
- [x] Compare all conditionally present fields with rippled 3.2.0 and its tests.
- [x] Reproduce the XRP `Asset` and zero trust-line node field divergences.
- [x] Implement the minimal metadata-reconstruction fix and audit adjacent
      AMMCreate default fields.
- [x] Run focused AMM and serialization tests, transaction tests, vet, lint,
      and build.
- [x] Review the final diff and record exact results below.

## Review

- Root cause: AMMCreate and RippleState serialization already match rippled.
  Rippled stores required XRP AMM assets and explicitly written zero
  `LowNode`/`HighNode` values in the SLE, but omits them from
  `CreatedNode.NewFields` because their serialized values are defaults. Replay
  reconstruction was treating those deliberately partial NewFields as the
  complete object.
- Fix: restore default XRP for either required AMM asset slot and restore absent
  zero node hints for newly created RippleState objects before re-encoding.
- Rippled 3.2.0 references checked: `AMMCreate.cpp`, `ApplyStateTable.cpp`,
  `RippleStateHelpers.cpp`, `STIssue.cpp`, `STInteger.h`, and the AMM/RippleState
  ledger-entry formats.
- Regression coverage: byte-level CreatedNode reconstruction for XRP/IOU and
  XRP/MPT AMMs, both XRP/IOU trust lines, and end-to-end AMMCreate metadata
  omission behavior.
- Passed: replaytool tests; all AMM transaction and integration tests; all
  `internal/tx/...` tests; focused race tests; `just vet`; required and advisory
  golangci-lint configurations; `just build-all`; `git diff --check`.
- Full `go test ./... -count=1 -timeout 15m` passed every package except the
  existing conformance corpus failures under Vault, XChain, and XChainSim,
  which are listed in `scripts/conformance-out-of-scope.txt`.
- The original ledger 3025006 replay was not rerun because its VM database is
  not present locally; the supplied field-level divergence is covered directly
  by the byte-level reconstruction tests.

# Issue #1326 — trusted metadata STArray serialization

Target: `origin/v3.0.0` at
`bf5709635715a158437b3718982a8a363fb01c59`. Behavioral oracle: clean local
rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Trace trusted metadata serialization and the transaction/state commit
      boundary, including every caller of the codec array-size guard.
- [x] Confirm rippled 3.2.0 separates untrusted JSON limits from internally
      generated metadata and preserves atomic ledger application.
- [x] Add focused regressions for a 1,027-node metadata array, the 512-element
      untrusted-input limit, and serialization failure before state commit.
- [x] Implement the smallest production-quality API and engine changes that
      satisfy those contracts.
- [x] Run formatting, focused codec/transaction/engine tests, broader affected
      suites, vet, strict lint, build, and diff review.
- [x] Record the review results and prepare only intentional files for delivery
      against `v3.0.0`.

## Review

- Rippled v3.2.0 applies its 512-element limit only while parsing untrusted JSON;
  native `TxMeta` STArrays serialize without that cap. Its separate metadata
  safeguard is the 5,200 modified-entry `tecOVERSIZE` limit.
- Trusted internal metadata now bypasses only the JSON-input array guard while
  retaining unknown-field and binary-format validation. Ordinary `Encode` and
  `EncodeBytes` calls still reject arrays over 512.
- `BlockProcessor` applies each transaction to an unpublished mutable snapshot,
  serializes and inserts the tx+meta leaf there, then adopts state, transaction
  map, and destroyed drops together. Serialization/insertion failure discards
  the snapshot without advancing either transaction counter.
- Applied-first result classification and TxQ transaction-index seeding match
  rippled's open-ledger behavior. Replay callers no longer insert a second leaf.
- Passed uncached focused and broad codec, transaction, ledger, and replay tests;
  focused race tests; `just fmt`; `just build-all`; `just vet`; CI-pinned strict
  golangci-lint v2.11.3; advisory lint; and `git diff --check`.

## PR #1329 finalization

- [x] Review the complete PR diff and production callers for correctness.
- [x] Verify every protocol-bearing change against local rippled v3.2.0.
- [x] Make transaction staging atomic without flushing backed SHAMaps per tx.
- [x] Remove redundant implementation narration in a separate cleanup commit.
- [x] Diagnose the final merged-head consensus failure against both PR and base.
- [x] Pin the mixed-node smoke to rippled v3.2.0 and pass the local topology.

### Finalization review

The original consensus smoke used rippled 2.6.2. Its newly funded AccountRoot
starts at Sequence 1, while rippled v3.2.0 sets Sequence to the current ledger
sequence in `Payment::doApply`. The Go implementation matches v3.2.0, so the old
image created deterministic account-state and transaction-metadata divergence
before exercising PR #1329. The smoke topology and both CI callers now use the
project's mandated rippled 3.2.0 image and its `/config` entrypoint contract.
# Issue #1360 — peer_private fixed-peer-only policy

Target: `origin/main` at `7aab186ddb36317bdb63c831e6ffb9c5dd25b364`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Trace every outbound candidate source and private-mode configuration path
      in go-xrpl, including bootstrap, fixed, gossip, redirect, boot cache,
      restart, and DNS re-resolution behavior.
- [x] Confirm rippled v3.2.0 private-mode configuration and PeerFinder selection
      semantics in the pinned local oracle.
- [x] Add focused regressions proving private mode dials only fixed peers while
      public mode retains all existing discovery behavior.
- [x] Implement the smallest layered policy enforcement that prevents current
      and future dynamic candidate sources from bypassing private mode.
- [x] Run formatting, focused and race tests, affected package/core tests, build,
      vet, strict CI lint, advisory lint, and diff checks.
- [x] Review the complete diff for concurrency, reconnect/DNS behavior, startup
      wiring, and rippled v3.2.0 conformance; record exact results below.
- [x] Stage intentional files only, commit, push, open the PR against `main`, and
      verify the published head and initial CI state.

## Review

- `Discovery.SelectPeersToConnect` now selects and reserves configured fixed
  peers before applying the private-mode gate, then skips every ordinary live
  and boot-cache candidate. This matches rippled v3.2.0's fixed handout before
  its `autoConnect` check; learned data remains stored but cannot be auto-dialed.
- Public-mode discovery is unchanged. The paired regression proves bootstrap,
  fixed, gossip, redirect, and boot-cache candidates all remain eligible when
  privacy is off and only the fixed candidate remains eligible when it is on.
- Restart coverage proves a persisted stale fixed endpoint is loaded but not
  selected after the configured hostname resolves to a new address. Fixed
  disconnect/reconnect cooldown remains intact. Rippled v3.2.0 likewise
  re-resolves configured names on process restart rather than periodically.
- Existing zero ordinary inbound capacity and hops-0 self-gossip suppression
  remain covered and unchanged. Manual administrative `Overlay.Connect` remains
  available, matching rippled's `peer_connect` override.
- Passed repeated focused tests, the full peer-management package, focused race
  coverage, `just test-core`, `just fmt`, `just build-all`, `just vet`, tagged
  PostgreSQL vet, CI-pinned strict lint, advisory lint, and `git diff --check`.
- Independent final Go-quality, adversarial-test, and rippled-conformance
  reviews found no Blocking, Major, Minor, or Nit issues.
- Behavior commit `d1fc5be2` and test-hardening commit `5f0890f8` are published
  on `fix/issue-1360-peer-private-fixed-peers`; PR #1364 targets `main` at
  https://github.com/LeJamon/go-xrpl/pull/1364.

# Issue #1396 — queued status from a disconnected peer

Target: `origin/main` at `e171f7c3dbf7741cbcd89ca7c5b1bad270aefe19`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Problem

- The overlay can enqueue a peer's status frame before removing that peer, while
  the router consumes the frame only after `HandlePeerDisconnect` has cleared
  the peer's state.
- Router dispatch carries only the process-unique peer ID, so the delayed status
  currently recreates `peerStates` and the peer LCL vote for a dead session.
- Catch-up treats the status origin as a valid send destination. An immediate
  `ErrPeerNotFound` then remains in the legacy acquisition for its full retry
  budget instead of selecting a connected replacement.
- Rippled v3.2.0 serializes status dispatch with the owning connection lifetime
  and resolves retained acquisition peer IDs through the live overlay at each
  send boundary.

## Plan

- [x] Validate GitHub access, issue state and comments, linked PRs, active release
      branches, exact base, clean dedicated worktree, and oracle provenance.
- [x] Trace router dispatch, peer lifecycle callbacks, status-derived state,
      acquisition hinting, immediate send failures, timer retries, and existing
      issue #1391 regressions.
- [x] Verify the corresponding session-ordering, unique peer identity, live-peer
      resolution, and replacement behavior against local rippled v3.2.0.
- [x] Add live-session dispatch checks and a lossless router-owned disconnect
      cleanup worker so queued messages from ended sessions cannot restore state
      and the overlay callback never performs acquisition scans.
- [x] Re-resolve ledger-base destinations after disconnect-class send failures;
      immediately use a live eligible replacement, retain peerless generic
      acquisitions, or remove consensus/history acquisitions until re-armed.
- [x] Add focused regressions for the exact enqueue/disconnect/replacement/status
      ordering, no-replacement behavior, immediate reselection, and non-disconnect
      send errors retaining the existing bounded retry semantics.
- [x] Run formatting, focused and race tests, affected core tests, build, vet,
      strict CI lint, advisory lint, and diff checks.
- [x] Review the complete diff for lock ordering, callback races, acquisition
      ownership, oracle parity, and test coverage; record exact results below.
- [x] Stage only intentional files, commit, push, open the PR against `main`, and
      verify the published head and CI state.

## Review

- Dispatch rejects messages whose originating peer session is no longer live and
  repeats idempotent disconnect cleanup after handling to close the admitted-
  message/disconnect race. Process-unique, monotonic peer IDs keep that cleanup
  scoped to the ended connection.
- The overlay callback publishes disconnect IDs through a coalescing mailbox and
  nonblocking wake. One router-owned worker performs peer-state, LCL, validator-
  list, catch-up/history hint, and active-acquisition cleanup and is joined when
  `Router.Run` exits.
- Consensus, history, validation-stash, and RPC generic base requests all resolve
  their peer at the final send boundary. `ErrPeerNotFound` and
  `ErrConnectionClosed` remove the dead source and immediately try another live
  peer. Generic acquisitions remain registered with an empty peer set when no
  peer is available, matching rippled's later timer-driven peer attachment;
  fetch-pack escalation snapshots one nonzero peer at its own send boundary.
- Compared with the clean local rippled v3.2.0 oracle at
  `3c43f4614f87965298773279ff5b85d4c56c637b`: per-session dispatch/close ordering,
  live peer lookup at send time, `PeerSet` dead-session skipping, generic
  acquisition retention, and timer-driven replacement behavior match.
- Verification passed: `just fmt`; relevant package tests and their race build;
  focused ordering/generic regressions at `-count=100`; `just test-core`;
  `just build-all`; `just build-nocgo`; `just vet`; strict and advisory
  `golangci-lint`; PostgreSQL-tagged vet; and `git diff --check`.
- Two independent final reviews found no remaining correctness, race, deadlock,
  callback-lifecycle, test-coverage, or rippled-conformance blockers.

# Issue #1391 — stale initial-sync catch-up peer

Target: `origin/main` at `456073c17acc79b2022c010f2173ab3e71e92580`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate GitHub access, issue state and discussion, linked PRs, active
      release branches, exact base, and clean dedicated worktree.
- [x] Trace the complete catch-up target, peer lifecycle, acquisition dispatch,
      immediate-failure, and maintenance retry paths with their current tests.
- [x] Verify rippled v3.2.0 peer selection, disconnect, and acquisition retry
      behavior in the pinned local oracle.
- [x] Add focused regressions for same-sequence replacement, no eligible peers,
      disconnect invalidation, and bounded retries after immediate dispatch failure.
- [x] Implement the smallest production-quality fix preserving existing sync
      behavior while eliminating stale-peer dispatch and the 100 ms hot loop.
- [x] Run formatting, focused and race tests, affected core tests, build, vet,
      strict CI lint, advisory lint, and diff checks.
- [x] Review the complete diff for concurrency, failure paths, oracle parity, and
      test coverage; record exact results below.
- [x] Stage only intentional files, commit, push, open the PR against `main`, and
      verify the published head and initial CI state.

## Review

- Catch-up dispatch now uses a fresh gossip origin or selects from currently
  connected peer state at timer-driven dispatch; disconnects invalidate cached
  target provenance and same-ledger replacements join the retained acquisition.
- Immediate base-request failures remain in one acquisition governed by the
  existing three-second timer and seven-attempt no-progress budget. Exhausted
  acquisition hashes enter a five-minute recent-failure cache, including forward
  child ledgers and direct validation/status acquisition paths.
- The behavior matches rippled v3.2.0's live peer resolution, fixed timeout
  progression, bounded retry count, and five-minute recent-failure suppression.
- Verification passed: `go test ./internal/consensus/adaptor/... -count=1`, the
  focused race suite, `just test-core`, `just build-all`, `just vet`, tagged
  PostgreSQL vet, strict golangci-lint v2.11.3, advisory lint, gofmt, and
  `git diff --check`.
- Final adversarial review found no unresolved lock-order, data-race, retry-bound,
  peer-replacement, or rippled-conformance blocker after centralizing the failed
  hash gate in the shared consensus acquisition entry point.

# Issue #1395 — bootstrap manifest relay amplification

Target: `origin/main` at `e171f7c3dbf7741cbcd89ca7c5b1bad270aefe19`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate GitHub access, issue state and discussion, linked PRs, active
      release branches, exact base, and clean dedicated worktree.
- [x] Trace manifest acceptance, relay, recipient selection, send-queue
      backpressure, router dispatch, counters, logging, and existing tests.
- [x] Verify the complete corresponding behavior and tests against local
      rippled v3.2.0, including batching and execution context.
- [x] Add deterministic regressions for bounded relay frames, one post-processing
      recipient snapshot, one backpressure attempt, and router progress.
- [x] Implement the smallest production-quality fix that preserves accepted
      manifest semantics while bounding fan-out and dispatch latency.
- [x] Run formatting, focused and race tests, affected core tests, build, vet,
      strict CI lint, advisory lint, and diff checks.
- [x] Review the complete diff for concurrency, failure paths, oracle parity, and
      test coverage; record exact results below.
- [x] Stage only intentional files, commit, push, open the PR against `main`, and
      verify the published head and initial CI state.

## Review

- Moved inbound manifest verification off the consensus router onto a single
  four-entry FIFO worker lane. Saturation is non-blocking and counted; admitted
  work drains completely before router shutdown.
- Matched rippled v3.2.0 for admitted frames: apply the full list, preserve the
  accepted bytes and order, then broadcast one aggregate frame to one
  post-processing peer snapshot, including the source peer.
- Added deterministic coverage for 100-entry aggregation, bounded saturation
  and status-message progress, shutdown drain/join behavior, and a real overlay
  with one full and one healthy peer send queue.
- Passed `go test -race ./internal/consensus/adaptor`,
  `go test -race ./internal/peermanagement`, `just build-all`, `just test-core`,
  `just vet`, postgres-tagged `go vet`, strict and advisory golangci-lint 2.11.3,
  and `git diff --check`. The core suite was rerun successfully with an isolated
  Go build cache after the shared cache reported a missing entry.
- Three final focused reviews found no unresolved concurrency, lifecycle,
  test-design, or rippled-conformance defect. Rippled does not shed its manifest
  job queue; the bounded saturation policy is an intentional Go overload
  safeguard required to keep this repository's single router responsive.

# Issue #1404 — LoanSet holding owner count

- [x] Validate issue state, repository, base branch, and rippled 3.2.0 behavior.
- [x] Create `fix/issue-1404-loanset-owner-count` from `origin/main`.
- [x] Apply borrower and broker-owner holding deltas without clobbering the submitter.
- [x] Add focused regression coverage for new/existing holdings and account roles.
- [x] Format, test, build, vet, lint, and review the final diff.
- [x] Commit, push, and open the pull request.
- [x] Finalize PR #1406 against rippled v3.2.0, including reserve checks and
      end-to-end LoanSet coverage.

## Review

- LoanSet now applies the owner-count delta returned by borrower and origination-
  fee holding creation. Submitter deltas update `ctx.Account`; counterparty deltas
  use the canonical view-backed AccountRoot writer.
- Zero deltas from XRP, issuer accounts, and existing IOU/MPT holdings remain
  no-ops. Focused coverage exercises new IOU and MPT holdings, existing holdings,
  submitter and counterparty borrowers, and the fee-recipient role.
- Rippled v3.2.0 performs the same count transition inside `addEmptyHolding`.
- Holding creation now receives the caller's explicit pre-fee or ledger balance,
  matching rippled's reserve checks for both IOU trust lines and MPT holdings.
- End-to-end LoanSet coverage verifies successful and failed reserve boundaries,
  rollback, top-up recovery, signer roles, owner directories, and existing holdings.
- Passed lending transaction and integration tests, `just build-all`, `just vet`,
  `just lint`, and `git diff --check`.

# Issue #1408 — IOU EscrowFinish divideRound parity

Target: `origin/main` at `a7caa2fb4e2efed48288c42294ede0783d92fd40`.
Behavioral oracle: clean local rippled `3.2.0` worktree at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

## Plan

- [x] Validate GitHub access, issue state and discussion, linked PRs, active
      release branches, exact base, clean dedicated worktree, and oracle provenance.
- [x] Trace IOU escrow transfer-fee calculation and rate selection in go-xrpl and
      rippled v3.2.0, including parity and issuer-involved behavior.
- [x] Add focused regressions for the ledger 3,275,748 rounding value and
      `min(lockedRate, currentRate)` selection.
- [x] Replace the escrow-only `MulRatio` shortcut with rippled's non-strict
      `divideRound` operation while preserving the escrow amount's issue.
- [x] Run formatting, focused escrow and state tests, affected transaction and
      integration suites, build, vet, strict/advisory lint, and diff checks.
- [x] Attempt the ledger 3,275,748 replay and record the unavailable local database
      and checkpoint inputs when it cannot run.
- [x] Review the final diff for arithmetic parity, failure paths, adjacent rate
      behavior, and test coverage; record exact results below.
- [ ] Stage only intentional files, commit, push, open the PR against `main`, and
      verify the published head and initial CI state.

## Review

- Replaced the escrow-only `MulRatio` shortcut with non-strict `state.DivRound`,
  representing the transfer rate as mantissa `rate` at exponent `-9` and retaining
  the escrow amount's currency and issuer. This matches rippled v3.2.0
  `Rate2.cpp` and `EscrowHelpers.h` behavior without changing global `MulRatio`.
- Added an exact regression for `100 / 1.01 = 99.00990099009901` and end-to-end
  coverage proving the lower of the locked and current issuer rates is used.
  Existing parity and issuer guards remain unchanged.
- Passed focused transaction, integration, and state tests; the complete
  `internal/tx/...` shard; focused race tests; repeated rounding/rate-selection
  regressions; `just build-all`; `just vet`; strict CI and advisory golangci-lint
  v2.11.3; gofmt; and `git diff --check`.
- The full integration shard passed the changed escrow package and reproduced only
  the repository's existing out-of-scope Vault, XChain, Batch, and NFT conformance
  corpus failures.
- The one-ledger replay started with rippled v3.2.0 compatibility semantics but
  could not connect to PostgreSQL at `localhost:5432`. No ledger 3,275,748 fixture
  or checkpoint exists in the workspace, so account/transaction roots could not
  be replay-verified locally.
- Three independent final reviews found no Go-correctness, test-quality,
  verification, or rippled v3.2.0 conformance findings.

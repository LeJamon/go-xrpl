# Issue #926 — unify the native canonicalize (Option 2)

Branch: refactor/issue-926-unify-amount-math (off origin/main @ 6a35f957)

## Context

The muldiv-round consolidation from #926 already landed via #894 (`d3783283` + `c54a14cc`):
one `muldivRound` core, shared `MulMantissas`/`DivMantissas`, `CanonicalizeRoundIOUOverflow`,
`PrepareMulDivOperand`, `FinalizeRoundIOU`; golden differential tests
(`amount_round_golden_test.go`, `offer_quality_golden_test.go`).

The one unmet acceptance-criterion is **"one canonicalize pair"**: the *native* (XRP-drops)
canonicalize is still duplicated across packages:

- `state.canonicalizeRoundNative` (round branch **silently drops a positive residual offset** — a
  documented, unreachable bug) — used by `MulRoundNative`/`DivRoundNative`.
- `payment.CanonicalizeDrops` / `CanonicalizeDropsStrict` (faithful: multiply on `offset>0`) — used
  by the `offer` native path **and** `payment/quality.go` (2 sites each).

These two genuinely diverge on the unreachable `offset>0` overflow corner (golden row 0 `ru=true`:
state `MulRoundNative` = `33333333333333330` vs offer `offerMulRound` = `-3520120672398401536`).

## Goal

A single native-round canonicalize, living in `state` (lowest layer — `payment`→`state` and
`offer`→`state` are legal, the reverse isn't), consumed by `state` + `offer` + `payment`.

## Plan

- [ ] **state/amount_round.go**: move `CanonicalizeDrops(mantissa, exponent) int64` and
      `CanonicalizeDropsStrict(mantissa, exponent, roundUp) int64` here (verbatim from payment — the
      rippled-faithful loop-count / hadRemainder pair). Exported (payment/quality.go calls them).
- [ ] **state/amount_round.go**: add `NativeRoundDrops(amount uint64, offset int, resultNegative,
      roundUp, addSlop, strict bool) int64` — the single native finalizer (addSlop→Canonicalize
      Drops{,Strict}; else floor-rescale; zero→1 fixup; sign). This is offer's current
      `offerNativeDrops` core, promoted to `state`.
- [ ] **state/amount_round.go**: rewrite `MulRoundNative`/`DivRoundNative` to delegate to
      `NativeRoundDrops`; delete `canonicalizeRoundNative` + `finalizeRoundNative`.
- [ ] **offer/offer_quality.go**: `offerNativeDrops` → thin wrapper over `state.NativeRoundDrops`;
      drop the now-unused `payment` import.
- [ ] **payment/amount.go**: delete `CanonicalizeDrops` + `CanonicalizeDropsStrict` (keep the
      private `canonicalizeDropsFloor` / `canonicalizeDropsRound` — separate concern, kept callers).
- [ ] **payment/quality.go**: update the 2 call sites to `state.CanonicalizeDrops` /
      `state.CanonicalizeDropsStrict`.

## Behaviour-preservation contract

- `offer` + `payment` native paths: **byte-identical** (logic moved, not changed) → their golden
  suites stay green unchanged.
- `state.MulRoundNative`/`DivRoundNative`: change **only** on `addSlop && offset>0` inputs — fixes
  the documented drop-offset bug to the rippled-faithful multiply behaviour (now == offer/payment).
  These inputs produce absurd `>10^17`-drop "XRP" amounts that cannot occur in a valid ledger, so
  no reachable / conformance behaviour changes. Re-capture the affected
  `amount_round_golden_test.go` native rows and document why.

## Verify

- [ ] `go test ./internal/ledger/state/... ./internal/tx/offer/... ./internal/tx/payment/...`
- [ ] `go test ./internal/tx/... ./internal/testing/...` (offer-crossing, payment-flow, AMM)
- [ ] `go vet` on the three packages
- [ ] conformance: 0 regressions vs merge base

## Review

Done. Single native-round canonicalize now lives in `state` (`CanonicalizeDrops`,
`CanonicalizeDropsStrict`, `NativeRoundDrops`); `offer` and `payment/quality.go` delegate to it;
`payment`'s duplicate `CanonicalizeDrops`/`CanonicalizeDropsStrict` deleted. Net −51 LOC of
non-test code.

- `go build ./...`, `go vet` (state/offer/payment), `gofmt -l`: all clean.
- `go test ./internal/tx/...` (23 pkgs) + `internal/ledger/state` + offer/payment/AMM integration
  suites: all green.
- offer & payment golden suites: **unchanged** (logic moved, not changed).
- state golden suite: native MN/DN columns re-captured for the `addSlop && offset>0` rows only —
  `MulRoundNative`/`DivRoundNative` now scale a positive residual offset like offer/payment/rippled
  instead of dropping it. Those inputs produce `>10^17`-drop (impossible) amounts; IOU columns and
  all reachable native cases are byte-identical.
- **Conformance: 0 regressions, 0 deltas** — 204 failing subtests on branch vs origin/main
  (@6a35f957), byte-identical sets, verified by in-worktree `git stash` baseline diff.

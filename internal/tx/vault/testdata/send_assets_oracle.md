# Multi-recipient lending transfers

Oracle: private `XRPLF/xrpld-private` tag `3.4.1`, commit
`d147fccf54a500fce586522f28d6044c37fd8d29`. The reference checkout is read-only.
Baseline reproduction used go-xrpl `68b7a10b7de1e828e09ccdc2e1e739ef3b761ba5`.
The repair is based on refreshed `v3.4.1` at
`02f4f17c7d1c1b5676b112ddfa669655bcff4415`.

## Behavioral conclusion

Sequential `SendAsset` calls are not equivalent to `accountSendMulti`.

| Asset | Required behavior |
| --- | --- |
| XRP | Credit each existing recipient after checking the running signed debit. Debit the sender once after all credits. Aggregate overflow returns `tecINTERNAL`; insufficient funds returns `tecFAILED_PROCESSING`. |
| MPT | Accumulate actual sender cost with checked signed arithmetic, including direct redemptions. Transit sends issue credits first and redeem the combined transit debit last. Direct issuer/holder sends remain ordered within the loop. |
| IOU | Credit recipients in order and debit transit funds once, preserving the aggregate STAmount rounding and trust-line effects. No new integral overflow guard applies. |

`LoanSet` disburses the principal less the origination fee to the borrower, then
the origination fee to the broker owner. `LoanPay` pays the rounded vault amount,
then the broker fee. Both C++ calls explicitly use `WaiveTransferFee::Yes`, so
`SendAssets` has that contract. Nonwaived C++ controls do not represent a Go
lending call and are excluded from the Go replay.

LoanPay checks both recipients' authorization before making either transfer.
Self-transfers and zero amounts are skipped without combining repeated
recipients. XRP/MPT negative amounts are rejected before the self-transfer check.
An MPT recipient equal to the issuer redeems directly; a sender equal to the
issuer issues directly. These amounts contribute to actual sender cost, but not
to the final transit debit.

The IOU control starts with `1e15` units and sends `0.03` to each of two
recipients. The oracle debits the aggregate `0.06`, leaving
`999999999999999.9`. Subtracting each rounded leg separately can leave the
original balance unchanged. Keeping IOU debits aggregated preserves that
existing behavior even though the 3.4.1 overflow guards concern XRP and MPT.

## Amendment profiles

With `fixCleanup3_1_3`, an issuer's MPT sends check the exact running issuance
total plus the original outstanding amount against `MaximumAmount`. Without it,
the cap check deliberately uses the original outstanding snapshot for every
recipient. Updating and re-reading the supply between sequential sends changes
historical results.

`MPTokensV2` independently checks transient issuance and recipient arithmetic.
Transit credits can temporarily increase outstanding supply above the issuance
cap; the final redemption removes the sender debit. The permitted transient
supply remains bounded by `uint64`, and each issued amount remains bounded by
the issuance maximum. The checked signed cost accumulator is unconditional in
3.4.1.

## Reachability and rollback

Overflow vectors that inject extreme balances or use more than the two lending
recipients are helper tests, not valid-ledger loan demonstrations. In particular,
each XRP input is bounded by `10^17` drops, so the two lending recipients cannot
overflow the signed 64-bit aggregate. Testing that guard requires a synthetic
many-recipient input. MPT aggregate overflow likewise must be distinguished from
a funded transaction on a valid ledger.

Failures may leave credits and issuance changes inside the helper's sandbox.
The transaction engine must discard those changes, including preceding loan,
vault and broker accounting. Claimed failures retain only the fee and sequence
or ticket effects. Engine regressions check the affected nodes and compare
unrelated ledger entries byte for byte.

## Reproduction

The C++ corpus has 36 cases: 8 XRP, 4 IOU, and 24 MPT. Go replays all 32
fee-waived cases; the remaining 4 are C++ fee controls. The MPT control sets a
25% transfer fee so the sender debit and outstanding supply demonstrate an
actual fee. The recorded C++ suite passed 2,042 assertions with no failures.

The fixture replay runs with:

```sh
just test-pkg './internal/tx/vault -run TestSendAssetsOracle -count=1'
```

The fixture records the exact reference commit, arithmetic and amendment
profiles, ordered destinations, initial balances, TER, and intermediate sandbox
balances and outstanding supply. C++ harness provenance and generation commands
are recorded alongside the fixture.

To regenerate, use an existing Ninja `xrpld` build from the pinned private
source, with unit tests enabled, and a clean checkout of that commit:

```sh
python3 internal/tx/vault/testdata/send_assets_oracle/record.py \
  --build /path/to/pinned/ninja/build \
  --oracle /path/to/clean/private/3.4.1 \
  --output /tmp/send-assets-oracle
```

The script validates the oracle pin and helper source, links the checked-in
harness against the build's actual `libxrpl.a`, and runs a separate executable.
It leaves the oracle and existing build untouched. The output directory contains
`evidence.json` and full command/hash provenance. Copy the evidence to
`send_assets_oracle.json` and update the adjacent provenance summary when
regenerating. Go CI needs only the checked-in fixture.

Full-engine regressions in `internal/testing/lending/multi_recipient_transfers_test.go`
cover XRP, IOU and MPT LoanSet/LoanPay, real issuer transfer fees, issuer aliases,
both cleanup amendment states, and sequence/ticket failure rollback. On baseline
`68b7a10b7`, the XRP fee-drain LoanPay regression returns
`tecINSUFFICIENT_FUNDS`; the aggregate helper returns the oracle's
`tecFAILED_PROCESSING`. All lending/vault/MPT/payment unit and integration suites
pass with race detection on the repaired release base.

# Memory

## AMM Pseudo-Account Flags
- rippled: `createPseudoAccount()` in View.cpp sets `lsfDisableMaster | lsfDefaultRipple | lsfDepositAuth` (0x01900000)
- goXRPL: additionally sets `LsfAMM` (0x02000000) for fast AMM detection
- Combined flags on AMM account: 0x03900000
- rippled identifies pseudo-accounts via `sfAMMID` field presence; goXRPL uses `LsfAMM` flag

## AMM Pseudo-Account Rejection
- Escrow, PayChan, Check, TrustSet all reject AMM pseudo-accounts as destination
- rippled uses `isPseudoAccount(sleDst)` which checks for `sfAMMID`/`sfVaultID` fields
- goXRPL uses `(destAccount.Flags & sle.LsfAMM) != 0` check
- TrustSet: allows existing trust line modification, blocks new non-LP trust lines
- These checks added in: `escrow_create.go`, `payment_channel_create.go`, `check_create.go`, `trustset.go`

## AMMCreate: Issuer-as-Creator for IOU/IOU AMMs
- When creator IS the issuer of an IOU, skip `updateTrustlineBalanceInView` debit
- Issuer has unlimited supply — no self-trust-line exists or is needed
- `createOrUpdateAMMTrustline` already credits the AMM↔issuer trust line correctly

## AMMDeposit: Freeze Check Gating on AMMClawback
- Both-assets freeze check (`isFrozen` on Asset + Asset2) is ONLY done when `featureAMMClawback` is enabled
- Without AMMClawback: only the specific deposit `Amount`/`Amount2` fields are checked
- This allows depositing a non-frozen asset when the other AMM asset is frozen (pre-AMMClawback)

## AMMWithdraw: Reserve Check + Trust Line Creation (fixAMMv1_2)
- When withdrawing IOU and trust line doesn't exist, must create it
- With `fixAMMv1_2`: check reserve before creating trust line → `tecINSUFFICIENT_RESERVE` if insufficient
- Without `fixAMMv1_2`: no reserve check, trust line created on the fly
- `withdrawIOUToAccount()` helper handles both paths
- `createWithdrawTrustLine()` creates trust line with DirInsert + OwnerCount++

## QualityFunction & limitOut (Payment Quality Limiting)
- File: `internal/core/tx/payment/quality_function.go`
- Implements rippled's `QualityFunction.h/.cpp` + `StrandFlow.h:limitOut()`
- QualityFunction: `q(out) = m * out + b`
  - AMM (single-path): `m = -cfee / poolGets`, `b = poolPays * cfee / poolGets` (cfee = 1 - tradingFee/100000)
  - CLOB-like: `m = 0`, `b = 1/quality.rate()`
- Combine: `new_m = m + b * other.m`, `new_b = b * other.b`
- OutFromAvgQ: `out = (1/quality.rate() - b) / m`
- Step interface: `GetQualityFunc()` added; XRPEndpointStep, DirectStepI use default CLOB-like pattern
- BookStep.GetQualityFunc: checks CLOB vs AMM tip offer, adjusts for transfer fees
- Flow() integration: single strand + limitQuality → `limitOut()` adjusts `remainingOut`
- Quality tolerance: 1e-7 relative distance when output was adjusted by limitOut
- `AuctionSlotFeeScaleFactor = 100000` (rippled AMMCore.h)
- Uses `tx.Amount` (IOU mantissa+exponent) as Go equivalent of rippled's `Number` type

## AMM adjustAmountsByLPTokens Calling Convention (CRITICAL)
- rippled's `withdraw()`/`deposit()` call `adjustAmountsByLPTokens(amountBalance, amount, amount2, ...)`
- **Single-asset modes**: `amount2 = nullopt` → enters "single trade" path in adjustAmountsByLPTokens
- **Two-asset modes**: `amount2 = optional(value)` → enters "equal trade" path
- `amountBalance` = balance of the DEPOSITED/WITHDRAWN asset, not always assetBalance1
- Bug: Go code passed `(assetBalance1, withdrawAmount1, &withdrawAmount2)` for all modes
- Fix: Track `isSingleAssetDeposit`/`isSingleAssetWithdraw` + `singleDepositIsAsset2`/`singleWithdrawIsAsset2`
- Pass `(withdrawAssetBalance, withdrawAmt, nil)` for single-asset modes
- Files: `amm_deposit.go`, `amm_withdraw.go`

## AMM calculateLPTokens Rounding (fixAMMv1_3)
- rippled `ammLPTokens()` uses `Number::downward` rounding when `fixAMMv1_3` is enabled
- Maintains AMM invariant: `sqrt(asset1 * asset2) >= LPTokensBalance`
- Without this rounding, LP tokens off by 1 in last digit → cascading precision errors
- Example: `sqrt(5000*4000)` = `4472135954999580` (no rounding) vs `4472135954999579` (downward)
- Fix: Added `fixV1_3 ...bool` variadic param to `calculateLPTokens()`
- Callers: `amm_create.go` and `amm_deposit.go` (tfTwoAssetIfEmpty path)
- rippled test adjustments: With fixAMMv1_3, use slightly larger deposit limits (e.g., USD(2000.25) instead of USD(2000))

## Permission Delegation (Phase 7)
- `Common.Delegate` field (string, JSON "Delegate,omitempty"): r-address of delegate
- Preflight: `featurePermissionDelegation` must be enabled (SupportedNo), Delegate != Account (temBAD_SIGNER)
- Preclaim order matches rippled: checkSeqProxy → checkPriorTxAndLastLedger → checkFee → checkPermission → checkSign
- Fee balance check: uses delegate's balance when Delegate is present
- checkPermission: `keylet.DelegateKeylet(account, delegate)` → parse SLE → check `permissionValue == txType + 1`
- checkSign: uses delegate as `idAccount` for both single-sign and multi-sign
- doApply fee: deducted from delegate's AccountRoot (not source), both success and tec paths
- Result codes: `TecNO_DELEGATE_PERMISSION = 198`, `TecPRECISION_LOSS = 197`
- Keylet: `spaceDelegate = 'E'`, `indexHash(spaceDelegate, account, authorizedAccount)`
- SLE parsing: `state.ParseDelegate()` → extracts `Permissions []uint32` via binarycodec
- Binary codec: PermissionValue ToJSON returns string names or uint32; `DelegatablePermissions` map has `txName → txType+1`
- Files: `engine.go`, `transaction.go`, `result.go`, `signature.go`, `keylet.go`, `delegate_entry.go`, `delegate.go`

## Pre-existing Test Failures (Not Caused by Changes)
- `internal/core/tx/trustset/trustset_test.go`: wrong `NewIssuedAmount` signatures
- `internal/core/tx/escrow/escrow_test.go`: `*string` vs `string` type mismatches
- `internal/core/tx/payment/flow_test.go`: Result vs nil comparison
- `internal/testing/payment/`: `TestFlow_TransferRate`, `TestDeliverMin_*`, `TestFlow_BookStep` (temBAD_PATH)
- `internal/testing/check/`: `TestCheck_CashQuality` (tecPATH_PARTIAL)
- `internal/core/tx/vault/vault_test.go`: error message mismatch ("positive" vs "required")

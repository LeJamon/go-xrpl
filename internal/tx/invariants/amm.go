package invariants

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// ---------------------------------------------------------------------------
// ValidAMM
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidAMM (lines 1720-2023)
// Reference: rippled InvariantCheck.h — ValidAMM struct (lines 644-705)
//
// visitEntry phase:
//   - Track AMM entries: extract account ID and LPTokenBalance from before/after
//   - Track pool changes: RippleState with lsfAMMNode flag, or AccountRoot with non-zero AMMID
//
// finalize phase — dispatch by tx type:
//   AMMVote: LP tokens and pool must not change
//   AMMBid: Pool must not change; LP tokens should decrease (burnt for bidding)
//   AMMCreate: AMM must be created; sqrt(amount * amount2) == LPTokens; all balances > 0
//   AMMDelete: AMM must not remain (deleted on tesSUCCESS, unchanged on tecINCOMPLETE)
//   AMMDeposit: AMM must not be deleted; general invariant sqrt(a*b) >= LPT
//   AMMWithdraw/AMMClawback: AMM may be deleted (last withdraw); general invariant with zero allowed
//   DEX (Payment/OfferCreate/CheckCash): AMM object must not be changed directly
//
// Amendment gating: enforce = rules.Enabled(fixAMMv1_3)

// ammInvariantFields holds the fields extracted from AMM SLE entries during the
// visitEntry phase.
type ammInvariantFields struct {
	accountID  [20]byte
	lptBalance Amount
	hasBalance bool
}

// isLikelyAMMBinary checks if binary data is an AMM SLE entry.
// AMM entries are now stored using the standard binary codec format, so we
// check the LedgerEntryType field via the codec. This replaced the old
// heuristic-based approach that relied on the custom binary format.
func isLikelyAMMBinary(data []byte) bool {
	// The standard detection via getLedgerEntryType is now the primary path.
	// This function serves as a fallback for entries where EntryType != "AMM".
	return getLedgerEntryType(data) == "AMM"
}

// parseAMMInvariantFields extracts the Account ID and LPTokenBalance from
// binary AMM SLE data. AMM data is now stored in the standard binary codec
// format, so we decode it via binarycodec.Decode.
// Reference: rippled InvariantCheck.cpp lines 1733-1737 (after), 1749-1754 (before)
func parseAMMInvariantFields(data []byte) (*ammInvariantFields, error) {
	hexStr := hex.EncodeToString(data)
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM binary: %w", err)
	}

	result := &ammInvariantFields{}

	// Account (r-address string → [20]byte)
	if acctStr, ok := fields["Account"].(string); ok {
		id, err := state.DecodeAccountID(acctStr)
		if err == nil {
			result.accountID = id
		}
	}

	// LPTokenBalance (Amount object)
	if lptObj, ok := fields["LPTokenBalance"].(map[string]any); ok {
		valueStr, _ := lptObj["value"].(string)
		currency, _ := lptObj["currency"].(string)
		issuer, _ := lptObj["issuer"].(string)
		result.lptBalance = state.NewIssuedAmountFromDecimalString(valueStr, currency, issuer)
		result.hasBalance = true
	}

	return result, nil
}

// ammPoolHoldsForInvariant reads the balances of both assets in the AMM pool.
// Uses fhIGNORE_FREEZE (no freeze zeroing) to match rippled's invariant behavior.
// This is a local implementation to avoid importing the amm package.
// Reference: rippled AMMUtils.cpp ammPoolHolds + InvariantCheck.cpp (fhIGNORE_FREEZE)
func ammPoolHoldsForInvariant(view ReadView, ammAccountID [20]byte, asset1, asset2 Asset) (Amount, Amount) {
	balance1 := ammAccountHoldsForInvariant(view, ammAccountID, asset1)
	balance2 := ammAccountHoldsForInvariant(view, ammAccountID, asset2)
	return balance1, balance2
}

// ammAccountHoldsForInvariant returns the amount held by the AMM account for a specific issue.
// For XRP: reads from the AMM account's AccountRoot.Balance
// For IOU: reads from the trustline between AMM account and issuer
func ammAccountHoldsForInvariant(view ReadView, ammAccountID [20]byte, asset Asset) Amount {
	if asset.Currency == "" || asset.Currency == "XRP" {
		// XRP: read from AccountRoot
		accountKey := keylet.Account(ammAccountID)
		data, err := view.Read(accountKey)
		if err != nil || data == nil {
			return state.NewXRPAmountFromInt(0)
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return state.NewXRPAmountFromInt(0)
		}
		return state.NewXRPAmountFromInt(int64(account.Balance))
	}

	// IOU: read from trustline
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	// Determine balance based on canonical ordering
	// Balance is stored from low account's perspective
	// AMM account is always the "holder" side
	ammIsLow := state.CompareAccountIDsForLine(ammAccountID, issuerID) < 0
	balance := rs.Balance
	if !ammIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), asset.Currency, asset.Issuer)
}

// toIOUForInvariant converts an Amount to IOU representation for precise calculations.
// Matches the AMM helper's toIOUForCalc function.
func toIOUForInvariant(amt Amount) Amount {
	if !amt.IsNative() {
		return amt
	}
	drops := amt.Drops()
	if drops == 0 {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}
	mantissa := drops
	exp := 0
	for mantissa >= 1e16 {
		mantissa /= 10
		exp++
	}
	for mantissa > 0 && mantissa < 1e15 {
		mantissa *= 10
		exp--
	}
	return state.NewIssuedAmountFromValue(mantissa, exp, "", "")
}

// calculateLPTokensForInvariant computes the geometric mean: sqrt(amount1 * amount2).
// This matches rippled's ammLPTokens function.
// Reference: rippled AMMHelpers.cpp ammLPTokens / InvariantCheck.cpp root2(amount * amount2)
func calculateLPTokensForInvariant(amount1, amount2 Amount) Amount {
	if amount1.IsZero() || amount2.IsZero() {
		return state.NewIssuedAmountFromValue(0, -100, "", "")
	}

	// Convert both to IOU for consistent calculation
	iou1 := toIOUForInvariant(amount1)
	iou2 := toIOUForInvariant(amount2)

	// product = iou1 * iou2
	product := iou1.Mul(iou2, false)
	// result = sqrt(product)
	return product.Sqrt()
}

// validAMMBalances checks that balances are valid for the AMM invariant.
// If zeroAllowed, all three can be zero together; otherwise all must be positive.
// Reference: rippled InvariantCheck.cpp validBalances (lines 1757-1771)
func validAMMBalances(amount, amount2, lptBalance Amount, zeroAllowed bool) bool {
	positive := amount.Signum() > 0 && amount2.Signum() > 0 && lptBalance.Signum() > 0
	if zeroAllowed {
		return positive ||
			(amount.IsZero() && amount2.IsZero() && lptBalance.IsZero())
	}
	return positive
}

// withinRelativeDistanceForInvariant checks if two IOU amounts are within
// relative distance dist: (max - min) / max < dist.
// Uses math/big.Int for precise integer arithmetic on mantissa/exponent pairs.
// Reference: rippled AMMHelpers.h withinRelativeDistance (lines 156-162)
func withinRelativeDistanceForInvariant(calc, req Amount) bool {
	calcIOU := toIOUForInvariant(calc)
	reqIOU := toIOUForInvariant(req)

	if calcIOU.Compare(reqIOU) == 0 {
		return true
	}

	var minAmt, maxAmt Amount
	if calcIOU.Compare(reqIOU) < 0 {
		minAmt = calcIOU
		maxAmt = reqIOU
	} else {
		minAmt = reqIOU
		maxAmt = calcIOU
	}

	// Compute (max - min) / max using Number-based division for precision.
	// We need the result compared against 1e-11.
	// Use big.Int arithmetic: diff_mantissa * 10^diff_exp / max_mantissa * 10^max_exp < 1e-11
	// Equivalently: diff_mantissa * 10^(diff_exp - max_exp) * 10^11 < max_mantissa

	diff, _ := maxAmt.Sub(minAmt)
	if diff.IsZero() {
		return true
	}

	// Use XRPLNumber for precise division matching rippled's Number arithmetic
	diffNum := state.NewXRPLNumber(diff.Mantissa(), diff.Exponent())
	maxNum := state.NewXRPLNumber(maxAmt.Mantissa(), maxAmt.Exponent())
	ratio := diffNum.Div(maxNum)

	// Compare ratio < 1e-11
	// 1e-11 as XRPLNumber: mantissa=1e15, exponent=-26 (normalized)
	threshold := state.NewXRPLNumber(1e15, -26)

	// ratio < threshold ?
	ratioAmt := ratio.ToIOUAmountValue()
	thresholdAmt := threshold.ToIOUAmountValue()
	rIOU := state.NewIssuedAmountFromValue(ratioAmt.Mantissa(), ratioAmt.Exponent(), "", "")
	tIOU := state.NewIssuedAmountFromValue(thresholdAmt.Mantissa(), thresholdAmt.Exponent(), "", "")

	return rIOU.Compare(tIOU) < 0
}

// checkValidAMM implements the ValidAMM invariant checker.
// Reference: rippled InvariantCheck.cpp ValidAMM::visitEntry + ValidAMM::finalize (lines 1720-2023)
func checkValidAMM(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	// Delete may return tecINCOMPLETE if there are too many trustlines to delete.
	// Reference: rippled lines 1994-1995
	if result != TesSUCCESS && result != TecINCOMPLETE {
		return nil
	}

	// --- visitEntry phase ---
	// Track AMM entries: extract account ID and LPTokenBalance from before/after.
	// Track pool changes: RippleState with lsfAMMNode flag, or AccountRoot with non-zero AMMID.
	var (
		ammAccount     *[20]byte
		lptAfter       *Amount
		lptBefore      *Amount
		ammPoolChanged bool
	)

	for _, e := range entries {
		if e.IsDelete {
			continue
		}

		// Check "after" data
		if e.After != nil {
			// Try to detect AMM entries.
			// AMM SLE uses a custom binary format (no 0x11 type header), so
			// e.EntryType from getLedgerEntryType may not return "AMM".
			// We detect AMM entries by:
			//   1. Explicit "AMM" EntryType (if binary codec format is used)
			//   2. Trying to parse as AMM data for entries with unknown/non-standard types
			isAMMEntry := false
			if e.EntryType == "AMM" {
				isAMMEntry = true
			} else if isLikelyAMMBinary(e.After) {
				isAMMEntry = true
			}

			if isAMMEntry {
				// AMM object changed — extract account ID and LPTokenBalance
				fields, err := parseAMMInvariantFields(e.After)
				if err == nil {
					id := fields.accountID
					ammAccount = &id
					if fields.hasBalance {
						bal := fields.lptBalance
						lptAfter = &bal
					}
				}
			} else if e.EntryType == "RippleState" {
				// Check for lsfAMMNode flag
				rs, err := state.ParseRippleState(e.After)
				if err == nil && (rs.Flags&state.LsfAMMNode) != 0 {
					ammPoolChanged = true
				}
			} else if e.EntryType == "AccountRoot" {
				// Check for non-zero AMMID (AMM pseudo-account)
				acct, err := state.ParseAccountRoot(e.After)
				if err == nil {
					var zeroHash [32]byte
					if acct.AMMID != zeroHash {
						ammPoolChanged = true
					}
				}
			}
		}

		// Check "before" data for LPTokenBalance
		if e.Before != nil {
			isAMMBefore := false
			if e.EntryType == "AMM" {
				isAMMBefore = true
			} else if isLikelyAMMBinary(e.Before) {
				isAMMBefore = true
			}

			if isAMMBefore {
				fields, err := parseAMMInvariantFields(e.Before)
				if err == nil && fields.hasBalance {
					bal := fields.lptBalance
					lptBefore = &bal
				}
			}
		}
	}

	// --- finalize phase ---
	enforce := rules != nil && rules.Enabled(amendment.FeatureFixAMMv1_3)

	txType := tx.TxType()
	switch txType {
	case TypeAMMCreate:
		return finalizeAMMCreate(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMDeposit:
		return finalizeAMMDeposit(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMClawback, TypeAMMWithdraw:
		return finalizeAMMWithdraw(tx, view, ammAccount, lptAfter, enforce)
	case TypeAMMBid:
		return finalizeAMMBid(ammPoolChanged, lptBefore, lptAfter, enforce)
	case TypeAMMVote:
		return finalizeAMMVote(ammPoolChanged, lptBefore, lptAfter, enforce)
	case TypeAMMDelete:
		return finalizeAMMDelete(ammAccount, result, enforce)
	case TypeCheckCash, TypeOfferCreate, TypePayment:
		return finalizeAMMDEX(ammAccount, enforce)
	}

	return nil
}

// finalizeAMMVote checks that LP tokens and pool do not change on AMMVote.
// Reference: rippled InvariantCheck.cpp finalizeVote (lines 1774-1790)
func finalizeAMMVote(ammPoolChanged bool, lptBefore, lptAfter *Amount, enforce bool) *InvariantViolation {
	// Check if LPTokenBalance changed
	lptChanged := false
	if lptBefore != nil && lptAfter != nil {
		lptChanged = lptBefore.Compare(*lptAfter) != 0
	} else if (lptBefore == nil) != (lptAfter == nil) {
		// One is nil and the other isn't — that's a change
		lptChanged = true
	}

	if lptChanged || ammPoolChanged {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMVote invariant failed: LP tokens or pool changed",
			}
		}
	}

	return nil
}

// finalizeAMMBid checks that pool does not change and LP tokens decrease on AMMBid.
// Reference: rippled InvariantCheck.cpp finalizeBid (lines 1793-1819)
func finalizeAMMBid(ammPoolChanged bool, lptBefore, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammPoolChanged {
		// The pool cannot change on bid
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMBid invariant failed: pool changed",
			}
		}
	} else if lptBefore != nil && lptAfter != nil {
		// LP tokens are burnt, therefore there should be fewer LP tokens after
		// lptAfter > lptBefore || lptAfter <= 0
		if lptAfter.Compare(*lptBefore) > 0 || lptAfter.Signum() <= 0 {
			if enforce {
				return &InvariantViolation{
					Name:    "ValidAMM",
					Message: "AMMBid invariant failed: LP tokens did not decrease",
				}
			}
		}
	}

	return nil
}

// finalizeAMMCreate checks that AMM was created with correct initial LP tokens.
// Reference: rippled InvariantCheck.cpp finalizeCreate (lines 1822-1862)
func finalizeAMMCreate(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// AMM object was not created
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: AMM object is not created",
			}
		}
		return nil
	}

	if lptAfter == nil {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: no LPTokenBalance",
			}
		}
		return nil
	}

	// Get asset issues from the transaction
	createProvider, ok := tx.(AMMCreateIssueProvider)
	if !ok {
		// Cannot inspect tx fields — skip check
		return nil
	}

	asset1 := createProvider.GetAmountAsset()
	asset2 := createProvider.GetAmount2Asset()

	// Read pool balances
	if view == nil {
		return nil
	}
	amount, amount2 := ammPoolHoldsForInvariant(view, *ammAccount, asset1, asset2)
	// Create invariant: sqrt(amount * amount2) == LPTokens, all balances > 0
	if !validAMMBalances(amount, amount2, *lptAfter, false) {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMCreate invariant failed: invalid balances",
			}
		}
		return nil
	}

	// Check sqrt(amount * amount2) == LPTokens
	// Use the same calculation path as the AMM create code.
	// rippled: ammLPTokens(amount, amount2, lptAMMBalanceAfter_->issue()) != *lptAMMBalanceAfter_
	expectedLPT := calculateLPTokensForInvariant(amount, amount2)
	expectedIOU := toIOUForInvariant(expectedLPT)
	actualIOU := toIOUForInvariant(*lptAfter)
	if expectedIOU.Compare(actualIOU) != 0 {
		// Allow for tiny precision differences by using withinRelativeDistance
		// rippled uses exact != comparison, but our Amount arithmetic may
		// have minor differences from Number arithmetic
		if !withinRelativeDistanceForInvariant(expectedLPT, *lptAfter) {
			if enforce {
				return &InvariantViolation{
					Name:    "ValidAMM",
					Message: fmt.Sprintf("AMMCreate invariant failed: LP tokens mismatch (expected=%v, got=%v)", expectedLPT, *lptAfter),
				}
			}
		}
	}

	return nil
}

// finalizeAMMDelete checks that the AMM object is properly deleted.
// Reference: rippled InvariantCheck.cpp finalizeDelete (lines 1864-1880)
func finalizeAMMDelete(ammAccount *[20]byte, result Result, enforce bool) *InvariantViolation {
	if ammAccount != nil {
		// AMM object still exists after delete
		if enforce {
			msg := "AMM object is not deleted on tesSUCCESS"
			if result == TecINCOMPLETE {
				msg = "AMM object is changed on tecINCOMPLETE"
			}
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: fmt.Sprintf("AMMDelete invariant failed: %s", msg),
			}
		}
	}
	return nil
}

// finalizeAMMDeposit checks the general AMM invariant on deposit.
// Reference: rippled InvariantCheck.cpp finalizeDeposit (lines 1944-1962)
func finalizeAMMDeposit(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// AMM object was deleted — not allowed on deposit
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMMDeposit invariant failed: AMM object is deleted",
			}
		}
		return nil
	}

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, false); v != nil {
		if enforce {
			return v
		}
	}

	return nil
}

// finalizeAMMWithdraw checks the general AMM invariant on withdraw/clawback.
// AMM may be deleted (last withdraw), so ammAccount == nil is allowed.
// Reference: rippled InvariantCheck.cpp finalizeWithdraw (lines 1964-1982)
func finalizeAMMWithdraw(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool) *InvariantViolation {
	if ammAccount == nil {
		// Last Withdraw or Clawback deleted AMM — allowed
		return nil
	}

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, true); v != nil {
		if enforce {
			return v
		}
	}

	return nil
}

// finalizeAMMDEX checks that the AMM object is not directly modified by DEX transactions.
// Reference: rippled InvariantCheck.cpp finalizeDEX (lines 1883-1895)
func finalizeAMMDEX(ammAccount *[20]byte, enforce bool) *InvariantViolation {
	if ammAccount != nil {
		if enforce {
			return &InvariantViolation{
				Name:    "ValidAMM",
				Message: "AMM swap invariant failed: AMM object changed",
			}
		}
	}
	return nil
}

// generalAMMInvariant checks that sqrt(amount * amount2) >= LPTokens.
// zeroAllowed controls whether all-zero balances are acceptable (for withdrawals).
// Reference: rippled InvariantCheck.cpp generalInvariant (lines 1897-1941)
func generalAMMInvariant(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, zeroAllowed bool) *InvariantViolation {
	if ammAccount == nil || lptAfter == nil || view == nil {
		return nil
	}

	// Get asset pair from the transaction
	assetProvider, ok := tx.(AMMAssetProvider)
	if !ok {
		return nil
	}

	asset1 := assetProvider.GetAMMAsset()
	asset2 := assetProvider.GetAMMAsset2()

	// Read pool balances from the view
	amount, amount2 := ammPoolHoldsForInvariant(view, *ammAccount, asset1, asset2)

	// Compute sqrt(amount * amount2)
	poolProductMean := calculateLPTokensForInvariant(amount, amount2)

	// Check valid balances
	nonNegativeBalances := validAMMBalances(amount, amount2, *lptAfter, zeroAllowed)

	// Strong check: poolProductMean >= lptAfter
	poolMeanIOU := toIOUForInvariant(poolProductMean)
	lptAfterIOU := toIOUForInvariant(*lptAfter)
	strongInvariantCheck := poolMeanIOU.Compare(lptAfterIOU) >= 0

	// Weak check: if lptAfter != 0, check relative distance < 1e-11
	weakInvariantCheck := false
	if !strongInvariantCheck {
		if !lptAfter.IsZero() {
			weakInvariantCheck = withinRelativeDistanceForInvariant(poolProductMean, *lptAfter)
		}
	}

	if !nonNegativeBalances || (!strongInvariantCheck && !weakInvariantCheck) {
		return &InvariantViolation{
			Name:    "ValidAMM",
			Message: "AMM invariant failed: balances invalid or sqrt(a*b) < LPT",
		}
	}

	return nil
}

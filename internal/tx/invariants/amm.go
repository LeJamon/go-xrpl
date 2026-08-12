package invariants

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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
}

// parseAMMInvariantFields extracts the Account ID and LPTokenBalance from
// binary AMM SLE data. AMM data is stored in the standard binary codec format,
// so we decode it via binarycodec.Decode.
func parseAMMInvariantFields(data []byte) (*ammInvariantFields, error) {
	hexStr := hex.EncodeToString(data)
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM binary: %w", err)
	}

	result := &ammInvariantFields{}

	// Account and LPTokenBalance are both soeREQUIRED on the AMM ledger object
	// (rippled ledger_entries.macro:387,391). ValidAMM::visitEntry reads them
	// with getAccountID(sfAccount)/getFieldAmount(sfLPTokenBalance), which throw
	// when the field is absent — ApplyContext's catch-all converts that to
	// tecINVARIANT_FAILED. A successful decode missing either field is a
	// serialization round-trip bug, so fail rather than default to zero.
	acctStr, ok := fields["Account"].(string)
	if !ok {
		return nil, fmt.Errorf("AMM SLE missing required Account field")
	}
	id, err := state.DecodeAccountID(acctStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode AMM Account ID: %w", err)
	}
	result.accountID = id

	lptObj, ok := fields["LPTokenBalance"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("AMM SLE missing required LPTokenBalance field")
	}
	valueStr, _ := lptObj["value"].(string)
	currency, _ := lptObj["currency"].(string)
	issuer, _ := lptObj["issuer"].(string)
	lptBalance, err := state.NewIssuedAmountFromDecimalString(valueStr, currency, issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to parse AMM LPTokenBalance: %w", err)
	}
	result.lptBalance = lptBalance

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
	if asset.IsMPT() {
		var id [24]byte
		decoded, err := hex.DecodeString(asset.MPTIssuanceID)
		if err != nil || len(decoded) != len(id) {
			return state.NewMPTAmountWithIssuanceID(0, "", asset.MPTIssuanceID)
		}
		copy(id[:], decoded)
		var issuerID [20]byte
		copy(issuerID[:], id[4:])
		issuer := state.EncodeAccountIDSafe(issuerID)
		data, err := view.Read(keylet.MPTokenByID(id, ammAccountID))
		if err != nil || data == nil {
			return state.NewMPTAmountWithIssuanceID(0, issuer, asset.MPTIssuanceID)
		}
		token, err := state.ParseMPToken(data)
		if err != nil {
			return state.NewMPTAmountWithIssuanceID(0, issuer, asset.MPTIssuanceID)
		}
		return state.NewMPTAmountWithIssuanceID(int64(token.MPTAmount), issuer, asset.MPTIssuanceID)
	}
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
	ammIsLow := state.CompareAccountIDs(ammAccountID, issuerID) < 0
	balance := rs.Balance
	if !ammIsLow {
		balance = balance.Negate()
	}

	if balance.Signum() <= 0 {
		return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.Mantissa(), balance.Exponent(), asset.Currency, asset.Issuer)
}

func calculateLPTokenNumberForInvariant(
	amount1, amount2 Amount,
	ctx state.NumberContext,
	mode state.RoundingMode,
) state.XRPLNumber {
	if amount1.IsZero() || amount2.IsZero() {
		return ctx.Int(0)
	}
	product := ctx.FromAmount(amount1, mode).MulRounded(ctx.FromAmount(amount2, mode), mode)
	return product.Root2Rounded(mode)
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

// withinRelativeDistanceForInvariant checks whether two Numbers differ by less
// than 1e-11 relative to the larger value.
// Reference: rippled AMMHelpers.h withinRelativeDistance (lines 156-162)
func withinRelativeDistanceForInvariant(
	ctx state.NumberContext,
	calcNumber, reqNumber state.XRPLNumber,
) bool {
	if calcNumber.Equal(reqNumber) {
		return true
	}

	var minNumber, maxNumber state.XRPLNumber
	if calcNumber.Cmp(reqNumber) < 0 {
		minNumber = calcNumber
		maxNumber = reqNumber
	} else {
		minNumber = reqNumber
		maxNumber = calcNumber
	}

	diff := maxNumber.AddRounded(minNumber.Negate(), state.RoundToNearest)
	ratio := diff.DivRounded(maxNumber, state.RoundToNearest)
	return ratio.Cmp(ctx.Number(1, -11, state.RoundToNearest)) < 0
}

// ammParseViolation reports a failure to decode an entry already identified as
// an AMM SLE. The bytes were serialized by go-xrpl moments earlier, so a decode
// failure is a serialization round-trip bug that must fail the invariant.
func ammParseViolation(err error) *InvariantViolation {
	return &InvariantViolation{
		Name:    "ValidAMM",
		Message: fmt.Sprintf("could not parse AMM SLE: %v", err),
	}
}

// checkValidAMM implements the ValidAMM invariant checker.
// Reference: rippled InvariantCheck.cpp ValidAMM::visitEntry + ValidAMM::finalize (lines 1720-2023)
func checkValidAMM(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules, numberContexts ...state.NumberContext) *InvariantViolation {
	// Delete may return tecINCOMPLETE if there are too many trustlines to delete.
	// Reference: rippled lines 1994-1995
	if result != TesSUCCESS && result != TecINCOMPLETE {
		return nil
	}

	// --- visitEntry phase ---
	// Track AMM entries: extract account ID and LPTokenBalance from before/after.
	// Track pool changes: RippleState with lsfAMMNode, AccountRoot with AMMID,
	// or MPToken with lsfMPTAMM.
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
			if e.EntryType == entry.TypeAMM {
				// AMM object changed — extract account ID and LPTokenBalance.
				// A decode failure of an entry we identified as an AMM SLE is a
				// serialization round-trip bug and fails the invariant outright,
				// regardless of fixAMMv1_3: rippled's visitEntry catch-all is not
				// amendment-gated.
				fields, err := parseAMMInvariantFields(e.After)
				if err != nil {
					return ammParseViolation(err)
				}
				id := fields.accountID
				ammAccount = &id
				bal := fields.lptBalance
				lptAfter = &bal
			} else if e.EntryType == entry.TypeRippleState {
				// Check for lsfAMMNode flag
				rs, err := state.ParseRippleState(e.After)
				if err == nil && (rs.Flags&state.LsfAMMNode) != 0 {
					ammPoolChanged = true
				}
			} else if e.EntryType == entry.TypeAccountRoot {
				// Check for non-zero AMMID (AMM pseudo-account)
				acct, err := state.ParseAccountRoot(e.After)
				if err == nil {
					var zeroHash [32]byte
					if acct.AMMID != zeroHash {
						ammPoolChanged = true
					}
				}
			} else if e.EntryType == entry.TypeMPToken {
				token, err := state.ParseMPToken(e.After)
				if err == nil && token.Flags&entry.LsfMPTAMM != 0 {
					ammPoolChanged = true
				}
			}
		}

		// Check "before" data for LPTokenBalance
		if e.Before != nil && e.EntryType == entry.TypeAMM {
			fields, err := parseAMMInvariantFields(e.Before)
			if err != nil {
				return ammParseViolation(err)
			}
			bal := fields.lptBalance
			lptBefore = &bal
		}
	}

	// --- finalize phase ---
	enforce := rules != nil && rules.Enabled(amendment.FeatureFixAMMv1_3)
	numberContext := txcore.NumberContextForRules(rules)
	if len(numberContexts) > 0 {
		numberContext = numberContexts[0]
	}

	txType := tx.TxType()
	switch txType {
	case TypeAMMCreate:
		return finalizeAMMCreate(tx, view, ammAccount, lptAfter, enforce, numberContext)
	case TypeAMMDeposit:
		return finalizeAMMDeposit(tx, view, ammAccount, lptAfter, enforce, numberContext)
	case TypeAMMClawback, TypeAMMWithdraw:
		return finalizeAMMWithdraw(tx, view, ammAccount, lptAfter, enforce, numberContext)
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

// lptBalanceChanged mirrors rippled's std::optional<STAmount> operator!= for the
// AMM LP token balance: a difference in presence, numeric value, OR issue
// (currency/issuer) is a change. STAmount equality compares the issue, so a
// value-only Compare would miss an LP-token issue change.
// Reference: rippled InvariantCheck.cpp:1776
func lptBalanceChanged(before, after *Amount) bool {
	if (before == nil) != (after == nil) {
		return true
	}
	if before == nil {
		return false
	}
	return before.Compare(*after) != 0 ||
		before.Native != after.Native ||
		before.Currency != after.Currency ||
		before.Issuer != after.Issuer
}

// finalizeAMMVote checks that LP tokens and pool do not change on AMMVote.
// Reference: rippled InvariantCheck.cpp finalizeVote (lines 1774-1790)
func finalizeAMMVote(ammPoolChanged bool, lptBefore, lptAfter *Amount, enforce bool) *InvariantViolation {
	if lptBalanceChanged(lptBefore, lptAfter) || ammPoolChanged {
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
func finalizeAMMCreate(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool, ctx state.NumberContext) *InvariantViolation {
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

	mode := state.RoundToNearest
	if enforce {
		mode = state.RoundDownward
	}
	expectedNumber := calculateLPTokenNumberForInvariant(amount, amount2, ctx, mode)
	expectedLPT := ctx.ToAmount(
		expectedNumber,
		state.NewIssuedAmountFromValue(0, -100, lptAfter.Currency, lptAfter.Issuer),
		mode,
	)
	if expectedLPT.Compare(*lptAfter) != 0 && enforce {
		return &InvariantViolation{
			Name:    "ValidAMM",
			Message: fmt.Sprintf("AMMCreate invariant failed: LP tokens mismatch (expected=%v, got=%v)", expectedLPT, *lptAfter),
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
func finalizeAMMDeposit(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool, ctx state.NumberContext) *InvariantViolation {
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

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, false, ctx); v != nil {
		if enforce {
			return v
		}
	}

	return nil
}

// finalizeAMMWithdraw checks the general AMM invariant on withdraw/clawback.
// AMM may be deleted (last withdraw), so ammAccount == nil is allowed.
// Reference: rippled InvariantCheck.cpp finalizeWithdraw (lines 1964-1982)
func finalizeAMMWithdraw(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, enforce bool, ctx state.NumberContext) *InvariantViolation {
	if ammAccount == nil {
		// Last Withdraw or Clawback deleted AMM — allowed
		return nil
	}

	if v := generalAMMInvariant(tx, view, ammAccount, lptAfter, true, ctx); v != nil {
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
func generalAMMInvariant(tx Transaction, view ReadView, ammAccount *[20]byte, lptAfter *Amount, zeroAllowed bool, ctx state.NumberContext) *InvariantViolation {
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

	poolProductMean := calculateLPTokenNumberForInvariant(amount, amount2, ctx, state.RoundToNearest)
	lptAfterNumber := ctx.FromAmount(*lptAfter, state.RoundToNearest)

	// Check valid balances
	nonNegativeBalances := validAMMBalances(amount, amount2, *lptAfter, zeroAllowed)

	// Strong check: poolProductMean >= lptAfter
	strongInvariantCheck := poolProductMean.Cmp(lptAfterNumber) >= 0

	// Weak check: if lptAfter != 0, check relative distance < 1e-11
	weakInvariantCheck := false
	if !strongInvariantCheck {
		if !lptAfter.IsZero() {
			weakInvariantCheck = withinRelativeDistanceForInvariant(ctx, poolProductMean, lptAfterNumber)
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

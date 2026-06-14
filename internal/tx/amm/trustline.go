package amm

import (
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// alignToBalance retags delta with balance's currency/issuer so it can be added
// to a trust-line balance under the strict Amount.Add currency guard. rippled's
// rippleCredit derives the issue from the trust line, not the passed amount;
// delta carries only the magnitude to credit (it may be the unitless result of
// AMM Number math). This mirrors the way each caller already re-tags the summed
// result to the line's currency, so the value is unchanged.
func alignToBalance(balance, delta tx.Amount) tx.Amount {
	if balance.IsNative() || delta.IsNative() {
		return delta
	}
	return state.NewIssuedAmountFromValue(delta.Mantissa(), delta.Exponent(),
		balance.Currency, balance.Issuer)
}

// trustCreateOpts parameterises the differences between the AMM, LP-token, and
// withdraw trust-line creation paths.
type trustCreateOpts struct {
	// setAMMNode tags the line as AMM-owned (lsfAMMNode). Only the AMM↔issuer
	// pool line sets it.
	setAMMNode bool
	// setNoRipple sets each side's NoRipple flag when that side's account lacks
	// lsfDefaultRipple. The LP-token and withdraw lines set it; the AMM pool line
	// does not.
	setNoRipple bool
}

// trustCreate creates a new RippleState trust line holding `amount` for the
// receiver (the token holder) against the counterparty (the issuer or AMM),
// delegating to the shared tx.TrustCreate. The receiver is the account being
// set: it carries the reserve flag and (for the LP-token/withdraw lines) the
// DefaultRipple-derived NoRipple. The AMM↔issuer pool line passes AMMNode. The
// shared helper always sets the peer side's NoRipple when that side lacks
// DefaultRipple, matching rippled's trustCreate. Owner-count bumps stay with the
// caller, as before.
func trustCreate(view tx.LedgerView, receiverID, counterpartyID [20]byte, currency string, amount tx.Amount, opts trustCreateOpts) tx.Result {
	receiverStr, err := state.EncodeAccountID(receiverID)
	if err != nil {
		return tx.TefINTERNAL
	}

	return tx.TrustCreate(view, tx.TrustCreateParams{
		SrcHigh:     keylet.IsLowAccount(receiverID, counterpartyID),
		Src:         counterpartyID,
		Dst:         receiverID,
		LineKey:     keylet.Line(receiverID, counterpartyID, currency),
		LimitIssuer: receiverID,
		NoRipple:    opts.setNoRipple && !accountHasDefaultRipple(view, receiverID),
		AMMNode:     opts.setAMMNode,
		Balance:     state.NewIssuedAmountFromValue(amount.Mantissa(), amount.Exponent(), currency, state.AccountOneAddress),
		Limit:       tx.NewIssuedAmount(0, state.MinExponent, currency, receiverStr),
	})
}

// trustDelete removes a trust line from the low and high owner directories and
// erases it, mirroring rippled's trustDelete. Owner-count adjustments are the
// caller's responsibility. It returns the first DirRemove error, if any, before
// erasing — callers that must ignore directory errors can discard the result.
func trustDelete(view tx.LedgerView, lineKey keylet.Keylet, lowID, highID [20]byte, lowNode, highNode uint64) error {
	if _, err := state.DirRemove(view, keylet.OwnerDir(lowID), lowNode, lineKey.Key, false); err != nil {
		return err
	}
	if _, err := state.DirRemove(view, keylet.OwnerDir(highID), highNode, lineKey.Key, false); err != nil {
		return err
	}
	return view.Erase(lineKey)
}

// accountHasDefaultRipple reports whether an account has lsfDefaultRipple set.
// A missing or unparseable account reads as not-set, matching the create paths'
// defensive defaulting.
func accountHasDefaultRipple(view tx.LedgerView, accountID [20]byte) bool {
	data, err := view.Read(keylet.Account(accountID))
	if err != nil || data == nil {
		return false
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return false
	}
	return (account.Flags & state.LsfDefaultRipple) != 0
}

// updateTrustlineBalanceResult holds the result of a trust line balance update,
// including any owner count adjustments that the caller must apply.
type updateTrustlineBalanceResult struct {
	// SenderOwnerCountDelta is the change to the sender's owner count (-1 if reserve cleared, 0 otherwise)
	SenderOwnerCountDelta int
	// IssuerOwnerCountDelta is the change to the issuer's owner count (-1 if reserve cleared, 0 otherwise)
	IssuerOwnerCountDelta int
}

// createOrUpdateAMMTrustline creates or updates a trust line for an AMM asset.
// This creates the trustline between the AMM account and the asset issuer,
// following rippled's trustCreate logic.
// Reference: rippled View.cpp trustCreate lines 1329-1445
func createOrUpdateAMMTrustline(ammAccountID [20]byte, asset tx.Asset, amount tx.Amount, view tx.LedgerView) error {
	// XRP doesn't need a trustline
	if isXRPAsset(asset) {
		return nil
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return err
	}

	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)

	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		// Reference: rippled rippleCreditIOU lines 1668-1748
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		ammIsLow := keylet.IsLowAccount(ammAccountID, issuerID)

		// Update balance - positive balance means low owes high
		// AMM is receiving tokens from issuer (or being credited), so:
		// If AMM is low: balance should increase (AMM holds more)
		// If AMM is high: balance should decrease (AMM holds more, from their perspective)
		currentBalance := rs.Balance
		var newBalance tx.Amount

		delta := alignToBalance(currentBalance, amount)
		if ammIsLow {
			// AMM is low - positive balance means AMM holds tokens
			newBalance, err = currentBalance.Add(delta)
			if err != nil {
				return err
			}
		} else {
			// AMM is high - negative balance means AMM holds tokens
			newBalance, err = currentBalance.Sub(delta)
			if err != nil {
				return err
			}
		}

		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		// Ensure lsfAMMNode flag is set (for AMM-owned trustlines)
		rs.Flags |= state.LsfAMMNode

		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create the AMM-owned line.
	if result := trustCreate(view, ammAccountID, issuerID, asset.Currency, amount, trustCreateOpts{setAMMNode: true}); result != tx.TesSUCCESS {
		return errors.New("trust create failed")
	}
	return nil
}

// updateTrustlineBalanceInView updates the balance of a trust line for IOU transfers.
// This reads the trust line, modifies the balance, and writes it back.
// delta is the amount to add (positive) or subtract (negative) from the account's perspective.
func updateTrustlineBalanceInView(accountID [20]byte, issuerID [20]byte, currency string, delta tx.Amount, view tx.LedgerView) error {
	result, err := updateTrustlineBalanceInViewEx(accountID, issuerID, currency, delta, view)
	_ = result
	return err
}

// updateTrustlineBalanceInViewEx updates a trust line balance and handles reserve
// clearing and trust line deletion when the balance goes to zero.
// It does NOT modify AccountRoots — the caller must apply the returned owner
// count deltas to the appropriate accounts.
// Reference: rippled View.cpp updateTrustLine + redeemIOU/issueIOU
func updateTrustlineBalanceInViewEx(accountID [20]byte, issuerID [20]byte, currency string, delta tx.Amount, view tx.LedgerView) (updateTrustlineBalanceResult, error) {
	var result updateTrustlineBalanceResult

	lineKey := keylet.Line(accountID, issuerID, currency)

	exists, err := view.Exists(lineKey)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, errors.New("trust line does not exist")
	}

	data, err := view.Read(lineKey)
	if err != nil {
		return result, err
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return result, err
	}

	// Determine if sender (accountID) is low or high
	senderIsLow := keylet.IsLowAccount(accountID, issuerID)

	// Get balance from sender's perspective
	beforeBalance := rs.Balance
	if !senderIsLow {
		beforeBalance = beforeBalance.Negate()
	}

	afterBalance, err := beforeBalance.Add(alignToBalance(beforeBalance, delta))
	if err != nil {
		return result, err
	}

	// Convert back to RippleState balance convention
	newBalance := afterBalance
	if !senderIsLow {
		newBalance = newBalance.Negate()
	}

	rs.Balance = state.NewIssuedAmountFromValue(
		newBalance.Mantissa(), newBalance.Exponent(),
		rs.Balance.Currency, rs.Balance.Issuer,
	)

	// --- updateTrustLine logic (rippled View.cpp lines 2135-2185) ---
	// Check if sender's reserve should be cleared when balance transitions
	// from positive to zero/negative.
	uFlags := rs.Flags
	bDelete := false

	var senderReserveFlag, senderNoRippleFlag, senderFreezeFlag uint32
	var senderLimit tx.Amount
	var senderQualityIn, senderQualityOut uint32
	if senderIsLow {
		senderReserveFlag = state.LsfLowReserve
		senderNoRippleFlag = state.LsfLowNoRipple
		senderFreezeFlag = state.LsfLowFreeze
		senderLimit = rs.LowLimit
		senderQualityIn = rs.LowQualityIn
		senderQualityOut = rs.LowQualityOut
	} else {
		senderReserveFlag = state.LsfHighReserve
		senderNoRippleFlag = state.LsfHighNoRipple
		senderFreezeFlag = state.LsfHighFreeze
		senderLimit = rs.HighLimit
		senderQualityIn = rs.HighQualityIn
		senderQualityOut = rs.HighQualityOut
	}

	if beforeBalance.Signum() > 0 && afterBalance.Signum() <= 0 &&
		(uFlags&senderReserveFlag) != 0 {
		// Read sender's DefaultRipple flag
		senderDefaultRipple := false
		if senderData, readErr := view.Read(keylet.Account(accountID)); readErr == nil && senderData != nil {
			if senderAcct, parseErr := state.ParseAccountRoot(senderData); parseErr == nil {
				senderDefaultRipple = (senderAcct.Flags & state.LsfDefaultRipple) != 0
			}
		}

		senderNoRipple := (uFlags & senderNoRippleFlag) != 0
		senderFrozen := (uFlags & senderFreezeFlag) != 0

		if senderNoRipple != senderDefaultRipple &&
			!senderFrozen &&
			senderLimit.IsZero() &&
			senderQualityIn == 0 &&
			senderQualityOut == 0 {
			result.SenderOwnerCountDelta = -1
			rs.Flags &^= senderReserveFlag

			// Check deletion: balance is zero AND receiver has no reserve
			var receiverReserveFlag uint32
			if senderIsLow {
				receiverReserveFlag = state.LsfHighReserve
			} else {
				receiverReserveFlag = state.LsfLowReserve
			}
			if afterBalance.Signum() == 0 && (rs.Flags&receiverReserveFlag) == 0 {
				bDelete = true
			}
		}
	}

	if bDelete {
		lowAccountID, highAccountID := issuerID, accountID
		if senderIsLow {
			lowAccountID, highAccountID = accountID, issuerID
		}

		err := trustDelete(view, lineKey, lowAccountID, highAccountID, rs.LowNode, rs.HighNode)

		// Check issuer's reserve for owner count delta
		var issuerReserveFlag uint32
		if senderIsLow {
			issuerReserveFlag = state.LsfHighReserve
		} else {
			issuerReserveFlag = state.LsfLowReserve
		}
		if (uFlags & issuerReserveFlag) != 0 {
			result.IssuerOwnerCountDelta = -1
		}

		return result, err
	}

	serialized, err := state.SerializeRippleState(rs)
	if err != nil {
		return result, err
	}

	return result, view.Update(lineKey, serialized)
}

// createLPTokenTrustline creates or updates a trust line for LP tokens.
// This creates the trustline between the depositor and the AMM account (LP token issuer).
// Reference: rippled View.cpp trustCreate
func createLPTokenTrustline(accountID [20]byte, lptAsset tx.Asset, amount tx.Amount, view tx.LedgerView) error {
	// LP token issuer is the AMM account
	ammAccountID, err := state.DecodeAccountID(lptAsset.Issuer)
	if err != nil {
		return err
	}

	trustLineKey := keylet.Line(accountID, ammAccountID, lptAsset.Currency)

	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		holderIsLow := keylet.IsLowAccount(accountID, ammAccountID)

		currentBalance := rs.Balance
		var newBalance tx.Amount

		delta := alignToBalance(currentBalance, amount)
		if holderIsLow {
			// Holder is low - positive balance means holder holds tokens
			newBalance, err = currentBalance.Add(delta)
			if err != nil {
				return err
			}
		} else {
			// Holder is high - negative balance means holder holds tokens
			newBalance, err = currentBalance.Sub(delta)
			if err != nil {
				return err
			}
		}

		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create the holder's LP token line. The holder
	// receives the tokens; NoRipple is set per each side's DefaultRipple flag.
	if result := trustCreate(view, accountID, ammAccountID, lptAsset.Currency, amount, trustCreateOpts{setNoRipple: true}); result != tx.TesSUCCESS {
		return errors.New("trust create failed")
	}
	return nil
}

// redeemIOUWithCleanup burns LP tokens from the holder's trust line, deleting
// the line when its balance reaches zero (matching rippled's redeemIOU +
// updateTrustLine + trustDelete flow).
// Reference: rippled View.cpp redeemIOU (line 2288)
func redeemIOUWithCleanup(view tx.LedgerView, holderID, ammAccountID [20]byte, amount tx.Amount) tx.Result {
	if amount.IsZero() {
		return tx.TesSUCCESS
	}

	trustLineKey := keylet.Line(holderID, ammAccountID, amount.Currency)
	data, err := view.Read(trustLineKey)
	if err != nil || data == nil {
		return tx.TefINTERNAL // LP token trust line must exist
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return tx.TefINTERNAL
	}

	holderHigh := !keylet.IsLowAccount(holderID, ammAccountID)

	// Get balance in holder terms (positive = holder holds tokens)
	saBalance := rs.Balance
	if holderHigh {
		saBalance = saBalance.Negate()
	}

	saBefore := saBalance
	// Holder is redeeming (sending back to AMM/issuer), so balance decreases
	saBalance, err = saBalance.Sub(amount)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Reference: rippled View.cpp updateTrustLine (line 2135) + redeemIOU (line 2323)
	bDelete := false
	rsFlags := rs.Flags

	var holderReserveFlag, holderNoRippleFlag, holderFreezeFlag uint32
	var holderLimitIsZero, holderQInIsZero, holderQOutIsZero bool
	if !holderHigh {
		holderReserveFlag = state.LsfLowReserve
		holderNoRippleFlag = state.LsfLowNoRipple
		holderFreezeFlag = state.LsfLowFreeze
		holderLimitIsZero = rs.LowLimit.IsZero()
		holderQInIsZero = rs.LowQualityIn == 0
		holderQOutIsZero = rs.LowQualityOut == 0
	} else {
		holderReserveFlag = state.LsfHighReserve
		holderNoRippleFlag = state.LsfHighNoRipple
		holderFreezeFlag = state.LsfHighFreeze
		holderLimitIsZero = rs.HighLimit.IsZero()
		holderQInIsZero = rs.HighQualityIn == 0
		holderQOutIsZero = rs.HighQualityOut == 0
	}

	isPositive := !saBefore.IsZero() && !saBefore.IsNegative()
	isZeroOrNeg := saBalance.IsZero() || saBalance.IsNegative()
	hasReserve := (rsFlags & holderReserveFlag) != 0

	holderAccountData, _ := view.Read(keylet.Account(holderID))
	holderAccount, _ := state.ParseAccountRoot(holderAccountData)

	holderHasDefaultRipple := false
	if holderAccount != nil {
		holderHasDefaultRipple = (holderAccount.Flags & state.LsfDefaultRipple) != 0
	}
	holderHasNoRipple := (rsFlags & holderNoRippleFlag) != 0
	holderHasFreeze := (rsFlags & holderFreezeFlag) != 0

	if isPositive && isZeroOrNeg && hasReserve &&
		(holderHasNoRipple != holderHasDefaultRipple) &&
		!holderHasFreeze &&
		holderLimitIsZero &&
		holderQInIsZero &&
		holderQOutIsZero {
		if holderAccount != nil && holderAccount.OwnerCount > 0 {
			holderAccount.OwnerCount--
			holderBytes, err := state.SerializeAccountRoot(holderAccount)
			if err != nil {
				return tx.TefINTERNAL
			}
			if err := view.Update(keylet.Account(holderID), holderBytes); err != nil {
				return tx.TefINTERNAL
			}
		}

		rsFlags &= ^holderReserveFlag

		var ammReserveFlag uint32
		if holderHigh {
			ammReserveFlag = state.LsfLowReserve
		} else {
			ammReserveFlag = state.LsfHighReserve
		}

		bDelete = saBalance.IsZero() && (rsFlags&ammReserveFlag) == 0
	}

	finalBalance := saBalance
	if holderHigh {
		finalBalance = finalBalance.Negate()
	}
	rs.Balance = state.NewIssuedAmountFromValue(
		finalBalance.Mantissa(), finalBalance.Exponent(),
		rs.Balance.Currency, rs.Balance.Issuer)
	rs.Flags = rsFlags

	if bDelete {
		return trustDeleteRippleState(view, trustLineKey, rs, holderID, ammAccountID, holderHigh)
	}

	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := view.Update(trustLineKey, rsBytes); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// trustDeleteRippleState removes a trust line from both owner directories and
// erases it. Reference: rippled View.cpp trustDelete (line 1534)
func trustDeleteRippleState(view tx.LedgerView, lineKey keylet.Keylet, rs *state.RippleState, id1, id2 [20]byte, id1IsHigh bool) tx.Result {
	lowID, highID := id1, id2
	if id1IsHigh {
		lowID, highID = id2, id1
	}
	if trustDelete(view, lineKey, lowID, highID, rs.LowNode, rs.HighNode) != nil {
		return tx.TefBAD_LEDGER
	}
	return tx.TesSUCCESS
}

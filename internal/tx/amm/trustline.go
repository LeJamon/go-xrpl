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

// updateTrustlineBalanceResult holds the result of a trust line balance update,
// including any owner count adjustments that the caller must apply.
type updateTrustlineBalanceResult struct {
	// SenderOwnerCountDelta is the change to the sender's owner count (-1 if reserve cleared, 0 otherwise)
	SenderOwnerCountDelta int
	// IssuerOwnerCountDelta is the change to the issuer's owner count (-1 if reserve cleared, 0 otherwise)
	IssuerOwnerCountDelta int
	// Deleted is true if the trust line was deleted (zero balance, no reserves on either side)
	Deleted bool
}

// createOrUpdateAMMTrustline creates or updates a trust line for an AMM asset.
// This creates the trustline between the AMM account and the asset issuer,
// following rippled's trustCreate logic.
// Reference: rippled View.cpp trustCreate lines 1329-1445
func createOrUpdateAMMTrustline(ammAccountID [20]byte, asset tx.Asset, amount tx.Amount, view tx.LedgerView) error {
	// XRP doesn't need a trustline
	if asset.Currency == "" || asset.Currency == "XRP" {
		return nil
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return err
	}

	// Get trustline keylet
	trustLineKey := keylet.Line(ammAccountID, issuerID, asset.Currency)

	// Check if trustline already exists
	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		// Trustline exists - update the balance
		// Reference: rippled rippleCreditIOU lines 1668-1748
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		// Determine if AMM is low or high account
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

		// Update balance preserving currency/issuer
		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		// Ensure lsfAMMNode flag is set (for AMM-owned trustlines)
		rs.Flags |= state.LsfAMMNode

		// Serialize and update
		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create it
	// Reference: rippled trustCreate lines 1347-1445

	// Determine low/high account ordering
	var lowAccountID, highAccountID [20]byte
	ammIsLow := keylet.IsLowAccount(ammAccountID, issuerID)
	if ammIsLow {
		lowAccountID = ammAccountID
		highAccountID = issuerID
	} else {
		lowAccountID = issuerID
		highAccountID = ammAccountID
	}

	lowAccountStr, _ := state.EncodeAccountID(lowAccountID)
	highAccountStr, _ := state.EncodeAccountID(highAccountID)

	// Create the RippleState entry
	// For AMM trustlines:
	// - Balance represents how much the low account "owes" the high account
	// - If AMM is low, positive balance = AMM holds tokens
	// - If AMM is high, negative balance = AMM holds tokens
	// - Balance issuer is always ACCOUNT_ONE (no account)
	var balance tx.Amount
	if ammIsLow {
		// AMM is low - positive balance
		balance = state.NewIssuedAmountFromValue(
			amount.Mantissa(),
			amount.Exponent(),
			asset.Currency,
			state.AccountOneAddress,
		)
	} else {
		// AMM is high - negative balance
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(
			negated.Mantissa(),
			negated.Exponent(),
			asset.Currency,
			state.AccountOneAddress,
		)
	}

	// Create RippleState
	// Reference: rippled trustCreate - limits are set based on who set the limit
	// For AMM trustlines, the limits are 0 on both sides (AMM doesn't set limits)
	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, asset.Currency, lowAccountStr),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, asset.Currency, highAccountStr),
		Flags:     0,
		LowNode:   0,
		HighNode:  0,
	}

	// Set reserve flag for the side that is NOT the issuer
	// Reference: rippled trustCreate line 1409
	// For AMM, the AMM account should have reserve set
	if ammIsLow {
		rs.Flags |= state.LsfLowReserve
	} else {
		rs.Flags |= state.LsfHighReserve
	}

	// Set lsfAMMNode flag - this identifies it as an AMM-owned trustline
	// Reference: rippled AMMCreate.cpp line 297-306
	rs.Flags |= state.LsfAMMNode

	// Insert into low account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into high account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set deletion hints (page numbers where the trustline is stored)
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert the trustline
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Insert(trustLineKey, rsBytes)
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
		result.Deleted = true
		var lowAccountID, highAccountID [20]byte
		if senderIsLow {
			lowAccountID = accountID
			highAccountID = issuerID
		} else {
			lowAccountID = issuerID
			highAccountID = accountID
		}

		lowDirKey := keylet.OwnerDir(lowAccountID)
		state.DirRemove(view, lowDirKey, rs.LowNode, lineKey.Key, false)

		highDirKey := keylet.OwnerDir(highAccountID)
		state.DirRemove(view, highDirKey, rs.HighNode, lineKey.Key, false)

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

		return result, view.Erase(lineKey)
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

	// Get trustline keylet
	trustLineKey := keylet.Line(accountID, ammAccountID, lptAsset.Currency)

	// Check if trustline already exists
	exists, err := view.Exists(trustLineKey)
	if err != nil {
		return err
	}

	if exists {
		// Trustline exists - update the balance
		data, err := view.Read(trustLineKey)
		if err != nil {
			return err
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return err
		}

		// Determine if holder is low or high account
		holderIsLow := keylet.IsLowAccount(accountID, ammAccountID)

		// Update balance - holder is receiving LP tokens
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

		// Update balance preserving currency/issuer
		rs.Balance = state.NewIssuedAmountFromValue(
			newBalance.Mantissa(),
			newBalance.Exponent(),
			rs.Balance.Currency,
			rs.Balance.Issuer,
		)

		// Serialize and update
		rsBytes, err := state.SerializeRippleState(rs)
		if err != nil {
			return err
		}

		return view.Update(trustLineKey, rsBytes)
	}

	// Trustline doesn't exist - create it

	// Determine low/high account ordering
	var lowAccountID, highAccountID [20]byte
	holderIsLow := keylet.IsLowAccount(accountID, ammAccountID)
	if holderIsLow {
		lowAccountID = accountID
		highAccountID = ammAccountID
	} else {
		lowAccountID = ammAccountID
		highAccountID = accountID
	}

	lowAccountStr, _ := state.EncodeAccountID(lowAccountID)
	highAccountStr, _ := state.EncodeAccountID(highAccountID)

	// Create balance - holder receives LP tokens
	var balance tx.Amount
	if holderIsLow {
		// Holder is low - positive balance
		balance = state.NewIssuedAmountFromValue(
			amount.Mantissa(),
			amount.Exponent(),
			lptAsset.Currency,
			state.AccountOneAddress,
		)
	} else {
		// Holder is high - negative balance
		negated := amount.Negate()
		balance = state.NewIssuedAmountFromValue(
			negated.Mantissa(),
			negated.Exponent(),
			lptAsset.Currency,
			state.AccountOneAddress,
		)
	}

	// Create RippleState
	// For LP token trustlines, the holder side gets reserve, AMM side doesn't
	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, lptAsset.Currency, lowAccountStr),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, lptAsset.Currency, highAccountStr),
		Flags:     0,
		LowNode:   0,
		HighNode:  0,
	}

	// Set reserve flag and NoRipple flags matching rippled's trustCreate + issueIOU.
	// Reference: rippled View.cpp trustCreate (lines 1415-1432) and issueIOU (line 2228-2240).
	// When creating a trust line, each side gets NoRipple set if that account
	// does NOT have the lsfDefaultRipple flag set.
	holderHasDefaultRipple := false
	if holderAccountData, readErr := view.Read(keylet.Account(accountID)); readErr == nil && holderAccountData != nil {
		if holderAcct, parseErr := state.ParseAccountRoot(holderAccountData); parseErr == nil {
			holderHasDefaultRipple = (holderAcct.Flags & state.LsfDefaultRipple) != 0
		}
	}
	ammHasDefaultRipple := false
	if ammAccountData, readErr := view.Read(keylet.Account(ammAccountID)); readErr == nil && ammAccountData != nil {
		if ammAcct, parseErr := state.ParseAccountRoot(ammAccountData); parseErr == nil {
			ammHasDefaultRipple = (ammAcct.Flags & state.LsfDefaultRipple) != 0
		}
	}

	if holderIsLow {
		rs.Flags |= state.LsfLowReserve
		if !holderHasDefaultRipple {
			rs.Flags |= state.LsfLowNoRipple
		}
		if !ammHasDefaultRipple {
			rs.Flags |= state.LsfHighNoRipple
		}
	} else {
		rs.Flags |= state.LsfHighReserve
		if !holderHasDefaultRipple {
			rs.Flags |= state.LsfHighNoRipple
		}
		if !ammHasDefaultRipple {
			rs.Flags |= state.LsfLowNoRipple
		}
	}

	// Insert into low account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(view, lowDirKey, trustLineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into high account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(view, highDirKey, trustLineKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set deletion hints
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	rsBytes, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	return view.Insert(trustLineKey, rsBytes)
}

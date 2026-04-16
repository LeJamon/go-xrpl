package payment

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func (s *BookStep) creditTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	lineKey := keylet.Line(account, issuer, amount.Currency)
	lineData, err := sb.Read(lineKey)
	if err != nil {
		return err
	}
	if lineData == nil {
		// Trust line doesn't exist — create one (offer crossing creates trust lines on demand).
		// Reference: rippled rippleCredit() → trustCreate() in View.cpp
		return s.trustCreateForCredit(sb, account, issuer, amount, txHash, ledgerSeq)
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return err
	}

	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0

	// Compute sender's (issuer's) balance BEFORE update, from issuer's perspective.
	// The sender here is 'issuer' (crediting account means issuer sends to account).
	// Reference: rippled rippleCreditIOU() line 1672-1675
	issuerIsLow := !accountIsLow
	var preCreditIssuerBalance tx.Amount
	if issuerIsLow {
		preCreditIssuerBalance = rs.Balance
	} else {
		preCreditIssuerBalance = rs.Balance.Negate()
	}
	// Record deferred credit: issuer (sender) → account (receiver).
	// Reference: rippled View.cpp rippleCreditIOU() line 1675:
	//   view.creditHook(uSenderID, uReceiverID, saAmount, saBalance)
	sb.CreditHook(issuer, account, amount, preCreditIssuerBalance)

	if accountIsLow {
		rs.Balance, _ = rs.Balance.Add(amount)
	} else {
		rs.Balance, _ = rs.Balance.Sub(amount)
	}

	rs.PreviousTxnID = txHash
	rs.PreviousTxnLgrSeq = ledgerSeq

	lineDataNew, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}
	return sb.Update(lineKey, lineDataNew)
}

// trustCreateForCredit creates a new trust line between account and issuer with initial balance.
// This is used when creditTrustline encounters a missing trust line during offer crossing.
// Reference: rippled View.cpp trustCreate() lines 1329-1445
func (s *BookStep) trustCreateForCredit(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	// Determine low and high accounts
	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0
	var lowAccountID, highAccountID [20]byte
	if accountIsLow {
		lowAccountID = account
		highAccountID = issuer
	} else {
		lowAccountID = issuer
		highAccountID = account
	}

	lowAccountStr := state.EncodeAccountIDSafe(lowAccountID)
	highAccountStr := state.EncodeAccountIDSafe(highAccountID)

	// Calculate the initial balance from low account's perspective
	// The issuer sends to account:
	// - If account is LOW: issuer (HIGH) pays account (LOW) → balance increases (positive)
	// - If account is HIGH: issuer (LOW) pays account (HIGH) → balance decreases (negative)
	var balance tx.Amount
	if accountIsLow {
		balance = amount // account is low, receives credit → positive balance
	} else {
		balance = amount.Negate() // account is high, receives credit → negative balance
	}

	// Check receiver account's DefaultRipple flag for NoRipple setting
	var noRipple bool
	accountKey := keylet.Account(account)
	accountData, err := sb.Read(accountKey)
	if err == nil && accountData != nil {
		acct, parseErr := state.ParseAccountRoot(accountData)
		if parseErr == nil {
			const lsfDefaultRipple = 0x00800000
			noRipple = (acct.Flags & lsfDefaultRipple) == 0
		}
	}

	// Build the trust line flags — set reserve flag for the receiver (account) side
	var flags uint32
	if accountIsLow {
		// account is LOW
		if noRipple {
			flags |= state.LsfLowNoRipple
		}
		flags |= state.LsfLowReserve
	} else {
		// account is HIGH
		if noRipple {
			flags |= state.LsfHighNoRipple
		}
		flags |= state.LsfHighReserve
	}

	// Create the RippleState
	rs := &state.RippleState{
		Balance:           tx.NewIssuedAmount(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, state.AccountOneAddress),
		LowLimit:          tx.NewIssuedAmount(0, -100, amount.Currency, lowAccountStr),
		HighLimit:         tx.NewIssuedAmount(0, -100, amount.Currency, highAccountStr),
		Flags:             flags,
		LowNode:           0,
		HighNode:          0,
		PreviousTxnID:     txHash,
		PreviousTxnLgrSeq: ledgerSeq,
	}

	lineKey := keylet.Line(account, issuer, amount.Currency)

	// Insert into LOW account's owner directory
	lowDirKey := keylet.OwnerDir(lowAccountID)
	lowDirResult, err := state.DirInsert(sb, lowDirKey, lineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = lowAccountID
	})
	if err != nil {
		return err
	}

	// Insert into HIGH account's owner directory
	highDirKey := keylet.OwnerDir(highAccountID)
	highDirResult, err := state.DirInsert(sb, highDirKey, lineKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = highAccountID
	})
	if err != nil {
		return err
	}

	// Set directory node hints
	rs.LowNode = lowDirResult.Page
	rs.HighNode = highDirResult.Page

	// Serialize and insert
	lineData, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if err := sb.Insert(lineKey, lineData); err != nil {
		return err
	}

	// Increment receiver's OwnerCount
	return s.adjustOwnerCountForTrustCreate(sb, account, 1, txHash, ledgerSeq)
}

// adjustOwnerCountForTrustCreate modifies an account's OwnerCount by delta during trust line creation.
func (s *BookStep) adjustOwnerCountForTrustCreate(sb *PaymentSandbox, account [20]byte, delta int32, txHash [32]byte, ledgerSeq uint32) error {
	// Read current owner count and record via hook before modifying.
	accountKey := keylet.Account(account)
	data, err := sb.Read(accountKey)
	if err == nil && data != nil {
		if acct, pErr := state.ParseAccountRoot(data); pErr == nil {
			curOC := acct.OwnerCount
			newOC := int(curOC) + int(delta)
			if newOC < 0 {
				newOC = 0
			}
			sb.AdjustOwnerCount(account, curOC, uint32(newOC))
		}
	}
	return tx.AdjustOwnerCountWithTx(sb, account, int(delta), txHash, ledgerSeq)
}

// debitTrustline decreases an account's IOU balance.
// After updating, checks if the trust line should be deleted (zero balance, auto-created).
// Reference: rippled View.cpp rippleCreditIOU() lines 1688-1745
func (s *BookStep) debitTrustline(sb *PaymentSandbox, account, issuer [20]byte, amount tx.Amount, txHash [32]byte, ledgerSeq uint32) error {
	lineKey := keylet.Line(account, issuer, amount.Currency)
	lineData, err := sb.Read(lineKey)
	if err != nil {
		return err
	}
	if lineData == nil {
		return errors.New("trustline not found for debit")
	}

	rs, err := state.ParseRippleState(lineData)
	if err != nil {
		return err
	}

	// The "sender" is account (their balance decreases)
	accountIsLow := state.CompareAccountIDsForLine(account, issuer) < 0

	// Compute sender's (account's) balance BEFORE update (from sender's perspective)
	var saBefore tx.Amount
	if accountIsLow {
		saBefore = rs.Balance
	} else {
		saBefore = rs.Balance.Negate()
	}

	// Record deferred credit: account (sender) → issuer (receiver).
	// Reference: rippled View.cpp rippleCreditIOU() line 1675:
	//   view.creditHook(uSenderID, uReceiverID, saAmount, saBalance)
	sb.CreditHook(account, issuer, amount, saBefore)

	// Update balance
	if accountIsLow {
		rs.Balance, _ = rs.Balance.Sub(amount)
	} else {
		rs.Balance, _ = rs.Balance.Add(amount)
	}

	// Compute sender's balance AFTER update
	var saBalance tx.Amount
	if accountIsLow {
		saBalance = rs.Balance
	} else {
		saBalance = rs.Balance.Negate()
	}

	// Check trust line deletion conditions
	// Reference: rippled rippleCreditIOU() lines 1688-1745
	bDelete := false
	uFlags := rs.Flags

	if saBefore.Signum() > 0 && saBalance.Signum() <= 0 {
		var senderReserve, senderNoRipple, senderFreeze uint32
		var senderLimit tx.Amount
		var senderQualityIn, senderQualityOut uint32

		if accountIsLow {
			senderReserve = state.LsfLowReserve
			senderNoRipple = state.LsfLowNoRipple
			senderFreeze = state.LsfLowFreeze
			senderLimit = rs.LowLimit
			senderQualityIn = rs.LowQualityIn
			senderQualityOut = rs.LowQualityOut
		} else {
			senderReserve = state.LsfHighReserve
			senderNoRipple = state.LsfHighNoRipple
			senderFreeze = state.LsfHighFreeze
			senderLimit = rs.HighLimit
			senderQualityIn = rs.HighQualityIn
			senderQualityOut = rs.HighQualityOut
		}

		// Read sender's DefaultRipple flag
		senderDefaultRipple := false
		senderKey := keylet.Account(account)
		senderData, sErr := sb.Read(senderKey)
		if sErr == nil && senderData != nil {
			senderAcct, pErr := state.ParseAccountRoot(senderData)
			if pErr == nil {
				senderDefaultRipple = (senderAcct.Flags & state.LsfDefaultRipple) != 0
			}
		}

		hasNoRipple := (uFlags & senderNoRipple) != 0
		noRippleMatchesDefault := hasNoRipple != senderDefaultRipple

		if (uFlags&senderReserve) != 0 &&
			noRippleMatchesDefault &&
			(uFlags&senderFreeze) == 0 &&
			senderLimit.Signum() == 0 &&
			senderQualityIn == 0 &&
			senderQualityOut == 0 {
			// Clear sender's reserve flag and decrement OwnerCount
			rs.Flags &= ^senderReserve
			s.adjustOwnerCount(sb, account, -1, txHash, ledgerSeq)

			// Check final deletion condition
			var receiverReserve uint32
			if accountIsLow {
				receiverReserve = state.LsfHighReserve
			} else {
				receiverReserve = state.LsfLowReserve
			}
			bDelete = saBalance.Signum() == 0 && (uFlags&receiverReserve) == 0
		}
	}

	rs.PreviousTxnID = txHash
	rs.PreviousTxnLgrSeq = ledgerSeq

	lineDataNew, err := state.SerializeRippleState(rs)
	if err != nil {
		return err
	}

	if bDelete {
		// Update first (for metadata), then delete
		sb.Update(lineKey, lineDataNew)

		var lowAccount, highAccount [20]byte
		if accountIsLow {
			lowAccount = account
			highAccount = issuer
		} else {
			lowAccount = issuer
			highAccount = account
		}
		return trustDeleteLine(sb, lineKey, rs, lowAccount, highAccount)
	}

	return sb.Update(lineKey, lineDataNew)
}

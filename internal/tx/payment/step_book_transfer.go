package payment

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// adjustOwnerCount adjusts the OwnerCount on an account.
// Also records the change via AdjustOwnerCount for the PaymentSandbox's
// OwnerCountHook, which returns the maximum count seen.
// Reference: rippled View.cpp adjustOwnerCount() calls adjustOwnerCountHook()
func (s *BookStep) adjustOwnerCount(sb *PaymentSandbox, account [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) error {
	// Read the current owner count BEFORE modifying so we can record it.
	accountKey := keylet.Account(account)
	data, err := sb.Read(accountKey)
	if err != nil || data == nil {
		return nil
	}
	acct, err := state.ParseAccountRoot(data)
	if err != nil {
		return err
	}
	curOC := acct.OwnerCount
	newOC := int(curOC) + delta
	if newOC < 0 {
		newOC = 0
	}

	// Record via AdjustOwnerCount hook so OwnerCountHook returns the maximum.
	sb.AdjustOwnerCount(account, curOC, uint32(newOC))

	// Perform the actual modification.
	return tx.AdjustOwnerCountWithTx(sb, account, delta, txHash, ledgerSeq)
}

// transferFunds transfers an amount between two accounts.
func (s *BookStep) transferFunds(sb *PaymentSandbox, from, to [20]byte, amount EitherAmount, issue Issue) error {
	if from == to {
		return nil
	}

	if amount.IsZero() {
		return nil
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	if issue.IsXRP() {
		return s.transferXRP(sb, from, to, amount.XRP, txHash, ledgerSeq)
	}

	return s.transferIOU(sb, from, to, amount.IOU, issue, txHash, ledgerSeq)
}

// transferFundsWithFee transfers an IOU amount with transfer fee handling.
// grossAmount is debited from sender, netAmount is credited to receiver.
// This handles the XRPL transfer fee mechanism where sender pays more than receiver gets.
func (s *BookStep) transferFundsWithFee(sb *PaymentSandbox, from, to [20]byte, grossAmount, netAmount EitherAmount, issue Issue) error {
	if from == to {
		return nil
	}

	if grossAmount.IsZero() || netAmount.IsZero() {
		return nil
	}

	txHash, ledgerSeq := sb.GetTransactionContext()

	// For XRP, there's no transfer fee - just use regular transfer
	if issue.IsXRP() {
		return s.transferXRP(sb, from, to, grossAmount.XRP, txHash, ledgerSeq)
	}

	// For IOUs: debit sender by gross, credit receiver by net
	issuer := issue.Issuer

	// Special case: if from is issuer, just credit receiver
	if from == issuer {
		return s.creditTrustline(sb, to, issuer, netAmount.IOU, txHash, ledgerSeq)
	}
	// Special case: if to is issuer, just debit sender
	if to == issuer {
		return s.debitTrustline(sb, from, issuer, grossAmount.IOU, txHash, ledgerSeq)
	}

	// Normal case: debit sender by gross, credit receiver by net
	if err := s.debitTrustline(sb, from, issuer, grossAmount.IOU, txHash, ledgerSeq); err != nil {
		return err
	}
	return s.creditTrustline(sb, to, issuer, netAmount.IOU, txHash, ledgerSeq)
}

// transferXRP transfers XRP between accounts.
// When from or to is the XRP pseudo-account (zero), that side is skipped.
// The XRPEndpointStep handles the actual source/destination account balance changes.
// Reference: rippled View.cpp accountSend() lines 1904-1939
func (s *BookStep) transferXRP(sb *PaymentSandbox, from, to [20]byte, drops int64, txHash [32]byte, ledgerSeq uint32) error {
	var xrpAccount [20]byte
	amount := tx.NewXRPAmount(drops)

	// Debit sender (skip if XRP pseudo-account)
	if from != xrpAccount {
		fromKey := keylet.Account(from)
		fromData, err := sb.Read(fromKey)
		if err != nil {
			return err
		}
		if fromData == nil {
			return errors.New("sender account not found")
		}

		fromAccount, err := state.ParseAccountRoot(fromData)
		if err != nil {
			return err
		}

		if int64(fromAccount.Balance) < drops {
			return errors.New("insufficient XRP balance")
		}

		// Record the credit via CreditHook BEFORE updating balance
		preCreditBalance := tx.NewXRPAmount(int64(fromAccount.Balance))
		sb.CreditHook(from, xrpAccount, amount, preCreditBalance)

		fromAccount.Balance -= uint64(drops)
		fromAccount.PreviousTxnID = txHash
		fromAccount.PreviousTxnLgrSeq = ledgerSeq

		fromAccountData, err := state.SerializeAccountRoot(fromAccount)
		if err != nil {
			return err
		}
		if err := sb.Update(fromKey, fromAccountData); err != nil {
			return err
		}
	}

	// Credit receiver (skip if XRP pseudo-account)
	if to != xrpAccount {
		toKey := keylet.Account(to)
		toData, err := sb.Read(toKey)
		if err != nil {
			return err
		}
		if toData == nil {
			return errors.New("receiver account not found")
		}

		toAccount, err := state.ParseAccountRoot(toData)
		if err != nil {
			return err
		}

		// Record the credit to receiver
		receiverPreBalance := tx.NewXRPAmount(-int64(toAccount.Balance))
		sb.CreditHook(xrpAccount, to, amount, receiverPreBalance)

		toAccount.Balance += uint64(drops)
		toAccount.PreviousTxnID = txHash
		toAccount.PreviousTxnLgrSeq = ledgerSeq

		toAccountData, err := state.SerializeAccountRoot(toAccount)
		if err != nil {
			return err
		}
		if err := sb.Update(toKey, toAccountData); err != nil {
			return err
		}
	}

	return nil
}

// transferIOU transfers IOU between accounts via trustline
func (s *BookStep) transferIOU(sb *PaymentSandbox, from, to [20]byte, amount tx.Amount, issue Issue, txHash [32]byte, ledgerSeq uint32) error {
	issuer := issue.Issuer

	if from == issuer {
		return s.creditTrustline(sb, to, issuer, amount, txHash, ledgerSeq)
	}
	if to == issuer {
		return s.debitTrustline(sb, from, issuer, amount, txHash, ledgerSeq)
	}

	if err := s.debitTrustline(sb, from, issuer, amount, txHash, ledgerSeq); err != nil {
		return err
	}
	return s.creditTrustline(sb, to, issuer, amount, txHash, ledgerSeq)
}

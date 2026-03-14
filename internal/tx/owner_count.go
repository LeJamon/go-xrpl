package tx

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// AdjustOwnerCount adjusts an account's OwnerCount by delta on a LedgerView
// without updating PreviousTxn fields.
// Returns an error if the account cannot be read or serialized.
// If the account does not exist, returns nil (account may have been deleted).
// Handles both positive (increment) and negative (decrement) deltas.
// For negative deltas, each unit decrements by 1 only if OwnerCount > 0,
// preventing underflow.
func AdjustOwnerCount(view LedgerView, accountID [20]byte, delta int) error {
	if delta == 0 {
		return nil
	}

	accountKey := keylet.Account(accountID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return nil // Account doesn't exist (may have been deleted)
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return fmt.Errorf("failed to parse account root: %w", err)
	}

	if delta > 0 {
		account.OwnerCount += uint32(delta)
	} else {
		for i := 0; i < -delta; i++ {
			if account.OwnerCount > 0 {
				account.OwnerCount--
			}
		}
	}

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

// AdjustOwnerCountWithTx adjusts an account's OwnerCount by delta and updates
// PreviousTxnID and PreviousTxnLgrSeq fields on the account.
// Clamps owner count to 0 if the result would go negative.
// Returns an error if the account cannot be read or serialized.
// If the account does not exist, returns nil.
func AdjustOwnerCountWithTx(view LedgerView, accountID [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) error {
	accountKey := keylet.Account(accountID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return nil // Account doesn't exist (may have been deleted)
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return fmt.Errorf("failed to parse account root: %w", err)
	}

	newCount := int(account.OwnerCount) + delta
	if newCount < 0 {
		newCount = 0
	}
	account.OwnerCount = uint32(newCount)
	account.PreviousTxnID = txHash
	account.PreviousTxnLgrSeq = ledgerSeq

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

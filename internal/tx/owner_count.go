package tx

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// confineOwnerCount applies adjustment to current, saturating to math.MaxUint32
// on positive overflow and clamping to 0 on negative underflow. Mirrors
// rippled's confineOwnerCount (src/xrpld/ledger/detail/View.cpp).
func confineOwnerCount(current uint32, adjustment int) uint32 {
	if adjustment >= 0 {
		if uint64(current)+uint64(adjustment) > math.MaxUint32 {
			return math.MaxUint32
		}
		return current + uint32(adjustment)
	}
	if int64(current)+int64(adjustment) < 0 {
		return 0
	}
	return current - uint32(-adjustment)
}

// AdjustOwnerCount adjusts an account's OwnerCount by delta on a LedgerView
// without updating PreviousTxn fields.
// Returns an error if the account cannot be read or serialized.
// If the account does not exist, returns nil (account may have been deleted).
// Handles both positive (increment) and negative (decrement) deltas, saturating
// to math.MaxUint32 on overflow and clamping to 0 on underflow.
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

	account.OwnerCount = confineOwnerCount(account.OwnerCount, delta)

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

// AdjustOwnerCountWithTx adjusts an account's OwnerCount by delta and updates
// PreviousTxnID and PreviousTxnLgrSeq fields on the account.
// Saturates to math.MaxUint32 on overflow and clamps to 0 on underflow.
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

	account.OwnerCount = confineOwnerCount(account.OwnerCount, delta)
	account.PreviousTxnID = txHash
	account.PreviousTxnLgrSeq = ledgerSeq

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

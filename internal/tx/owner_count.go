package tx

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

type OwnerCounts struct {
	OwnerCount           uint32
	SponsoredOwnerCount  uint32
	SponsoringOwnerCount uint32
}

func NewOwnerCounts(account *state.AccountRoot) OwnerCounts {
	if account == nil {
		return OwnerCounts{}
	}
	return OwnerCounts{
		OwnerCount:           account.OwnerCount,
		SponsoredOwnerCount:  account.SponsoredOwnerCount,
		SponsoringOwnerCount: account.SponsoringOwnerCount,
	}
}

func (counts OwnerCounts) Count() uint32 {
	count := int64(counts.OwnerCount) - int64(counts.SponsoredOwnerCount) + int64(counts.SponsoringOwnerCount)
	if count <= 0 {
		return 0
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

func (counts OwnerCounts) AdjustedCount(delta int64) uint32 {
	count := int64(counts.Count()) + delta
	if count <= 0 {
		return 0
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

func MaxOwnerCounts(left, right OwnerCounts) OwnerCounts {
	if right.Count() > left.Count() {
		return right
	}
	return left
}

func AccountCountForReserve(account *state.AccountRoot) uint32 {
	if account == nil {
		return 0
	}
	count := uint64(account.SponsoringAccountCount)
	if !account.HasSponsor {
		count++
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

type ownerCountAdjustHook interface {
	AdjustOwnerCount(account [20]byte, current, next OwnerCounts)
}

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

// ConfineOwnerCount applies rippled's saturating OwnerCount arithmetic.
func ConfineOwnerCount(current uint32, adjustment int) uint32 {
	return confineOwnerCount(current, adjustment)
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

	current := NewOwnerCounts(account)
	account.OwnerCount = confineOwnerCount(account.OwnerCount, delta)
	if hook, ok := view.(ownerCountAdjustHook); ok {
		hook.AdjustOwnerCount(accountID, current, NewOwnerCounts(account))
	}

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

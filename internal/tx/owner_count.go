package tx

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// OwnerCounts is the reserve-bearing owner-count state tracked by payment
// sandboxes while ledger entries are created or removed.
type OwnerCounts struct {
	Owner      uint32
	Sponsored  uint32
	Sponsoring uint32
}

func ownerCounts(account *state.AccountRoot) OwnerCounts {
	return OwnerCounts{
		Owner: account.OwnerCount, Sponsored: account.SponsoredOwnerCount,
		Sponsoring: account.SponsoringOwnerCount,
	}
}

func (c OwnerCounts) effective() uint32 {
	count := int64(c.Owner) - int64(c.Sponsored) + int64(c.Sponsoring)
	if count < 0 {
		return 0
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

// MoreRestrictiveThan reports whether c carries more reserve than other,
// matching rippled's OwnerCounts ordering.
func (c OwnerCounts) MoreRestrictiveThan(other OwnerCounts) bool {
	if c.effective() != other.effective() {
		return c.effective() > other.effective()
	}
	if c.Owner != other.Owner {
		return c.Owner > other.Owner
	}
	if c.Sponsored != other.Sponsored {
		return c.Sponsored > other.Sponsored
	}
	return c.Sponsoring > other.Sponsoring
}

type ownerCountsAdjuster interface {
	AdjustOwnerCounts(account [20]byte, current, next OwnerCounts)
}

type ownerCountAdjuster interface {
	AdjustOwnerCount(account [20]byte, current, next uint32)
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
	if err != nil {
		return fmt.Errorf("failed to read account root: %w", err)
	}
	if data == nil {
		return nil
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return fmt.Errorf("failed to parse account root: %w", err)
	}

	current := ownerCounts(account)
	account.OwnerCount = confineOwnerCount(account.OwnerCount, delta)
	if hook, ok := view.(ownerCountsAdjuster); ok {
		hook.AdjustOwnerCounts(accountID, current, ownerCounts(account))
	} else if hook, ok := view.(ownerCountAdjuster); ok {
		hook.AdjustOwnerCount(accountID, current.Owner, account.OwnerCount)
	}

	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return fmt.Errorf("failed to serialize account root: %w", err)
	}

	return view.Update(accountKey, updated)
}

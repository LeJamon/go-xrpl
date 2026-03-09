package entry

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// VoteSlot represents a vote slot in an AMM
type VoteSlot struct {
	Account    [20]byte // Account that voted
	TradingFee uint16   // Trading fee voted for (in basis points, 1/100000)
	VoteWeight uint32   // Weight of this vote
}

// AuctionSlot represents the auction slot in an AMM
type AuctionSlot struct {
	Account       [20]byte   // Account holding the auction slot
	AuthAccounts  [][20]byte // Accounts authorized to trade at discounted fee
	DiscountedFee uint32     // Discounted trading fee
	Expiration    uint32     // When the auction slot expires
	Price         Amount     // Price paid for the slot
}

// Amount represents a currency amount (XRP drops or IOU)
type Amount struct {
	Value    string   // Value as string for IOUs
	Currency [20]byte // Currency code
	Issuer   [20]byte // Issuer account
	Drops    uint64   // XRP amount in drops (if native)
	IsNative bool     // True if this is XRP
}

// AMM represents an Automated Market Maker ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltAMM
type AMM struct {
	BaseEntry

	// Required fields
	Account        [20]byte // The AMM's account (pseudo-account)
	LPTokenBalance Amount   // Total LP tokens outstanding
	Asset          Issue    // First asset in the pool
	Asset2         Issue    // Second asset in the pool
	OwnerNode      uint64   // Directory node hint

	// Default fields (always present but may be zero)
	TradingFee uint16 // Trading fee in basis points (1 = 0.001%)

	// Optional fields
	VoteSlots   []VoteSlot   // Active vote slots
	AuctionSlot *AuctionSlot // Current auction slot holder
}

func (a *AMM) Type() entry.Type {
	return entry.TypeAMM
}

func (a *AMM) Validate() error {
	if a.Account == [20]byte{} {
		return errors.New("account is required")
	}
	if a.TradingFee > 1000 {
		return errors.New("trading fee cannot exceed 1% (1000 basis points)")
	}
	return nil
}

func (a *AMM) Hash() ([32]byte, error) {
	hash := a.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= a.Account[i]
	}
	return hash, nil
}

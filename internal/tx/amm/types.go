package amm

import "github.com/LeJamon/goXRPLd/internal/tx"

// AMMData holds the internal AMM ledger entry data.
// This structure matches rippled's AMM ledger entry exactly.
// IMPORTANT: Asset balances are NOT stored in this entry.
// Instead, they are read from:
// - XRP: AMM account's AccountRoot.Balance
// - IOU: Trustlines between AMM account and asset issuers
// Use ammHolds() or ammPoolHolds() to get current balances.
// Reference: rippled include/xrpl/protocol/detail/ledger_entries.macro ltAMM
type AMMData struct {
	// Account is the AMM's pseudo-account ID (derived from the AMM hash)
	Account [20]byte
	// Asset is the first asset in the pair (Issue: currency + issuer)
	Asset tx.Asset
	// Asset2 is the second asset in the pair (Issue: currency + issuer)
	Asset2 tx.Asset
	// TradingFee is the trading fee in basis points (0-1000, where 1000 = 1%)
	TradingFee uint16
	// LPTokenBalance is the total LP tokens outstanding for this AMM
	LPTokenBalance tx.Amount
	// OwnerNode is the page in the owner directory where this AMM is stored
	OwnerNode uint64
	// VoteSlots contains the current fee voting slots (max 8)
	VoteSlots []VoteSlotData
	// AuctionSlot contains the current auction slot state (optional)
	AuctionSlot *AuctionSlotData
}

// VoteSlotData holds a single vote slot entry.
type VoteSlotData struct {
	Account    [20]byte
	TradingFee uint16
	VoteWeight uint32
}

// AuctionSlotData holds the auction slot state.
type AuctionSlotData struct {
	Account       [20]byte
	Expiration    uint32
	Price         tx.Amount
	DiscountedFee uint16 // Discounted trading fee for auction slot holder
	AuthAccounts  [][20]byte
}

// AuthAccount is an authorized account for AMM slot trading
type AuthAccount struct {
	AuthAccount AuthAccountData `json:"AuthAccount"`
}

// AuthAccountData contains the account address
type AuthAccountData struct {
	Account string `json:"Account"`
}

package offer

import "github.com/LeJamon/go-xrpl/ledger/entry"

// OfferCreate flags (exported for use by other packages)
const (
	// OfferCreateFlagPassive won't consume offers that match this one
	OfferCreateFlagPassive uint32 = 0x00010000
	// OfferCreateFlagImmediateOrCancel treats offer as immediate-or-cancel
	OfferCreateFlagImmediateOrCancel uint32 = 0x00020000
	// OfferCreateFlagFillOrKill treats offer as fill-or-kill
	OfferCreateFlagFillOrKill uint32 = 0x00040000
	// OfferCreateFlagSell makes the offer a sell offer
	OfferCreateFlagSell uint32 = 0x00080000
)

// Ledger offer flags (re-exported from ledger/entry).
const (
	lsfOfferPassive = entry.LsfPassive
	lsfOfferSell    = entry.LsfSell
)

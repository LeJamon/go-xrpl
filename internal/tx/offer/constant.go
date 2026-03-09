package offer

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

// Ledger offer flags
const (
	lsfOfferPassive uint32 = 0x00010000
	lsfOfferSell    uint32 = 0x00020000
)

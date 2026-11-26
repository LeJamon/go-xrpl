package nftoken

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// NFToken constants matching rippled
const (
	// maxTransferFee is the maximum transfer fee (50000 = 50%)
	maxTransferFee = 50000

	// maxTokenURILength is the maximum length of a token URI (256 bytes when encoded)
	// Note: When provided in transactions, URI is hex-encoded, so the actual
	// byte length is len(hexString)/2
	maxTokenURILength = 256

	// dirMaxTokensPerPage is the maximum number of NFTs per page
	// Reference: rippled Protocol.h - dirMaxTokensPerPage = 32
	dirMaxTokensPerPage = 32

	// NFTokenID flag values live in nftoken_id.go as the exported NFTokenFlag*
	// constants; this package uses that single set.

	// lsfSellNFToken is the flag for sell offers in ledger entries
	lsfSellNFToken = entry.LsfSellNFToken

	// maxDeletableTokenOfferEntries is the max offers to delete on burn
	maxDeletableTokenOfferEntries = 500

	// maxTokenOfferCancelCount is the max offers that can be cancelled in one tx
	maxTokenOfferCancelCount = 500
)

// nftIssuerCut returns the issuer's share of an NFT sale: the amount times the
// transfer fee as a rate (fee/100000), computed in Number arithmetic and
// converted back to the amount's asset with round-to-nearest.
func nftIssuerCut(amount state.Amount, fee uint16, nctx state.NumberContext) state.Amount {
	rate := nctx.Number(int64(fee)*10000, -9, state.RoundToNearest)
	cut := nctx.FromAmount(amount, state.RoundToNearest).MulRounded(rate, state.RoundToNearest)
	return nctx.ToAmount(cut, amount, state.RoundToNearest)
}

// nftIssuerCutXRP is nftIssuerCut for a sale priced in drops.
func nftIssuerCutXRP(drops uint64, fee uint16, nctx state.NumberContext) uint64 {
	return uint64(nftIssuerCut(state.NewXRPAmountFromInt(int64(drops)), fee, nctx).Drops())
}

// clampedSub subtracts n from count, clamping at zero.
func clampedSub(count uint32, n int) uint32 {
	if n <= 0 {
		return count
	}
	if uint32(n) >= count {
		return 0
	}
	return count - uint32(n)
}

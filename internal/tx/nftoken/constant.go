package nftoken

// NFToken constants matching rippled
const (
	// maxTransferFee is the maximum transfer fee (50000 = 50%)
	maxTransferFee = 50000

	// maxTokenURILength is the maximum length of a token URI (256 bytes when encoded)
	// Note: When provided in transactions, URI is hex-encoded, so the actual
	// byte length is len(hexString)/2
	maxTokenURILength = 256

	// transferFeeDivisor is the divisor used for transfer fee calculation
	// Transfer fee is in basis points where 50000 = 50%
	// Calculation: amount * transferFee / transferFeeDivisor
	transferFeeDivisor = 100000

	// transferFeeDivisor32 is the uint32 version for use with Amount.MulRatio
	transferFeeDivisor32 uint32 = 100000

	// dirMaxTokensPerPage is the maximum number of NFTs per page
	// Reference: rippled Protocol.h - dirMaxTokensPerPage = 32
	dirMaxTokensPerPage = 32

	// NFToken flags stored in NFTokenID
	nftFlagBurnable     uint16 = 0x0001
	nftFlagOnlyXRP      uint16 = 0x0002
	nftFlagTrustLine    uint16 = 0x0004
	nftFlagTransferable uint16 = 0x0008
	nftFlagMutable      uint16 = 0x0010

	// lsfSellNFToken is the flag for sell offers in ledger entries
	lsfSellNFToken uint32 = 0x00000001

	// maxDeletableTokenOfferEntries is the max offers to delete on burn
	maxDeletableTokenOfferEntries = 500

	// maxInt is used as unlimited limit for pre-amendment offer deletion
	maxInt = int(^uint(0) >> 1)

	// maxTokenOfferCancelCount is the max offers that can be cancelled in one tx
	maxTokenOfferCancelCount = 500

	// tfNFTokenCancelOfferMask is the mask for invalid flags (all flags are invalid)
	tfNFTokenCancelOfferMask uint32 = 0xFFFFFFFF
)

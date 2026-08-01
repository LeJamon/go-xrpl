package protocol

const (
	// MaxMPTokenMetadataLength is the maximum MPToken metadata length in bytes.
	MaxMPTokenMetadataLength = 1024

	// MaxMPTokenTransferFee is the maximum MPToken transfer fee in tenths of a
	// basis point (50,000 = 5,000 basis points = 50%).
	MaxMPTokenTransferFee uint16 = 50_000

	// MaxMPTokenAmount is the maximum representable MPToken amount.
	MaxMPTokenAmount uint64 = 0x7FFF_FFFF_FFFF_FFFF
)

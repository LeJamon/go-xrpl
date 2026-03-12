package protocol

// Transfer rate and quality constants.
// QualityOne (1e9) is the identity transfer rate: no fee is charged.
const (
	QualityOne      uint32 = 1_000_000_000
	TransferRateMin uint32 = 1_000_000_000
	TransferRateMax uint32 = 2_000_000_000
)

// Tick size bounds for order book rounding.
const (
	TickSizeMin uint8 = 3
	TickSizeMax uint8 = 16
)

// Fee limits.
const (
	// TradingFeeMax is the maximum AMM trading fee in basis points (1000 = 1%).
	TradingFeeMax uint16 = 1000

	// NFTokenTransferFeeMax is the maximum NFToken transfer fee in basis points (50000 = 50%).
	NFTokenTransferFeeMax uint16 = 50000
)

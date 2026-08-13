package protocol

// Lending (XLS-66) protocol constants, mirroring rippled 3.3.0
// include/xrpl/protocol/Protocol.h. Rates are expressed in bips (basis points,
// 1/10000) or tenth-bips (1/100000); a management fee rate of 10% is stored as
// 10000 tenth-bips.
const (
	// BipsPerUnity is 100% expressed in bips: 100 * 100 = 10000.
	BipsPerUnity uint32 = 100 * 100
	// TenthBipsPerUnity is 100% expressed in 1/10 bips: 100000.
	TenthBipsPerUnity uint32 = BipsPerUnity * 10

	// SecondsInYear is the proration base for annualized interest rates.
	SecondsInYear uint32 = 365 * 24 * 60 * 60
)

// PercentageToBips converts a whole-percent value to bips (p% -> p*100 bips).
func PercentageToBips(percentage uint32) uint32 {
	return percentage * BipsPerUnity / 100
}

// PercentageToTenthBips converts a whole-percent value to 1/10 bips.
func PercentageToTenthBips(percentage uint32) uint32 {
	return percentage * TenthBipsPerUnity / 100
}

// Lending rate and payment caps.
const (
	// MaxManagementFeeRate is the maximum loan-broker management fee: 10% in
	// 1/10 bips (10000). Stored as a UInt16 (sfManagementFeeRate).
	MaxManagementFeeRate uint16 = 10_000

	// The following are 100% caps in 1/10 bips (100000), stored as UInt32.
	MaxCoverRate               uint32 = 100_000
	MaxOverpaymentFee          uint32 = 100_000
	MaxInterestRate            uint32 = 100_000
	MaxLateInterestRate        uint32 = 100_000
	MaxCloseInterestRate       uint32 = 100_000
	MaxOverpaymentInterestRate uint32 = 100_000

	// LoanPaymentsPerFeeIncrement: a LoanPay transaction costs one base fee per
	// this many combined payments, estimated from Amount / PeriodicPayment.
	LoanPaymentsPerFeeIncrement = 5

	// LoanMaximumPaymentsPerTransaction caps the combined payments a single
	// LoanPay processes; excess amount is left unpaid (the tx still succeeds).
	LoanMaximumPaymentsPerTransaction = 100
)

package drops

// Fees reflects the fee and reserve settings in effect for a ledger. The values
// are fixed for every transaction applied to a given ledger and change only
// between ledgers:
//   - Base is the reference transaction cost (the base fee), in drops.
//   - Reserve is the base account reserve, in drops.
//   - Increment is the reserve charged per owned object, in drops.
type Fees struct {
	Base      XRPAmount
	Reserve   XRPAmount
	Increment XRPAmount
}

const (
	// DefaultBaseFee is the network default transaction fee in drops.
	DefaultBaseFee XRPAmount = 10
	// DefaultReserveBase is the network default account reserve in drops.
	DefaultReserveBase XRPAmount = 10_000_000
	// DefaultReserveIncrement is the network default owner reserve in drops.
	DefaultReserveIncrement XRPAmount = 2_000_000
)

// DefaultFees returns the fee schedule used when a ledger has no readable
// FeeSettings entry.
func DefaultFees() Fees {
	return Fees{
		Base:      DefaultBaseFee,
		Reserve:   DefaultReserveBase,
		Increment: DefaultReserveIncrement,
	}
}

// AccountReserve returns the total reserve an account must hold to own
// ownerSize objects: the base reserve plus ownerSize reserve increments.
func (f *Fees) AccountReserve(ownerSize int64) XRPAmount {
	return f.Reserve + f.Increment.Mul(ownerSize)
}

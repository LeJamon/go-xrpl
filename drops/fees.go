package drops

// Fees holds the XRP reserve parameters in effect for a ledger: the base
// account reserve and the incremental reserve charged per owned object.
type Fees struct {
	Base      XRPAmount
	Reserve   XRPAmount
	Increment XRPAmount
}

// AccountReserve returns the total reserve an account must hold to own
// ownerSize objects: the base reserve plus ownerSize reserve increments.
func (f *Fees) AccountReserve(ownerSize int64) XRPAmount {
	return f.Reserve + f.Increment.Mul(ownerSize)
}

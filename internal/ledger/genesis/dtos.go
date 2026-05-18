package genesis

import "github.com/LeJamon/goXRPLd/drops"

// accountRoot is the in-memory DTO for an AccountRoot ledger entry written
// during genesis construction. Production reads/writes of AccountRoot use
// internal/ledger/state.AccountRoot; this minimal struct exists only so
// genesis can build the initial state map without depending on the live
// state pipeline.
type accountRoot struct {
	Flags      uint32
	Account    [20]byte
	Sequence   uint32
	Balance    uint64
	OwnerCount uint32
}

// feeSettings is the in-memory DTO for the FeeSettings ledger entry written
// during genesis construction. Modern fields (XRPFees amendment) take
// precedence; legacy fields apply only when XRPFees is not enabled.
type feeSettings struct {
	BaseFeeDrops          drops.XRPAmount
	ReserveBaseDrops      drops.XRPAmount
	ReserveIncrementDrops drops.XRPAmount

	BaseFee           *uint64
	ReferenceFeeUnits *uint32
	ReserveBase       *uint32
	ReserveIncrement  *uint32
}

func newFeeSettings(baseFee, reserveBase, reserveIncrement drops.XRPAmount) *feeSettings {
	return &feeSettings{
		BaseFeeDrops:          baseFee,
		ReserveBaseDrops:      reserveBase,
		ReserveIncrementDrops: reserveIncrement,
	}
}

func newLegacyFeeSettings(baseFee uint64, refFeeUnits, reserveBase, reserveIncrement uint32) *feeSettings {
	return &feeSettings{
		BaseFee:           &baseFee,
		ReferenceFeeUnits: &refFeeUnits,
		ReserveBase:       &reserveBase,
		ReserveIncrement:  &reserveIncrement,
	}
}

func (f *feeSettings) IsUsingModernFees() bool {
	return f.BaseFeeDrops > 0 || f.ReserveBaseDrops > 0 || f.ReserveIncrementDrops > 0
}

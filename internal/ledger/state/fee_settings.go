package state

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

var ErrInvalidFeeSettings = errors.New("invalid FeeSettings field combination")

// FeeSettings represents the singleton fee settings ledger entry.
// This entry stores the current network fee configuration.
// Reference: rippled LedgerFormats.h and Fees.h
type FeeSettings struct {
	// Modern fee fields (XRPFees amendment)
	BaseFeeDrops          uint64 // Base transaction fee in drops
	ReserveBaseDrops      uint64 // Account reserve base in drops
	ReserveIncrementDrops uint64 // Owner reserve increment in drops

	// Legacy fee fields (pre-XRPFees amendment)
	BaseFee           uint64 // Base fee (legacy)
	ReferenceFeeUnits uint32 // Reference fee units (legacy, typically 10)
	ReserveBase       uint32 // Reserve base in drops (legacy, fits in uint32 for old values)
	ReserveIncrement  uint32 // Reserve increment in drops (legacy)

	// XRPFeesMode reports whether the entry encodes the modern (post-XRPFees)
	// field set. SerializeFeeSettings emits the matching triple/quad even when
	// values are zero, mirroring rippled Change.cpp:362-379 which uses
	// STObject::operator= (assignment) rather than a value-is-nonzero gate.
	XRPFeesMode              bool
	HasBaseFeeDrops          bool
	HasReserveBaseDrops      bool
	HasReserveIncrementDrops bool
	HasBaseFee               bool
	HasReserveBase           bool
	HasReserveIncrement      bool

	// Tracking fields (not always present)
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32

	feeFieldsPresent bool
	decoded          ledgerfields.FeeSettings
}

// ParseFeeSettings parses fee settings data from binary format
func ParseFeeSettings(data []byte) (*FeeSettings, error) {
	if len(data) < 4 {
		return nil, errors.New("fee settings data too short")
	}

	var decoded ledgerfields.FeeSettings
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode FeeSettings: %w", err)
	}
	fee := &FeeSettings{
		decoded: decoded,
	}

	if decoded.HasBaseFee() {
		var err error
		fee.BaseFee, err = decoded.GetBaseFee()
		if err != nil {
			return nil, fmt.Errorf("failed to decode FeeSettings.BaseFee: %w", err)
		}
		fee.HasBaseFee = true
	}
	hasReferenceFeeUnits := decoded.HasReferenceFeeUnits()
	if hasReferenceFeeUnits {
		fee.ReferenceFeeUnits = decoded.ReferenceFeeUnits
	}
	if decoded.HasReserveBase() {
		fee.ReserveBase = decoded.ReserveBase
		fee.HasReserveBase = true
	}
	if decoded.HasReserveIncrement() {
		fee.ReserveIncrement = decoded.ReserveIncrement
		fee.HasReserveIncrement = true
	}
	for _, amount := range []struct {
		name    string
		value   func() (ledgerfields.AmountValue, error)
		dst     *uint64
		present *bool
	}{
		{"BaseFeeDrops", decoded.GetBaseFeeDrops, &fee.BaseFeeDrops, &fee.HasBaseFeeDrops},
		{"ReserveBaseDrops", decoded.GetReserveBaseDrops, &fee.ReserveBaseDrops, &fee.HasReserveBaseDrops},
		{"ReserveIncrementDrops", decoded.GetReserveIncrementDrops, &fee.ReserveIncrementDrops, &fee.HasReserveIncrementDrops},
	} {
		var present bool
		switch amount.name {
		case "BaseFeeDrops":
			present = decoded.HasBaseFeeDrops()
		case "ReserveBaseDrops":
			present = decoded.HasReserveBaseDrops()
		case "ReserveIncrementDrops":
			present = decoded.HasReserveIncrementDrops()
		}
		if !present {
			continue
		}
		amountValue, err := amount.value()
		if err != nil {
			return nil, fmt.Errorf("failed to decode FeeSettings.%s: %w", amount.name, err)
		}
		decodedAmount, err := decodeLedgerAmount("FeeSettings."+amount.name, amountValue)
		if err != nil {
			return nil, err
		}
		drops, err := nonNegativeNativeDrops("FeeSettings."+amount.name, decodedAmount)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidFeeSettings, err)
		}
		*amount.dst = drops
		*amount.present = true
	}
	if decoded.HasPreviousTxnID() {
		var err error
		fee.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode FeeSettings.PreviousTxnID: %w", err)
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		fee.PreviousTxnLgrSeq = decoded.PreviousTxnLgrSeq
	}
	modern := fee.HasBaseFeeDrops || fee.HasReserveBaseDrops || fee.HasReserveIncrementDrops
	legacy := fee.HasBaseFee || fee.HasReserveBase || fee.HasReserveIncrement
	if modern && legacy {
		return nil, fmt.Errorf("%w: FeeSettings mixes legacy and XRPFees fields", ErrInvalidFeeSettings)
	}
	fee.XRPFeesMode = modern
	fee.feeFieldsPresent = modern || legacy || hasReferenceFeeUnits

	return fee, nil
}

// SerializeFeeSettings serializes a FeeSettings to binary format. The active
// field set (modern triple under XRPFeesMode, legacy quad otherwise) is always
// emitted, including zero-valued fields — matching rippled's `set()` /
// `makeFieldAbsent()` semantics at Change.cpp:362-379.
func SerializeFeeSettings(fee *FeeSettings) ([]byte, error) {
	// sfFlags is a soeREQUIRED common field (LedgerFormats.cpp commonFields), so
	// rippled serializes it on every entry — present at its default 0 from the
	// SLE template. The genesis FeeSettings (genesis.go) already emits Flags=0;
	// the runtime serializer (SetFee re-serialization) must match or the
	// post-fee-vote FeeSettings state diverges (account_hash fork).
	entry := fee.decoded
	if !entry.HasFlags() {
		entry.SetFlags(0)
	}

	if fee.XRPFeesMode {
		if err := entry.SetBaseFeeDropsValue(ledgerfields.AmountValue{Value: fmt.Sprintf("%d", fee.BaseFeeDrops)}); err != nil {
			return nil, fmt.Errorf("failed to encode FeeSettings.BaseFeeDrops: %w", err)
		}
		if err := entry.SetReserveBaseDropsValue(ledgerfields.AmountValue{Value: fmt.Sprintf("%d", fee.ReserveBaseDrops)}); err != nil {
			return nil, fmt.Errorf("failed to encode FeeSettings.ReserveBaseDrops: %w", err)
		}
		if err := entry.SetReserveIncrementDropsValue(ledgerfields.AmountValue{Value: fmt.Sprintf("%d", fee.ReserveIncrementDrops)}); err != nil {
			return nil, fmt.Errorf("failed to encode FeeSettings.ReserveIncrementDrops: %w", err)
		}
		entry.ClearBaseFee()
		entry.ClearReferenceFeeUnits()
		entry.ClearReserveBase()
		entry.ClearReserveIncrement()
	} else {
		entry.SetBaseFeeValue(fee.BaseFee)
		entry.SetReferenceFeeUnits(fee.ReferenceFeeUnits)
		entry.SetReserveBase(fee.ReserveBase)
		entry.SetReserveIncrement(fee.ReserveIncrement)
		entry.ClearBaseFeeDrops()
		entry.ClearReserveBaseDrops()
		entry.ClearReserveIncrementDrops()
	}

	var zeroHash [32]byte
	if fee.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnIDValue(fee.PreviousTxnID)
	} else if !entry.HasPreviousTxnID() {
		entry.ClearPreviousTxnID()
	}
	if fee.PreviousTxnLgrSeq != 0 {
		entry.SetPreviousTxnLgrSeq(fee.PreviousTxnLgrSeq)
	} else if !entry.HasPreviousTxnLgrSeq() {
		entry.ClearPreviousTxnLgrSeq()
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode FeeSettings: %w", err)
	}
	return data, nil
}

// GetBaseFee returns the base transaction fee in drops.
func (f *FeeSettings) GetBaseFee() uint64 {
	if f.XRPFeesMode {
		return f.BaseFeeDrops
	}
	return f.BaseFee
}

// GetReserveBase returns the account reserve base in drops.
func (f *FeeSettings) GetReserveBase() uint64 {
	if f.XRPFeesMode {
		return f.ReserveBaseDrops
	}
	return uint64(f.ReserveBase)
}

// GetReserveIncrement returns the owner reserve increment in drops.
func (f *FeeSettings) GetReserveIncrement() uint64 {
	if f.XRPFeesMode {
		return f.ReserveIncrementDrops
	}
	return uint64(f.ReserveIncrement)
}

// Fees resolves the active modern or legacy fields. The Go zero value uses the
// network defaults; parsed entries preserve explicitly serialized zero fees.
func (f *FeeSettings) Fees() drops.Fees {
	if !f.feeFieldsPresent && !f.XRPFeesMode && f.BaseFee == 0 && f.ReferenceFeeUnits == 0 && f.ReserveBase == 0 && f.ReserveIncrement == 0 {
		return drops.DefaultFees()
	}
	return drops.Fees{
		Base:      drops.XRPAmount(f.GetBaseFee()),
		Reserve:   drops.XRPAmount(f.GetReserveBase()),
		Increment: drops.XRPAmount(f.GetReserveIncrement()),
	}
}

// IsUsingModernFees returns true if the entry encodes the post-XRPFees field
// set. Authoritative source is XRPFeesMode (set at Parse and at Apply time).
func (f *FeeSettings) IsUsingModernFees() bool {
	return f.XRPFeesMode
}

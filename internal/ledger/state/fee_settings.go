package state

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

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
	XRPFeesMode bool

	// Tracking fields (not always present)
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32

	feeFieldsPresent bool
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
	fields := decoded.ToMap()
	fee := &FeeSettings{
		ReferenceFeeUnits: decoded.ReferenceFeeUnits,
		ReserveBase:       decoded.ReserveBase,
		ReserveIncrement:  decoded.ReserveIncrement,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
	}

	var err error
	if _, ok := fields["BaseFee"]; ok {
		fee.BaseFee, err = parseLedgerUint64("FeeSettings.BaseFee", decoded.BaseFee)
		if err != nil {
			return nil, err
		}
		fee.feeFieldsPresent = true
	}
	for _, name := range []string{"ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"} {
		if _, ok := fields[name]; ok {
			fee.feeFieldsPresent = true
			break
		}
	}
	for _, amount := range []struct {
		name  string
		value any
		dst   *uint64
	}{
		{"BaseFeeDrops", decoded.BaseFeeDrops, &fee.BaseFeeDrops},
		{"ReserveBaseDrops", decoded.ReserveBaseDrops, &fee.ReserveBaseDrops},
		{"ReserveIncrementDrops", decoded.ReserveIncrementDrops, &fee.ReserveIncrementDrops},
	} {
		if _, ok := fields[amount.name]; !ok {
			continue
		}
		decodedAmount, err := decodeLedgerAmount("FeeSettings."+amount.name, amount.value)
		if err != nil {
			return nil, err
		}
		if !decodedAmount.IsNative() {
			continue
		}
		*amount.dst = nativeMagnitude(decodedAmount)
		fee.feeFieldsPresent = true
		fee.XRPFeesMode = true
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("FeeSettings.PreviousTxnID", decoded.PreviousTxnID, fee.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}

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
	entry := &ledgerfields.FeeSettings{}
	entry.SetFlags(0)

	if fee.XRPFeesMode {
		entry.SetBaseFeeDrops(fmt.Sprintf("%d", fee.BaseFeeDrops))
		entry.SetReserveBaseDrops(fmt.Sprintf("%d", fee.ReserveBaseDrops))
		entry.SetReserveIncrementDrops(fmt.Sprintf("%d", fee.ReserveIncrementDrops))
	} else {
		entry.SetBaseFee(fmt.Sprintf("%x", fee.BaseFee))
		entry.SetReferenceFeeUnits(fee.ReferenceFeeUnits)
		entry.SetReserveBase(fee.ReserveBase)
		entry.SetReserveIncrement(fee.ReserveIncrement)
	}

	// Add tracking fields if present
	var zeroHash [32]byte
	if fee.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(hex.EncodeToString(fee.PreviousTxnID[:]))
	}
	if fee.PreviousTxnLgrSeq > 0 {
		entry.SetPreviousTxnLgrSeq(fee.PreviousTxnLgrSeq)
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

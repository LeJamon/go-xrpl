package state

import (
	"encoding/hex"
	"errors"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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
}

// Ledger entry type for FeeSettings
const ledgerEntryTypeFeeSettings = uint16(entry.TypeFeeSettings)

// Field codes for FeeSettings
// Reference: XRPL binary codec field definitions
const (
	// UInt32 fields (legacy)
	fieldCodeReferenceFeeUnits uint8 = 30 // sfReferenceFeeUnits
	fieldCodeReserveBase       uint8 = 31 // sfReserveBase (legacy)
	fieldCodeReserveIncrement  uint8 = 32 // sfReserveIncrement (legacy)

	// UInt64 fields
	fieldCodeBaseFee uint8 = 5 // sfBaseFee (legacy)

	// XRPAmount fields (Amount type) - modern XRPFees amendment
	// Field codes from definitions.json: BaseFeeDrops=22, ReserveBaseDrops=23, ReserveIncrementDrops=24
	fieldCodeBaseFeeDrops          uint8 = 22 // sfBaseFeeDrops
	fieldCodeReserveBaseDrops      uint8 = 23 // sfReserveBaseDrops
	fieldCodeReserveIncrementDrops uint8 = 24 // sfReserveIncrementDrops
)

// ParseFeeSettings parses fee settings data from binary format
func ParseFeeSettings(data []byte) (*FeeSettings, error) {
	if len(data) < 4 {
		return nil, errors.New("fee settings data too short")
	}

	fee := &FeeSettings{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt16:
			if f.FieldCode == fieldCodeLedgerEntryType {
				if f.UInt16() != ledgerEntryTypeFeeSettings {
					return fmt.Errorf("%w: not a FeeSettings entry", ErrInvalidFeeSettings)
				}
			}

		case stUInt32:
			switch f.FieldCode {
			case 5: // PreviousTxnLgrSeq
				fee.PreviousTxnLgrSeq = f.UInt32()
			case int(fieldCodeReferenceFeeUnits):
				fee.ReferenceFeeUnits = f.UInt32()
			case int(fieldCodeReserveBase):
				fee.ReserveBase = f.UInt32()
				fee.HasReserveBase = true
			case int(fieldCodeReserveIncrement):
				fee.ReserveIncrement = f.UInt32()
				fee.HasReserveIncrement = true
			}

		case stUInt64:
			if f.FieldCode == int(fieldCodeBaseFee) {
				fee.BaseFee = f.UInt64()
				fee.HasBaseFee = true
			}

		case stAmount:
			switch f.FieldCode {
			case int(fieldCodeBaseFeeDrops), int(fieldCodeReserveBaseDrops), int(fieldCodeReserveIncrementDrops):
				if len(f.Value) != 8 {
					return fmt.Errorf("%w: FeeSettings contains a non-native XRP fee", ErrInvalidFeeSettings)
				}
				drops := xrpDrops(f.Value)
				switch f.FieldCode {
				case int(fieldCodeBaseFeeDrops):
					fee.BaseFeeDrops = drops
					fee.HasBaseFeeDrops = true
				case int(fieldCodeReserveBaseDrops):
					fee.ReserveBaseDrops = drops
					fee.HasReserveBaseDrops = true
				case int(fieldCodeReserveIncrementDrops):
					fee.ReserveIncrementDrops = drops
					fee.HasReserveIncrementDrops = true
				}
			}

		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				fee.PreviousTxnID = f.Hash256()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	modern := fee.HasBaseFeeDrops || fee.HasReserveBaseDrops || fee.HasReserveIncrementDrops
	legacy := fee.HasBaseFee || fee.HasReserveBase || fee.HasReserveIncrement
	if modern && legacy {
		return nil, fmt.Errorf("%w: FeeSettings mixes legacy and XRPFees fields", ErrInvalidFeeSettings)
	}
	fee.XRPFeesMode = modern

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
	jsonObj := map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
	}

	if fee.XRPFeesMode {
		jsonObj["BaseFeeDrops"] = fmt.Sprintf("%d", fee.BaseFeeDrops)
		jsonObj["ReserveBaseDrops"] = fmt.Sprintf("%d", fee.ReserveBaseDrops)
		jsonObj["ReserveIncrementDrops"] = fmt.Sprintf("%d", fee.ReserveIncrementDrops)
	} else {
		jsonObj["BaseFee"] = fmt.Sprintf("%x", fee.BaseFee) // Hex string per rippled
		jsonObj["ReferenceFeeUnits"] = fee.ReferenceFeeUnits
		jsonObj["ReserveBase"] = fee.ReserveBase
		jsonObj["ReserveIncrement"] = fee.ReserveIncrement
	}

	// Add tracking fields if present
	var zeroHash [32]byte
	if fee.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = hex.EncodeToString(fee.PreviousTxnID[:])
	}
	if fee.PreviousTxnLgrSeq > 0 {
		jsonObj["PreviousTxnLgrSeq"] = fee.PreviousTxnLgrSeq
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode FeeSettings: %w", err)
	}

	return hex.DecodeString(hexStr)
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

// IsUsingModernFees returns true if the entry encodes the post-XRPFees field
// set. Authoritative source is XRPFeesMode (set at Parse and at Apply time).
func (f *FeeSettings) IsUsingModernFees() bool {
	return f.XRPFeesMode
}

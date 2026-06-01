package state

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
}

// Ledger entry type for FeeSettings
const ledgerEntryTypeFeeSettings uint16 = 0x0073

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
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		// Read field header
		header := data[offset]
		offset++

		// Decode type and field from header
		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		// Handle extended type codes
		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		// Handle extended field codes
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		// Parse field based on type
		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return fee, nil
			}
			value := binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
			if fieldCode == fieldCodeLedgerEntryType {
				if value != ledgerEntryTypeFeeSettings {
					return nil, errors.New("not a FeeSettings entry")
				}
			}

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return fee, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 5: // PreviousTxnLgrSeq
				fee.PreviousTxnLgrSeq = value
			case fieldCodeReferenceFeeUnits:
				fee.ReferenceFeeUnits = value
			case fieldCodeReserveBase:
				fee.ReserveBase = value
			case fieldCodeReserveIncrement:
				fee.ReserveIncrement = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return fee, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			if fieldCode == fieldCodeBaseFee {
				fee.BaseFee = value
			}

		case FieldTypeAmount:
			// XRP amounts are 8 bytes
			if offset+8 > len(data) {
				return fee, nil
			}
			// Check if it's XRP (first bit is 0)
			if data[offset]&0x80 == 0 {
				// XRP amount - 8 bytes
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				// Clear the top bit and extract drops
				drops := rawAmount & 0x3FFFFFFFFFFFFFFF
				switch fieldCode {
				case fieldCodeBaseFeeDrops:
					fee.BaseFeeDrops = drops
					fee.XRPFeesMode = true
				case fieldCodeReserveBaseDrops:
					fee.ReserveBaseDrops = drops
					fee.XRPFeesMode = true
				case fieldCodeReserveIncrementDrops:
					fee.ReserveIncrementDrops = drops
					fee.XRPFeesMode = true
				}
				offset += 8
			} else {
				// IOU amount - skip 48 bytes (shouldn't appear in FeeSettings)
				offset += 48
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return fee, nil
			}
			if fieldCode == 5 { // PreviousTxnID
				copy(fee.PreviousTxnID[:], data[offset:offset+32])
			}
			offset += 32

		default:
			// Unknown type - stop parsing
			return fee, nil
		}
	}

	return fee, nil
}

// SerializeFeeSettings serializes a FeeSettings to binary format. The active
// field set (modern triple under XRPFeesMode, legacy quad otherwise) is always
// emitted, including zero-valued fields — matching rippled's `set()` /
// `makeFieldAbsent()` semantics at Change.cpp:362-379.
func SerializeFeeSettings(fee *FeeSettings) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "FeeSettings",
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
// Returns the modern BaseFeeDrops if set, otherwise falls back to legacy BaseFee.
func (f *FeeSettings) GetBaseFee() uint64 {
	if f.BaseFeeDrops > 0 {
		return f.BaseFeeDrops
	}
	if f.BaseFee > 0 {
		return f.BaseFee
	}
	return 10 // Default: 10 drops
}

// GetReserveBase returns the account reserve base in drops.
// Returns the modern ReserveBaseDrops if set, otherwise falls back to legacy ReserveBase.
func (f *FeeSettings) GetReserveBase() uint64 {
	if f.ReserveBaseDrops > 0 {
		return f.ReserveBaseDrops
	}
	if f.ReserveBase > 0 {
		return uint64(f.ReserveBase)
	}
	return 10_000_000 // Default: 10 XRP
}

// GetReserveIncrement returns the owner reserve increment in drops.
// Returns the modern ReserveIncrementDrops if set, otherwise falls back to legacy ReserveIncrement.
func (f *FeeSettings) GetReserveIncrement() uint64 {
	if f.ReserveIncrementDrops > 0 {
		return f.ReserveIncrementDrops
	}
	if f.ReserveIncrement > 0 {
		return uint64(f.ReserveIncrement)
	}
	return 2_000_000 // Default: 2 XRP
}

// IsUsingModernFees returns true if the entry encodes the post-XRPFees field
// set. Authoritative source is XRPFeesMode (set at Parse and at Apply time).
func (f *FeeSettings) IsUsingModernFees() bool {
	return f.XRPFeesMode
}

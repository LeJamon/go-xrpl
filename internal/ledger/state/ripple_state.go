package state

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
)

// RippleState represents a trust line between two accounts
type RippleState struct {
	// Balance is the current balance of the trust line
	// Positive means LowAccount owes HighAccount
	// Negative means HighAccount owes LowAccount
	Balance Amount

	// LowLimit is the trust limit set by the low account
	LowLimit Amount

	// HighLimit is the trust limit set by the high account
	HighLimit Amount

	// LowNode is the directory node for the low account
	LowNode uint64

	// HighNode is the directory node for the high account
	HighNode uint64

	// Flags for the trust line
	Flags uint32

	// LowQualityIn/Out and HighQualityIn/Out for transfer rates
	LowQualityIn   uint32
	LowQualityOut  uint32
	HighQualityIn  uint32
	HighQualityOut uint32

	// PreviousTxnID is the hash of the previous transaction that modified this entry
	PreviousTxnID [32]byte

	// PreviousTxnLgrSeq is the ledger sequence of the previous transaction
	PreviousTxnLgrSeq uint32
}

// RippleState flags
const (
	LsfLowReserve     uint32 = 0x00010000
	LsfHighReserve    uint32 = 0x00020000
	LsfLowAuth        uint32 = 0x00040000
	LsfHighAuth       uint32 = 0x00080000
	LsfLowNoRipple    uint32 = 0x00100000
	LsfHighNoRipple   uint32 = 0x00200000
	LsfLowFreeze      uint32 = 0x00400000
	LsfHighFreeze     uint32 = 0x00800000
	LsfAMMNode        uint32 = 0x01000000 // Trustline is owned by an AMM
	LsfLowDeepFreeze  uint32 = 0x02000000
	LsfHighDeepFreeze uint32 = 0x04000000
)

// Ledger entry type code for RippleState
const ledgerEntryTypeRippleState = 0x0072

// Field codes for RippleState (based on XRPL binary serialization format)
const (
	fieldCodeRSBalance      = 2  // Amount field code for Balance
	fieldCodeLowLimit       = 6  // Amount field code for LowLimit
	fieldCodeHighLimit      = 7  // Amount field code for HighLimit
	fieldCodeLowNode        = 7  // UInt64 field code for LowNode
	fieldCodeHighNode       = 8  // UInt64 field code for HighNode
	fieldCodePrevTxnID      = 5  // Hash256 field code for PreviousTxnID
	fieldCodePrevTxnLgrSeq  = 5  // UInt32 field code for PreviousTxnLgrSeq
	fieldCodeHighQualityIn  = 16 // UInt32 field code for HighQualityIn (nth=16 in definitions.json)
	fieldCodeHighQualityOut = 17 // UInt32 field code for HighQualityOut (nth=17 in definitions.json)
	fieldCodeLowQualityIn   = 18 // UInt32 field code for LowQualityIn (nth=18 in definitions.json)
	fieldCodeLowQualityOut  = 19 // UInt32 field code for LowQualityOut (nth=19 in definitions.json)
)

// AccountOneAddress is the special issuer address used for Balance in RippleState
// This is ACCOUNT_ONE in rippled - a special address that represents no account
const AccountOneAddress = "rrrrrrrrrrrrrrrrrrrrBZbvji"

// Keep internal alias for backwards compatibility within the package
const accountOne = AccountOneAddress

// ParseRippleState parses a RippleState from binary data
func ParseRippleState(data []byte) (*RippleState, error) {
	if len(data) < 20 {
		return nil, errors.New("ripple state data too short")
	}

	rs := &RippleState{}
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

			if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return rs, nil
			}
			value := binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
			if fieldCode == fieldCodeLedgerEntryType {
				if value != ledgerEntryTypeRippleState {
					return nil, errors.New("not a RippleState entry")
				}
			}

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return rs, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case fieldCodeFlags:
				rs.Flags = value
			case fieldCodePrevTxnLgrSeq:
				rs.PreviousTxnLgrSeq = value
			case fieldCodeLowQualityIn:
				rs.LowQualityIn = value
			case fieldCodeLowQualityOut:
				rs.LowQualityOut = value
			case fieldCodeHighQualityIn:
				rs.HighQualityIn = value
			case fieldCodeHighQualityOut:
				rs.HighQualityOut = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return rs, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case fieldCodeLowNode:
				rs.LowNode = value
			case fieldCodeHighNode:
				rs.HighNode = value
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return rs, nil
			}
			if fieldCode == fieldCodePrevTxnID {
				copy(rs.PreviousTxnID[:], data[offset:offset+32])
			}
			offset += 32

		case FieldTypeAmount:
			// IOU amounts are 48 bytes
			if offset+48 > len(data) {
				// Try parsing as XRP (8 bytes) - should not happen for RippleState
				if offset+8 > len(data) {
					return rs, nil
				}
				offset += 8
				continue
			}

			amt, err := ParseIOUAmountBinary(data[offset : offset+48])
			if err != nil {
				offset += 48
				continue
			}

			switch fieldCode {
			case fieldCodeRSBalance:
				rs.Balance = amt
			case fieldCodeLowLimit:
				rs.LowLimit = amt
			case fieldCodeHighLimit:
				rs.HighLimit = amt
			}
			offset += 48

		default:
			return rs, nil
		}
	}

	return rs, nil
}

// ParseIOUAmountBinary parses an IOU amount from 48 bytes of binary data
// and returns a clean Amount with mantissa/exponent representation.
func ParseIOUAmountBinary(data []byte) (Amount, error) {
	if len(data) != 48 {
		return Amount{}, errors.New("invalid IOU amount length")
	}

	// First 8 bytes: value (mantissa + exponent)
	// Bytes 8-27: currency code (20 bytes / 160 bits)
	// Bytes 28-47: issuer account ID (20 bytes / 160 bits)

	// Parse currency from the 20-byte currency section (bytes 8-27)
	// Standard 3-char codes format: [12 zero bytes][3-char code][5 zero bytes]
	currency := ""
	isStandardCode := true
	for i := 8; i < 20; i++ {
		if data[i] != 0 {
			isStandardCode = false
			break
		}
	}
	if isStandardCode {
		currency = string(data[20:23])
	} else {
		currency = strings.ToUpper(hex.EncodeToString(data[8:28]))
	}

	// Parse issuer (last 20 bytes)
	var issuerID [20]byte
	copy(issuerID[:], data[28:48])
	issuer, _ := encodeAccountID(issuerID)

	// Parse value from first 8 bytes
	// Bit 63: not XRP (always 1 for IOU)
	// Bit 62: sign (1 = positive)
	// Bits 54-61: exponent (8 bits, biased by 97)
	// Bits 0-53: mantissa (54 bits)
	rawValue := binary.BigEndian.Uint64(data[0:8])

	if rawValue == 0x8000000000000000 { // Zero
		return NewIssuedAmountFromValue(0, zeroExponent, currency, issuer), nil
	}

	positive := (rawValue & 0x4000000000000000) != 0
	exponent := int((rawValue>>54)&0xFF) - 97
	mantissa := int64(rawValue & 0x003FFFFFFFFFFFFF)

	if !positive {
		mantissa = -mantissa
	}

	return NewIssuedAmountFromValue(mantissa, exponent, currency, issuer), nil
}

// serializeAmount serializes an Amount to a map suitable for binarycodec.Encode
func serializeAmount(amount Amount, currency string, useAccountOne bool) map[string]any {
	valueStr := amount.Value()
	curr := currency
	if curr == "" {
		curr = amount.Currency
	}
	issuer := amount.Issuer
	if useAccountOne {
		issuer = accountOne
	}
	return map[string]any{
		"value":    valueStr,
		"currency": curr,
		"issuer":   issuer,
	}
}

// ParseRippleStateFromBytes parses a RippleState from binary data (delegates to ParseRippleState)
func ParseRippleStateFromBytes(data []byte) (*RippleState, error) {
	return ParseRippleState(data)
}

// SerializeRippleState serializes a RippleState to binary
func SerializeRippleState(rs *RippleState) ([]byte, error) {
	// Use Balance's currency for all amounts (LowLimit/HighLimit may have been parsed with null currency)
	currency := rs.Balance.Currency
	if currency == "" || currency == "\x00\x00\x00" {
		if rs.LowLimit.Currency != "" && rs.LowLimit.Currency != "\x00\x00\x00" {
			currency = rs.LowLimit.Currency
		} else if rs.HighLimit.Currency != "" && rs.HighLimit.Currency != "\x00\x00\x00" {
			currency = rs.HighLimit.Currency
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "RippleState",
		"Flags":           rs.Flags,
		"Balance":         serializeAmount(rs.Balance, currency, true),
		"LowLimit":        serializeAmount(rs.LowLimit, currency, false),
		"HighLimit":       serializeAmount(rs.HighLimit, currency, false),
		"LowNode":         fmt.Sprintf("%x", rs.LowNode),
		"HighNode":        fmt.Sprintf("%x", rs.HighNode),
	}

	if rs.LowQualityIn != 0 {
		jsonObj["LowQualityIn"] = rs.LowQualityIn
	}
	if rs.LowQualityOut != 0 {
		jsonObj["LowQualityOut"] = rs.LowQualityOut
	}
	if rs.HighQualityIn != 0 {
		jsonObj["HighQualityIn"] = rs.HighQualityIn
	}
	if rs.HighQualityOut != 0 {
		jsonObj["HighQualityOut"] = rs.HighQualityOut
	}

	if rs.PreviousTxnID != [32]byte{} {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(rs.PreviousTxnID[:]))
	}

	if rs.PreviousTxnLgrSeq != 0 {
		jsonObj["PreviousTxnLgrSeq"] = rs.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode RippleState: %w", err)
	}

	return hex.DecodeString(hexStr)
}

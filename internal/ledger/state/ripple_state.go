package state

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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

// RippleState flags.
const (
	LsfLowReserve     = entry.LsfLowReserve
	LsfHighReserve    = entry.LsfHighReserve
	LsfLowAuth        = entry.LsfLowAuth
	LsfHighAuth       = entry.LsfHighAuth
	LsfLowNoRipple    = entry.LsfLowNoRipple
	LsfHighNoRipple   = entry.LsfHighNoRipple
	LsfLowFreeze      = entry.LsfLowFreeze
	LsfHighFreeze     = entry.LsfHighFreeze
	LsfAMMNode        = entry.LsfAMMNode
	LsfLowDeepFreeze  = entry.LsfLowDeepFreeze
	LsfHighDeepFreeze = entry.LsfHighDeepFreeze
)

// Ledger entry type code for RippleState
const ledgerEntryTypeRippleState = uint16(entry.TypeRippleState)

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

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt16:
			if f.FieldCode == fieldCodeLedgerEntryType {
				if f.UInt16() != ledgerEntryTypeRippleState {
					return errors.New("not a RippleState entry")
				}
			}

		case stUInt32:
			switch f.FieldCode {
			case fieldCodeFlags:
				rs.Flags = f.UInt32()
			case fieldCodePrevTxnLgrSeq:
				rs.PreviousTxnLgrSeq = f.UInt32()
			case fieldCodeLowQualityIn:
				rs.LowQualityIn = f.UInt32()
			case fieldCodeLowQualityOut:
				rs.LowQualityOut = f.UInt32()
			case fieldCodeHighQualityIn:
				rs.HighQualityIn = f.UInt32()
			case fieldCodeHighQualityOut:
				rs.HighQualityOut = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case fieldCodeLowNode:
				rs.LowNode = f.UInt64()
			case fieldCodeHighNode:
				rs.HighNode = f.UInt64()
			}

		case stHash256:
			if f.FieldCode == fieldCodePrevTxnID {
				rs.PreviousTxnID = f.Hash256()
			}

		case stAmount:
			// RippleState's Balance/LowLimit/HighLimit are IOU amounts (48
			// bytes); a non-IOU value is foreign here and is skipped.
			if len(f.Value) != 48 {
				return nil
			}
			amt, err := ParseIOUAmountBinary(f.Value)
			if err != nil {
				// A trust line whose Balance/limit fails to parse is corrupt;
				// returning a zero amount with no error would silently diverge
				// the trust line's state from the ledger.
				return fmt.Errorf("RippleState amount (field %d) parse failed: %w", f.FieldCode, err)
			}
			switch f.FieldCode {
			case fieldCodeRSBalance:
				rs.Balance = amt
			case fieldCodeLowLimit:
				rs.LowLimit = amt
			case fieldCodeHighLimit:
				rs.HighLimit = amt
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
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

	// Validate bounds before calling NewIssuedAmountFromValue, which panics
	// on overflow (matching rippled's Throw<std::overflow_error>). When parsing
	// binary data from the ledger, a panic would crash the server. Return an
	// error instead for out-of-range values that normalization cannot fix.
	//
	// The raw 54-bit mantissa can be up to ~1.8e16. Normalization divides by 10
	// until mantissa <= MaxMantissa (9.999e15), incrementing exponent each time.
	// If mantissa > MaxMantissa and exponent >= MaxExponent, normalization would
	// need to increment exponent past MaxExponent, causing overflow. Similarly,
	// if exponent already exceeds MaxExponent, it will always overflow.
	absMantissa := mantissa
	if absMantissa < 0 {
		absMantissa = -absMantissa
	}
	if absMantissa != 0 && (exponent > MaxExponent || (exponent >= MaxExponent && absMantissa > MaxMantissa)) {
		return Amount{}, fmt.Errorf("IOU amount overflow: mantissa %d exponent %d exceeds representable range", mantissa, exponent)
	}

	return NewIssuedAmountFromValue(mantissa, exponent, currency, issuer), nil
}

// ParseMPTAmountBinary parses an MPT amount from 33 bytes of binary data.
// Format: 1 byte header + 8 bytes value + 24 bytes issuance ID.
// Header byte: bit 0x40 = positive sign (0x60 positive, 0x20 zero, 0x00 negative).
// Value: 8-byte big-endian int64 (unsigned magnitude).
// Issuance ID: 24-byte MPT issuance ID (4 bytes sequence + 20 bytes issuer account).
func ParseMPTAmountBinary(data []byte) (Amount, error) {
	if len(data) != 33 {
		return Amount{}, errors.New("invalid MPT amount length: expected 33 bytes")
	}

	header := data[0]
	positive := (header & 0x40) != 0

	// Parse 8-byte value as big-endian uint64
	msb := binary.BigEndian.Uint32(data[1:5])
	lsb := binary.BigEndian.Uint32(data[5:9])
	msbBig := new(big.Int).SetUint64(uint64(msb))
	lsbBig := new(big.Int).SetUint64(uint64(lsb))
	shifted := new(big.Int).Lsh(msbBig, 32)
	num := new(big.Int).Or(shifted, lsbBig)

	value := num.Int64()
	if !positive {
		value = -value
	}

	// Parse 24-byte issuance ID (hex-encoded, uppercase)
	issuanceID := strings.ToUpper(hex.EncodeToString(data[9:33]))

	// Build Amount directly to set the private mptIssuanceID field.
	iouVal := NewIOUAmountValue(value, 0)
	raw := value
	return Amount{
		iou:           iouVal,
		Native:        false,
		mptRaw:        &raw,
		mptIssuanceID: issuanceID,
	}, nil
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

package state

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/keylet"
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

	LowNode    uint64
	HasLowNode bool

	HighNode    uint64
	HasHighNode bool

	// Flags for the trust line
	Flags uint32

	// LowQualityIn/Out and HighQualityIn/Out for transfer rates
	LowQualityIn      uint32
	HasLowQualityIn   bool
	LowQualityOut     uint32
	HasLowQualityOut  bool
	HighQualityIn     uint32
	HasHighQualityIn  bool
	HighQualityOut    uint32
	HasHighQualityOut bool

	HighSponsor string
	LowSponsor  string

	// PreviousTxnID is the hash of the previous transaction that modified this entry
	PreviousTxnID [32]byte

	// PreviousTxnLgrSeq is the ledger sequence of the previous transaction
	PreviousTxnLgrSeq uint32
	decoded           entry.RippleState
	binaryBadCurrency bool
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

// AccountOneAddress is the special issuer address used for Balance in RippleState
// This is ACCOUNT_ONE in rippled - a special address that represents no account
const AccountOneAddress = "rrrrrrrrrrrrrrrrrrrrBZbvji"

// Keep internal alias for backwards compatibility within the package
const accountOne = AccountOneAddress

const badCurrencyHex = "0000000000000000000000005852500000000000"

// ParseRippleState parses a RippleState from binary data
func ParseRippleState(data []byte) (*RippleState, error) {
	var decoded entry.RippleState
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode RippleState: %w", err)
	}
	decodeIssued := func(field string, value entry.AmountValue) (Amount, error) {
		amount, err := decodeLedgerAmount("RippleState."+field, value)
		if err != nil {
			return Amount{}, err
		}
		if amount.IsNative() || amount.IsMPT() {
			return Amount{}, fmt.Errorf("RippleState.%s: expected issued-currency amount", field)
		}
		return amount, nil
	}
	balanceValue, err := decoded.GetBalance()
	if err != nil {
		return nil, err
	}
	balance, err := decodeIssued("Balance", balanceValue)
	if err != nil {
		return nil, err
	}
	lowLimitValue, err := decoded.GetLowLimit()
	if err != nil {
		return nil, err
	}
	lowLimit, err := decodeIssued("LowLimit", lowLimitValue)
	if err != nil {
		return nil, err
	}
	highLimitValue, err := decoded.GetHighLimit()
	if err != nil {
		return nil, err
	}
	highLimit, err := decodeIssued("HighLimit", highLimitValue)
	if err != nil {
		return nil, err
	}
	badCurrencyAmounts := 0
	for _, amount := range []Amount{balance, lowLimit, highLimit} {
		if amount.Currency == badCurrencyHex {
			badCurrencyAmounts++
		}
	}
	if badCurrencyAmounts != 0 && badCurrencyAmounts != 3 {
		return nil, errors.New("RippleState has inconsistent badCurrency amounts")
	}

	rs := &RippleState{
		Balance:           balance,
		LowLimit:          lowLimit,
		HighLimit:         highLimit,
		Flags:             decoded.Flags,
		LowQualityIn:      decoded.LowQualityIn,
		LowQualityOut:     decoded.LowQualityOut,
		HighQualityIn:     decoded.HighQualityIn,
		HighQualityOut:    decoded.HighQualityOut,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
		HasLowNode:        decoded.HasLowNode(),
		HasHighNode:       decoded.HasHighNode(),
		HasLowQualityIn:   decoded.HasLowQualityIn(),
		HasLowQualityOut:  decoded.HasLowQualityOut(),
		HasHighQualityIn:  decoded.HasHighQualityIn(),
		HasHighQualityOut: decoded.HasHighQualityOut(),
		decoded:           decoded,
		binaryBadCurrency: badCurrencyAmounts == 3,
	}
	if rs.HasLowNode {
		rs.LowNode, err = decoded.GetLowNode()
		if err != nil {
			return nil, err
		}
	}
	if rs.HasHighNode {
		rs.HighNode, err = decoded.GetHighNode()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasHighSponsor() {
		rs.HighSponsor = decoded.HighSponsor
	}
	if decoded.HasLowSponsor() {
		rs.LowSponsor = decoded.LowSponsor
	}
	if decoded.HasPreviousTxnID() {
		rs.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
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
	amount, err := parseCanonicalAmountBinary(data)
	if err != nil {
		return Amount{}, fmt.Errorf("invalid IOU amount: %w", err)
	}
	if amount.IsNative() || amount.IsMPT() {
		return Amount{}, errors.New("invalid IOU amount: expected issued currency")
	}
	return amount, nil
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
	amount, err := parseCanonicalAmountBinary(data)
	if err != nil {
		return Amount{}, fmt.Errorf("invalid MPT amount: %w", err)
	}
	if !amount.IsMPT() {
		return Amount{}, errors.New("invalid MPT amount: expected MPToken issuance")
	}
	return amount, nil
}

func parseCanonicalAmountBinary(data []byte) (Amount, error) {
	parser := serdes.NewBinaryParser(data, definitions.Get())
	decoded, err := (&types.Amount{}).ToJSON(parser)
	if err != nil {
		return Amount{}, err
	}
	if parser.Remaining() != 0 {
		return Amount{}, errors.New("trailing amount bytes")
	}
	amountValue, err := entry.ParseAmountValue(decoded)
	if err != nil {
		return Amount{}, err
	}
	return decodeLedgerAmount("Amount", amountValue)
}

func serializeAmountValue(amount Amount, currency string, useAccountOne bool) entry.AmountValue {
	value := amount.LedgerValue()
	if currency != "" {
		value.Currency = currency
	}
	if useAccountOne {
		value.Issuer = accountOne
	}
	return value
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
	binaryBadCurrency := rs.binaryBadCurrency && currency == badCurrencyHex
	encodedCurrency := currency
	if binaryBadCurrency {
		encodedCurrency = "USD"
	}

	entry := rs.decoded
	entry.SetFlagsValue(rs.Flags)
	if err := entry.SetBalanceValue(serializeAmountValue(rs.Balance, encodedCurrency, true)); err != nil {
		return nil, err
	}
	if err := entry.SetLowLimitValue(serializeAmountValue(rs.LowLimit, encodedCurrency, false)); err != nil {
		return nil, err
	}
	if err := entry.SetHighLimitValue(serializeAmountValue(rs.HighLimit, encodedCurrency, false)); err != nil {
		return nil, err
	}
	if rs.HasLowNode || rs.LowNode != 0 {
		entry.SetLowNodeValue(rs.LowNode)
	} else {
		entry.ClearLowNode()
	}
	if rs.HasHighNode || rs.HighNode != 0 {
		entry.SetHighNodeValue(rs.HighNode)
	} else {
		entry.ClearHighNode()
	}
	if rs.HasLowQualityIn || rs.LowQualityIn != 0 {
		entry.SetLowQualityInValue(rs.LowQualityIn)
	} else {
		entry.ClearLowQualityIn()
	}
	if rs.HasLowQualityOut || rs.LowQualityOut != 0 {
		entry.SetLowQualityOutValue(rs.LowQualityOut)
	} else {
		entry.ClearLowQualityOut()
	}
	if rs.HasHighQualityIn || rs.HighQualityIn != 0 {
		entry.SetHighQualityInValue(rs.HighQualityIn)
	} else {
		entry.ClearHighQualityIn()
	}
	if rs.HasHighQualityOut || rs.HighQualityOut != 0 {
		entry.SetHighQualityOutValue(rs.HighQualityOut)
	} else {
		entry.ClearHighQualityOut()
	}
	if rs.HighSponsor != "" || (entry.HasHighSponsor() && rs.HighSponsor == entry.HighSponsor) {
		entry.SetHighSponsor(rs.HighSponsor)
	} else {
		entry.ClearHighSponsor()
	}
	if rs.LowSponsor != "" || (entry.HasLowSponsor() && rs.LowSponsor == entry.LowSponsor) {
		entry.SetLowSponsor(rs.LowSponsor)
	} else {
		entry.ClearLowSponsor()
	}

	originalPreviousTxnID := [32]byte{}
	if entry.HasPreviousTxnID() {
		var err error
		originalPreviousTxnID, err = entry.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if rs.PreviousTxnID != [32]byte{} || (entry.HasPreviousTxnID() && rs.PreviousTxnID == originalPreviousTxnID) {
		entry.SetPreviousTxnIDValue(rs.PreviousTxnID)
	} else {
		entry.SetPreviousTxnIDValue([32]byte{})
	}
	if rs.PreviousTxnLgrSeq != 0 || (entry.HasPreviousTxnLgrSeq() && rs.PreviousTxnLgrSeq == entry.PreviousTxnLgrSeq) {
		entry.SetPreviousTxnLgrSeqValue(rs.PreviousTxnLgrSeq)
	} else {
		entry.SetPreviousTxnLgrSeqValue(0)
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, err
	}
	if !binaryBadCurrency {
		return data, nil
	}

	badCurrency := keylet.BadCurrency()
	amounts := 0
	err = WalkFields(data, func(field Field) error {
		if field.TypeCode != stAmount {
			return nil
		}
		switch field.FieldCode {
		case 2, 6, 7:
			if len(field.Value) != types.CurrencyAmountByteLength {
				return fmt.Errorf("RippleState amount field %d is not issued currency", field.FieldCode)
			}
			copy(field.Value[8:28], badCurrency[:])
			amounts++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if amounts != 3 {
		return nil, fmt.Errorf("RippleState badCurrency encoding patched %d amounts, want 3", amounts)
	}
	return data, nil
}

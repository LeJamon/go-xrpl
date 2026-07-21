package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

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

	// Reserve sponsors for the high and low trust-line owners.
	HighSponsor string
	LowSponsor  string

	// PreviousTxnID is the hash of the previous transaction that modified this entry
	PreviousTxnID [32]byte

	// PreviousTxnLgrSeq is the ledger sequence of the previous transaction
	PreviousTxnLgrSeq uint32
	decodedOptionals  map[string]any
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
	fields := decoded.ToMap()
	decodeIssued := func(field string, value any) (Amount, error) {
		amount, err := decodeLedgerAmount("RippleState."+field, value)
		if err != nil {
			return Amount{}, err
		}
		if amount.IsNative() || amount.IsMPT() {
			return Amount{}, fmt.Errorf("RippleState.%s: expected issued-currency amount", field)
		}
		return amount, nil
	}
	balance, err := decodeIssued("Balance", decoded.Balance)
	if err != nil {
		return nil, err
	}
	lowLimit, err := decodeIssued("LowLimit", decoded.LowLimit)
	if err != nil {
		return nil, err
	}
	highLimit, err := decodeIssued("HighLimit", decoded.HighLimit)
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
		HighSponsor:       decoded.HighSponsor,
		LowSponsor:        decoded.LowSponsor,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
		decodedOptionals:  make(map[string]any),
		binaryBadCurrency: badCurrencyAmounts == 3,
	}
	if _, ok := fields["LowNode"]; ok {
		rs.LowNode, err = parseLedgerUint64("RippleState.LowNode", decoded.LowNode)
		if err != nil {
			return nil, err
		}
		rs.HasLowNode = true
	}
	if _, ok := fields["HighNode"]; ok {
		rs.HighNode, err = parseLedgerUint64("RippleState.HighNode", decoded.HighNode)
		if err != nil {
			return nil, err
		}
		rs.HasHighNode = true
	}
	_, rs.HasLowQualityIn = fields["LowQualityIn"]
	_, rs.HasLowQualityOut = fields["LowQualityOut"]
	_, rs.HasHighQualityIn = fields["HighQualityIn"]
	_, rs.HasHighQualityOut = fields["HighQualityOut"]
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("RippleState.PreviousTxnID", decoded.PreviousTxnID, rs.PreviousTxnID[:]); err != nil {
			return nil, err
		}
		rs.decodedOptionals["PreviousTxnID"] = rs.PreviousTxnID
	}
	if _, ok := fields["PreviousTxnLgrSeq"]; ok {
		rs.decodedOptionals["PreviousTxnLgrSeq"] = rs.PreviousTxnLgrSeq
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
	return decodeLedgerAmount("Amount", decoded)
}

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

	entry := &entry.RippleState{}
	entry.SetFlags(rs.Flags)
	entry.SetBalance(serializeAmount(rs.Balance, encodedCurrency, true))
	entry.SetLowLimit(serializeAmount(rs.LowLimit, encodedCurrency, false))
	entry.SetHighLimit(serializeAmount(rs.HighLimit, encodedCurrency, false))
	if rs.HasLowNode || rs.LowNode != 0 {
		entry.SetLowNode(fmt.Sprintf("%x", rs.LowNode))
	}
	if rs.HasHighNode || rs.HighNode != 0 {
		entry.SetHighNode(fmt.Sprintf("%x", rs.HighNode))
	}
	if rs.HasLowQualityIn || rs.LowQualityIn != 0 {
		entry.SetLowQualityIn(rs.LowQualityIn)
	}
	if rs.HasLowQualityOut || rs.LowQualityOut != 0 {
		entry.SetLowQualityOut(rs.LowQualityOut)
	}
	if rs.HasHighQualityIn || rs.HighQualityIn != 0 {
		entry.SetHighQualityIn(rs.HighQualityIn)
	}
	if rs.HasHighQualityOut || rs.HighQualityOut != 0 {
		entry.SetHighQualityOut(rs.HighQualityOut)
	}
	if rs.HighSponsor != "" {
		entry.SetHighSponsor(rs.HighSponsor)
	}
	if rs.LowSponsor != "" {
		entry.SetLowSponsor(rs.LowSponsor)
	}

	if rs.PreviousTxnID != [32]byte{} || decodedFieldUnchanged(rs.decodedOptionals, "PreviousTxnID", rs.PreviousTxnID) {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(rs.PreviousTxnID[:])))
	}
	if rs.PreviousTxnLgrSeq != 0 || decodedFieldUnchanged(rs.decodedOptionals, "PreviousTxnLgrSeq", rs.PreviousTxnLgrSeq) {
		entry.SetPreviousTxnLgrSeq(rs.PreviousTxnLgrSeq)
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

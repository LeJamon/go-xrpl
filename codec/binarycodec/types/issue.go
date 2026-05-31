package types

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types/interfaces"
)

const (
	// MPTIssuanceIDBytesLength is the number of bytes for an MPT issuance ID.
	MPTIssuanceIDBytesLength = 24
)

var (
	// NoAccountBytes is the marker used to identify MPT issues in the binary format.
	// This is the special account ID "0000000000000000000000000000000000000001".
	NoAccountBytes = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
)

var (
	// ErrInvalidIssueObject is returned when the JSON object is not a valid Issue.
	// ErrInvalidIssueObject is returned when the JSON object is not a valid Issue.
	ErrInvalidIssueObject = errors.New("invalid issue object")
	// ErrInvalidCurrency is returned when the currency field is missing or invalid in the Issue JSON.
	ErrInvalidCurrency = errors.New("invalid currency")
	// ErrInvalidIssuer is returned when the issuer field is missing or invalid in the Issue JSON.
	ErrInvalidIssuer = errors.New("invalid issuer")
	// ErrMissingIssueLengthOption is returned when no length option is provided to Issue.ToJSON.
	ErrMissingIssueLengthOption = errors.New("missing length option for Issue.ToJSON")
	// XRPBytes is the serialized byte representation for native XRP (zero-value currency issuer).
	XRPBytes = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
)

// Issue represents an XRPL Issue, which is essentially an AccountID.
// It is used to identify the issuer of a currency in the XRPL.
// The FromJson method converts a classic address string to an AccountID byte slice.
// The ToJson method converts an AccountID byte slice back to a classic address string.
// This type is crucial for handling currency issuers in XRPL transactions and ledger entries.
type Issue struct {
	length int
}

// FromJSON parses a classic address string and returns the corresponding AccountID byte slice.
// It uses the addresscodec package to decode the classic address.
// If the input is not a valid classic address, it returns an error.
func (i *Issue) FromJSON(json any) ([]byte, error) {
	if !i.isIssueObject(json) {
		return nil, ErrInvalidIssueObject
	}

	mapObj, ok := json.(map[string]any)
	if !ok {
		return nil, ErrInvalidIssueObject
	}

	currency, ok := mapObj["currency"]
	if !ok {
		mptIssuanceID, ok := mapObj["mpt_issuance_id"].(string)
		if !ok {
			return nil, ErrInvalidCurrency
		}

		mptIssuanceIDBytes, err := hex.DecodeString(mptIssuanceID)
		if err != nil {
			return nil, err
		}
		if len(mptIssuanceIDBytes) != MPTIssuanceIDBytesLength {
			return nil, ErrInvalidCurrency
		}

		// Reconstruct the 44-byte wire form — the inverse of ToJSON and matching
		// rippled's STIssue::add for an MPT asset (STIssue.cpp): issuer (160 bits)
		// + the noAccount black-hole marker (160 bits) + the 32-bit sequence.
		// mpt_issuance_id is sequence(big-endian, 4) + issuer(20); ToJSON read the
		// wire sequence little-endian, so swap it back so the round-trip is exact.
		seq := binary.BigEndian.Uint32(mptIssuanceIDBytes[:4])
		issuer := mptIssuanceIDBytes[4:]
		seqLE := make([]byte, 4)
		binary.LittleEndian.PutUint32(seqLE, seq)

		wire := make([]byte, 0, 2*len(NoAccountBytes)+4)
		wire = append(wire, issuer...)
		wire = append(wire, NoAccountBytes...)
		wire = append(wire, seqLE...)

		i.length = len(wire)

		return wire, nil
	}

	currencyCodec := &Currency{}

	currencyBytes, err := currencyCodec.FromJSON(currency)
	if err != nil {
		return nil, err
	}

	issuer, ok := mapObj["issuer"]
	if issuerString, okstring := issuer.(string); ok && okstring {
		_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerString)
		if err != nil {
			return nil, err
		}

		return append(currencyBytes, issuerBytes...), nil
	}

	return currencyBytes, nil
}

// ToJSON converts a binary Issue representation back to a JSON object.
// It self-determines the length by progressively reading and checking the data:
// - XRP: 20 bytes (currency only, all zeros)
// - IOU: 40 bytes (currency + issuer)
// - MPT: 44 bytes (issuer account + NO_ACCOUNT marker + sequence)
// The opts parameter is ignored as length is determined automatically.
func (i *Issue) ToJSON(p interfaces.BinaryParser, opts ...int) (any, error) {
	// Step 1: Read first 20 bytes (currency for XRP/IOU, or issuer account for MPT)
	currencyOrAccount, err := p.ReadBytes(20)
	if err != nil {
		return nil, err
	}

	// Step 2: Check if it's XRP (all zeros)
	if bytes.Equal(currencyOrAccount, XRPBytes) {
		return map[string]any{
			"currency": "XRP",
		}, nil
	}

	// Step 3: Read next 20 bytes (issuer for IOU, or NO_ACCOUNT marker for MPT)
	issuerOrNoAccount, err := p.ReadBytes(20)
	if err != nil {
		return nil, err
	}

	// Step 4: Check if it's MPT (NO_ACCOUNT marker)
	if bytes.Equal(issuerOrNoAccount, NoAccountBytes) {
		// MPT case - read 4 more bytes for sequence (stored in little-endian)
		sequenceBytes, err := p.ReadBytes(4)
		if err != nil {
			return nil, err
		}

		// Convert sequence from little-endian to big-endian for mpt_issuance_id
		sequence := binary.LittleEndian.Uint32(sequenceBytes)
		seqBE := make([]byte, 4)
		binary.BigEndian.PutUint32(seqBE, sequence)

		// mpt_issuance_id = sequence (BE) + issuer account
		mptID := append(seqBE, currencyOrAccount...)
		return map[string]any{
			"mpt_issuance_id": strings.ToUpper(hex.EncodeToString(mptID)),
		}, nil
	}

	// Step 5: IOU case - decode currency and issuer
	// currencyOrAccount contains the currency bytes
	currencyStr, err := decodeCurrencyBytes(currencyOrAccount)
	if err != nil {
		return nil, err
	}

	// issuerOrNoAccount contains the issuer bytes
	address, err := addresscodec.Encode(issuerOrNoAccount, []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"currency": currencyStr,
		"issuer":   address,
	}, nil
}

// decodeCurrencyBytes decodes a 20-byte currency into its string representation,
// matching rippled's to_string(Currency) (UintTypes.cpp): a standard-form code
// (bytes 0-11 and 15-19 zero) renders as the 3-char ISO code only when those
// bytes are a printable currency code; everything else renders as full hex.
func decodeCurrencyBytes(currencyBytes []byte) (string, error) {
	if bytes.Equal(currencyBytes, XRPBytes) {
		return "XRP", nil
	}

	// Note: render as the 3-char form only for a printable ISO code. The prior
	// code returned bytes 12-14 for any non-zero values and built the string with
	// string(byte), which rune-encodes bytes >= 0x80 into multi-byte UTF-8 — both
	// produced a value Encode could not read back.
	if bytes.Equal(currencyBytes[0:12], make([]byte, 12)) &&
		bytes.Equal(currencyBytes[15:20], make([]byte, 5)) &&
		iouCodeRegex.Match(currencyBytes[12:15]) {
		return string(currencyBytes[12:15]), nil
	}

	return strings.ToUpper(hex.EncodeToString(currencyBytes)), nil
}

func (i *Issue) isIssueObject(obj any) bool {
	mapObj, ok := obj.(map[string]any)
	if !ok {
		return false
	}

	nKeys := len(mapObj)

	_, okMptIssuanceID := mapObj["mpt_issuance_id"]
	if nKeys == 1 && okMptIssuanceID {
		return true
	}

	_, okCurrency := mapObj["currency"]
	if nKeys == 1 && okCurrency {
		return true
	}

	_, okIssuer := mapObj["issuer"]
	if nKeys == 2 && okCurrency && okIssuer {
		return true
	}

	return false
}

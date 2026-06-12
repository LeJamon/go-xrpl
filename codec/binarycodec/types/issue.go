package types

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

var (
	// noAccountBytes is the marker used to identify MPT issues in the binary format.
	// This is the special account ID "0000000000000000000000000000000000000001".
	noAccountBytes = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}

	// noCurrencyBytes is rippled's noCurrency() sentinel — the 160-bit value 1
	// (UintTypes.cpp:126-130), which to_string(Currency) renders as "1".
	noCurrencyBytes = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
)

var (
	// ErrInvalidIssueObject is returned when the JSON object is not a valid Issue.
	ErrInvalidIssueObject = errors.New("invalid issue object")
	// ErrInvalidCurrency is returned when the currency field is missing or invalid in the Issue JSON.
	ErrInvalidCurrency = errors.New("invalid currency")
	// ErrInvalidIssuer is returned when the issuer field is missing or invalid in the Issue JSON.
	ErrInvalidIssuer = errors.New("invalid issuer")
)

// Issue represents an XRPL Issue: an asset identified either by a currency
// (with an issuer for IOUs) or by an MPT issuance ID. The wire form is 20
// bytes for XRP, 40 for an IOU and 44 for an MPT.
type Issue struct{}

// FromJSON converts an Issue JSON object ({"currency": ...}, {"currency": ...,
// "issuer": ...} or {"mpt_issuance_id": ...}) to its wire representation.
func (i *Issue) FromJSON(json any) ([]byte, error) {
	mapObj, ok := json.(map[string]any)
	if !ok || !i.isIssueObject(json) {
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
		if len(mptIssuanceIDBytes) != MPTIssuanceIDByteLength {
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

		wire := make([]byte, 0, 2*len(noAccountBytes)+4)
		wire = append(wire, issuer...)
		wire = append(wire, noAccountBytes...)
		wire = append(wire, seqLE...)

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
func (i *Issue) ToJSON(p *serdes.BinaryParser, opts ...int) (any, error) {
	// Step 1: Read first 20 bytes (currency for XRP/IOU, or issuer account for MPT)
	currencyOrAccount, err := p.ReadBytes(20)
	if err != nil {
		return nil, err
	}

	// Step 2: Check if it's XRP (all zeros)
	if bytes.Equal(currencyOrAccount, zeroByteArray) {
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
	if bytes.Equal(issuerOrNoAccount, noAccountBytes) {
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
	currencyStr, err := decodeCurrencyCode(currencyOrAccount)
	if err != nil {
		return nil, err
	}

	address, err := addresscodec.Encode(issuerOrNoAccount, []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"currency": currencyStr,
		"issuer":   address,
	}, nil
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

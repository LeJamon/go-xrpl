package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// LedgerOffer represents an offer stored in the ledger
type LedgerOffer struct {
	Account           string
	Sequence          uint32
	TakerPays         Amount // What the offer creator wants
	TakerGets         Amount // What the offer creator is selling
	BookDirectory     [32]byte
	BookNode          uint64
	OwnerNode         uint64
	Expiration        uint32
	Flags             uint32
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32

	// DomainID is the permissioned domain for this offer (optional, requires PermissionedDEX amendment)
	DomainID [32]byte

	// AdditionalBookDirectory and AdditionalBookNode are for hybrid offers
	// that are placed in both domain and open books
	AdditionalBookDirectory [32]byte
	AdditionalBookNode      uint64
}

// OfferCreate flags (kept here for backwards compatibility and external references)
const (
	OfferCreateFlagPassive           uint32 = 0x00010000
	OfferCreateFlagImmediateOrCancel uint32 = 0x00020000
	OfferCreateFlagFillOrKill        uint32 = 0x00040000
	OfferCreateFlagSell              uint32 = 0x00080000
)

// SerializeLedgerOffer serializes a LedgerOffer to binary for storage
func SerializeLedgerOffer(offer *LedgerOffer) ([]byte, error) {
	// Helper function to convert Amount to JSON format
	amountToJSON := func(amt Amount) any {
		if amt.IsNative() {
			return amt.Value()
		}
		return map[string]any{
			"value":    amt.Value(),
			"currency": amt.Currency,
			"issuer":   amt.Issuer,
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType":   "Offer",
		"Account":           offer.Account,
		"Flags":             offer.Flags,
		"Sequence":          offer.Sequence,
		"TakerPays":         amountToJSON(offer.TakerPays),
		"TakerGets":         amountToJSON(offer.TakerGets),
		"BookDirectory":     strings.ToUpper(hex.EncodeToString(offer.BookDirectory[:])),
		"BookNode":          fmt.Sprintf("%x", offer.BookNode),
		"OwnerNode":         fmt.Sprintf("%x", offer.OwnerNode),
		"PreviousTxnID":     strings.ToUpper(hex.EncodeToString(offer.PreviousTxnID[:])),
		"PreviousTxnLgrSeq": offer.PreviousTxnLgrSeq,
	}

	// Include optional fields only when set (non-zero)
	if offer.Expiration > 0 {
		jsonObj["Expiration"] = offer.Expiration
	}
	var zeroDomainID [32]byte
	if offer.DomainID != zeroDomainID {
		jsonObj["DomainID"] = strings.ToUpper(hex.EncodeToString(offer.DomainID[:]))
	}

	// Hybrid offers carry the open book they were also placed into as a
	// single-entry AdditionalBooks STArray of Book inner objects.
	var zeroBookDir [32]byte
	if offer.AdditionalBookDirectory != zeroBookDir {
		jsonObj["AdditionalBooks"] = []any{
			map[string]any{
				"Book": map[string]any{
					"BookDirectory": strings.ToUpper(hex.EncodeToString(offer.AdditionalBookDirectory[:])),
					"BookNode":      fmt.Sprintf("%x", offer.AdditionalBookNode),
				},
			},
		}
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Offer: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// parseLedgerOffer parses a LedgerOffer from binary data
func parseLedgerOffer(data []byte) (*LedgerOffer, error) {
	if len(data) < 20 {
		return nil, errors.New("offer data too short")
	}

	offer := &LedgerOffer{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case fieldCodeFlags:
				offer.Flags = f.UInt32()
			case 4: // Sequence
				offer.Sequence = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				offer.PreviousTxnLgrSeq = f.UInt32()
			case 10: // Expiration
				offer.Expiration = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 3: // BookNode
				offer.BookNode = f.UInt64()
			case 4: // OwnerNode
				offer.OwnerNode = f.UInt64()
			}

		case stHash256:
			switch f.FieldCode {
			case 16: // BookDirectory
				offer.BookDirectory = f.Hash256()
			case 5: // PreviousTxnID
				offer.PreviousTxnID = f.Hash256()
			case 34: // DomainID (PermissionedDEX)
				offer.DomainID = f.Hash256()
			}

		case stAmount:
			var amt Amount
			switch len(f.Value) {
			case 48: // IOU
				a, err := ParseIOUAmountBinary(f.Value)
				if err != nil {
					return nil
				}
				amt = a
			case 8: // XRP
				amt = NewXRPAmountFromInt(int64(xrpDrops(f.Value)))
			default:
				return nil
			}
			switch f.FieldCode {
			case 4: // TakerPays
				offer.TakerPays = amt
			case 5: // TakerGets
				offer.TakerGets = amt
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok && f.FieldCode == 1 {
				offer.Account, _ = EncodeAccountID(id)
			}

		case stArray:
			if f.FieldCode == 13 { // AdditionalBooks
				if err := parseAdditionalBooks(f.Value, offer); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return offer, nil
}

// parseAdditionalBooks records the first Book entry of an AdditionalBooks
// STArray onto offer; hybrid offers carry exactly one entry. content is the
// array's inner bytes as delimited by WalkFields.
func parseAdditionalBooks(content []byte, offer *LedgerOffer) error {
	first := true
	return WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject || elem.FieldCode != 36 || !first { // Book
			return nil
		}
		first = false
		return WalkFields(elem.Value, func(inner Field) error {
			switch inner.TypeCode {
			case stUInt64:
				if inner.FieldCode == 3 { // BookNode
					offer.AdditionalBookNode = inner.UInt64()
				}
			case stHash256:
				if inner.FieldCode == 16 { // BookDirectory
					offer.AdditionalBookDirectory = inner.Hash256()
				}
			}
			return nil
		})
	})
}

// ParseLedgerOfferFromBytes parses a LedgerOffer from binary data (exported)
func ParseLedgerOfferFromBytes(data []byte) (*LedgerOffer, error) {
	return parseLedgerOffer(data)
}

// ParseLedgerOffer is an alias for ParseLedgerOfferFromBytes
var ParseLedgerOffer = ParseLedgerOfferFromBytes

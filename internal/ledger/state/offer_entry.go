package state

import (
	"encoding/binary"
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

// ParseDropsString parses an XRP drops value from string
func ParseDropsString(s string) (uint64, error) {
	var drops uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid drops value")
		}
		drops = drops*10 + uint64(c-'0')
	}
	return drops, nil
}

// parseLedgerOffer parses a LedgerOffer from binary data
func parseLedgerOffer(data []byte) (*LedgerOffer, error) {
	if len(data) < 20 {
		return nil, errors.New("offer data too short")
	}

	offer := &LedgerOffer{}
	offset := 0

	for offset < len(data) {
		typeCode, fieldCode, newOffset, ok := parseFieldHeader(data, offset)
		offset = newOffset
		if !ok {
			break
		}

		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return offer, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return offer, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case fieldCodeFlags:
				offer.Flags = value
			case 4: // Sequence
				offer.Sequence = value
			case 5: // PreviousTxnLgrSeq (nth=5 in sfields.macro)
				offer.PreviousTxnLgrSeq = value
			case 10: // Expiration
				offer.Expiration = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return offer, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 3: // BookNode (nth=3 in definitions.json)
				offer.BookNode = value
			case 4: // OwnerNode (nth=4 in definitions.json)
				offer.OwnerNode = value
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return offer, nil
			}
			switch fieldCode {
			case 16: // BookDirectory (nth=16 in definitions.json)
				copy(offer.BookDirectory[:], data[offset:offset+32])
			case 5: // PreviousTxnID (nth=5 in definitions.json)
				copy(offer.PreviousTxnID[:], data[offset:offset+32])
			case 34: // DomainID (nth=34 in definitions.json, PermissionedDEX)
				copy(offer.DomainID[:], data[offset:offset+32])
			}
			offset += 32

		case FieldTypeAmount:
			// Determine if XRP (8 bytes) or IOU (48 bytes)
			if offset >= len(data) {
				return offer, nil
			}
			isIOU := (data[offset] & 0x80) != 0
			if isIOU {
				if offset+48 > len(data) {
					return offer, nil
				}
				amt, err := ParseIOUAmountBinary(data[offset : offset+48])
				if err == nil {
					switch fieldCode {
					case 4: // TakerPays
						offer.TakerPays = amt
					case 5: // TakerGets
						offer.TakerGets = amt
					}
				}
				offset += 48
			} else {
				if offset+8 > len(data) {
					return offer, nil
				}
				drops := binary.BigEndian.Uint64(data[offset:offset+8]) & 0x3FFFFFFFFFFFFFFF
				amt := NewXRPAmountFromInt(int64(drops))
				switch fieldCode {
				case 4: // TakerPays
					offer.TakerPays = amt
				case 5: // TakerGets
					offer.TakerGets = amt
				}
				offset += 8
			}

		case FieldTypeAccountID:
			// AccountID is VL-encoded, first byte is length (should be 0x14 = 20)
			if offset >= len(data) {
				return offer, nil
			}
			length := int(data[offset])
			offset++
			if length != 20 || offset+20 > len(data) {
				return offer, nil
			}
			var accountID [20]byte
			copy(accountID[:], data[offset:offset+20])
			address, _ := EncodeAccountID(accountID)
			if fieldCode == 1 { // Account (nth=1 in definitions.json)
				offer.Account = address
			}
			offset += 20

		case FieldTypeArray:
			if fieldCode == 13 { // AdditionalBooks (nth=13)
				offset = parseAdditionalBooks(data, offset, offer)
			} else {
				offset = skipArray(data, offset)
			}

		default:
			// Unknown type - cannot determine its width, so stop parsing.
			return offer, nil
		}
	}

	return offer, nil
}

// parseAdditionalBooks reads the AdditionalBooks STArray starting just after its
// field header and records the first Book's directory/node onto offer (hybrid
// offers carry exactly one entry). It returns the offset just past the array's
// end marker.
func parseAdditionalBooks(data []byte, offset int, offer *LedgerOffer) int {
	first := true
	for offset < len(data) {
		if data[offset] == arrayEndMarker {
			return offset + 1
		}
		typeCode, fieldCode, newOffset, ok := parseFieldHeader(data, offset)
		offset = newOffset
		if !ok {
			return offset
		}
		// Each entry is a Book inner object (type 14); decode its fields up to
		// the object end marker, keeping the first entry only.
		if typeCode == FieldTypeObject && fieldCode == 36 { // Book (nth=36)
			var dir [32]byte
			var node uint64
			offset = parseInnerBook(data, offset, &dir, &node)
			if first {
				offer.AdditionalBookDirectory = dir
				offer.AdditionalBookNode = node
				first = false
			}
			continue
		}
		return offset
	}
	return offset
}

// parseInnerBook decodes a Book inner object's fields starting just after its
// field header, until the object end marker. It returns the offset just past
// that marker.
func parseInnerBook(data []byte, offset int, dir *[32]byte, node *uint64) int {
	for offset < len(data) {
		if data[offset] == objectEndMarker {
			return offset + 1
		}
		typeCode, fieldCode, newOffset, ok := parseFieldHeader(data, offset)
		offset = newOffset
		if !ok {
			return offset
		}
		switch typeCode {
		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return offset
			}
			if fieldCode == 3 { // BookNode (nth=3)
				*node = binary.BigEndian.Uint64(data[offset : offset+8])
			}
			offset += 8
		case FieldTypeHash256:
			if offset+32 > len(data) {
				return offset
			}
			if fieldCode == 16 { // BookDirectory (nth=16)
				copy(dir[:], data[offset:offset+32])
			}
			offset += 32
		default:
			return offset
		}
	}
	return offset
}

// skipArray advances past an unrecognized STArray, starting just after the
// array's field header. It skips structurally — each entry is an inner object
// whose typed fields are skipped by width and which ends at the object end
// marker — so a payload byte that happens to equal a terminator cannot be
// mistaken for the array end. It returns the offset just past the array end
// marker.
func skipArray(data []byte, offset int) int {
	for offset < len(data) {
		if data[offset] == arrayEndMarker {
			return offset + 1
		}
		// Each array element is an inner object introduced by its field
		// header; consume that header, then its fields up to the object end.
		_, _, newOffset, ok := parseFieldHeader(data, offset)
		offset = newOffset
		if !ok {
			return offset
		}
		offset = skipObject(data, offset)
	}
	return offset
}

// skipObject advances past an inner object's fields, starting just after the
// object's field header, until (and including) the object end marker. Typed
// field payloads are skipped by their on-wire width so terminator bytes inside
// a payload are never treated as the object end.
func skipObject(data []byte, offset int) int {
	for offset < len(data) {
		if data[offset] == objectEndMarker {
			return offset + 1
		}
		typeCode, _, newOffset, ok := parseFieldHeader(data, offset)
		offset = newOffset
		if !ok {
			return offset
		}
		offset = skipFieldValue(data, offset, typeCode)
		if offset < 0 {
			return len(data)
		}
	}
	return offset
}

// skipFieldValue returns the offset just past a single field's payload of the
// given type. Nested objects and arrays recurse structurally. It returns -1 if
// the data is truncated or the type's width is unknown.
func skipFieldValue(data []byte, offset int, typeCode byte) int {
	switch typeCode {
	case FieldTypeUInt16:
		return advance(data, offset, 2)
	case FieldTypeUInt32:
		return advance(data, offset, 4)
	case FieldTypeUInt64:
		return advance(data, offset, 8)
	case FieldTypeHash128:
		return advance(data, offset, 16)
	case FieldTypeHash256:
		return advance(data, offset, 32)
	case FieldTypeAmount:
		if offset >= len(data) {
			return -1
		}
		if data[offset]&0x80 == 0 {
			return advance(data, offset, 8) // XRP
		}
		return advance(data, offset, 48) // IOU
	case FieldTypeBlob, FieldTypeAccountID:
		// Variable length: 1-byte (or extended) length prefix then payload.
		if offset >= len(data) {
			return -1
		}
		length := int(data[offset])
		extra := 1
		if length > 192 {
			if offset+1 >= len(data) {
				return -1
			}
			length = 193 + ((length-193)<<8 | int(data[offset+1]))
			extra = 2
		}
		return advance(data, offset, extra+length)
	case FieldTypeObject:
		return skipObject(data, offset)
	case FieldTypeArray:
		return skipArray(data, offset)
	default:
		return -1
	}
}

// advance returns offset+n, or -1 if that would run past the end of data.
func advance(data []byte, offset, n int) int {
	if offset+n > len(data) {
		return -1
	}
	return offset + n
}

// ParseLedgerOfferFromBytes parses a LedgerOffer from binary data (exported)
func ParseLedgerOfferFromBytes(data []byte) (*LedgerOffer, error) {
	return parseLedgerOffer(data)
}

// ParseLedgerOffer is an alias for ParseLedgerOfferFromBytes
var ParseLedgerOffer = ParseLedgerOfferFromBytes

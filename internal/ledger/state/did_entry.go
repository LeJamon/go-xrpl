package state

import (
	"encoding/binary"
	"encoding/hex"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// DIDData represents a DID ledger entry.
// Reference: rippled ledger_entries.macro ltDID
type DIDData struct {
	Account     [20]byte
	OwnerNode   uint64
	URI         string // hex-encoded
	DIDDocument string // hex-encoded
	Data        string // hex-encoded
}

// SerializeDID serializes a DID ledger entry using the binary codec.
func SerializeDID(did *DIDData, accountAddress string) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "DID",
		"Account":         accountAddress,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	if did.URI != "" {
		jsonObj["URI"] = did.URI
	}
	if did.DIDDocument != "" {
		jsonObj["DIDDocument"] = did.DIDDocument
	}
	if did.Data != "" {
		jsonObj["Data"] = did.Data
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// ParseDID parses a DID ledger entry from binary data.
func ParseDID(data []byte) (*DIDData, error) {
	did := &DIDData{}
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
				return did, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return did, nil
			}
			offset += 4

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return did, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			if fieldCode == 34 { // OwnerNode
				did.OwnerNode = value
			}
			offset += 8

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return did, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				if fieldCode == 1 { // Account
					copy(did.Account[:], data[offset:offset+20])
				}
				offset += 20
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return did, nil
			}
			offset += 32

		case FieldTypeBlob:
			if offset >= len(data) {
				return did, nil
			}
			length := int(data[offset])
			offset++
			if offset+length > len(data) {
				return did, nil
			}
			switch fieldCode {
			case 5: // URI (nth=5 in definitions.json)
				did.URI = hex.EncodeToString(data[offset : offset+length])
			case 26: // DIDDocument (nth=26 in definitions.json)
				did.DIDDocument = hex.EncodeToString(data[offset : offset+length])
			case 27: // Data (nth=27 in definitions.json)
				did.Data = hex.EncodeToString(data[offset : offset+length])
			}
			offset += length

		default:
			return did, nil
		}
	}

	return did, nil
}

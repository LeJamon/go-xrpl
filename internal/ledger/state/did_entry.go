package state

import (
	"encoding/hex"
	"strconv"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
		"OwnerNode":       strconv.FormatUint(did.OwnerNode, 16),
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

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				did.OwnerNode = f.UInt64()
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok && f.FieldCode == 1 { // Account
				did.Account = id
			}

		case stBlob:
			switch f.FieldCode {
			case 5: // URI
				did.URI = hex.EncodeToString(f.VLBytes())
			case 26: // DIDDocument
				did.DIDDocument = hex.EncodeToString(f.VLBytes())
			case 27: // Data
				did.Data = hex.EncodeToString(f.VLBytes())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return did, nil
}

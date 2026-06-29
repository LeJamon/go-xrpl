package state

import (
	"encoding/hex"
	"strconv"
	"strings"

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
	// PreviousTxnID / PreviousTxnLgrSeq thread the DID SLE's modification history.
	// They must round-trip so a no-op DIDSet (re-submitting current field values)
	// re-serializes byte-identically, letting the apply layer's unchanged-entry
	// guard prune it — matching rippled, which emits no ModifiedNode and threads
	// no PreviousTxnID when nothing changed (ApplyStateTable.cpp:154-157). Zero
	// when the DID has never been threaded; omitted on serialize in that case.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
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

	// Emit the threading pointers only when the DID has been threaded before (a
	// freshly created DID has neither until the apply layer stamps it), so a
	// no-op modification round-trips byte-identically and the apply layer's
	// unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	var emptyHash [32]byte
	if did.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(did.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = did.PreviousTxnLgrSeq
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
		case stUInt32:
			if f.FieldCode == 5 { // PreviousTxnLgrSeq
				did.PreviousTxnLgrSeq = f.UInt32()
			}

		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				did.OwnerNode = f.UInt64()
			}

		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				did.PreviousTxnID = f.Hash256()
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

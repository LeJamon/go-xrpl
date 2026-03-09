package did

import (
	"encoding/hex"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"strings"
)

// DIDEntry represents a DID ledger entry
// Reference: rippled ledger_entries.macro ltDID (0x0049)
type DIDEntry struct {
	// Required fields
	Account   [20]byte // The account that owns this DID
	OwnerNode uint64   // Directory page hint

	// Optional fields (stored as hex in the ledger, nil means not present)
	URI         *string // URI for the DID document (hex-encoded)
	DIDDocument *string // The DID document content (hex-encoded)
	Data        *string // Arbitrary data (hex-encoded)

	// Transaction threading
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// HasAnyField returns true if at least one optional field is present and non-empty
func (d *DIDEntry) HasAnyField() bool {
	return (d.URI != nil && *d.URI != "") ||
		(d.DIDDocument != nil && *d.DIDDocument != "") ||
		(d.Data != nil && *d.Data != "")
}

// parseDIDEntry parses a DID ledger entry from binary data
func parseDIDEntry(data []byte) (*DIDEntry, error) {
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, err
	}

	did := &DIDEntry{}

	// Parse Account
	if account, ok := jsonObj["Account"].(string); ok {
		accountID, err := state.DecodeAccountID(account)
		if err == nil {
			did.Account = accountID
		}
	}

	// Parse OwnerNode
	//TODO HANDLE ERROR
	if ownerNode, ok := jsonObj["OwnerNode"].(string); ok {
		did.OwnerNode, _ = tx.ParseUint64Hex(ownerNode)
	}

	// Parse URI (optional)
	if uri, ok := jsonObj["URI"].(string); ok {
		did.URI = &uri
	}

	if doc, ok := jsonObj["DIDDocument"].(string); ok {
		did.DIDDocument = &doc
	}

	// Parse Data (optional)
	if data, ok := jsonObj["Data"].(string); ok {
		did.Data = &data
	}

	// Parse PreviousTxnID
	if prevTxnID, ok := jsonObj["PreviousTxnID"].(string); ok {
		bytes, _ := hex.DecodeString(prevTxnID)
		copy(did.PreviousTxnID[:], bytes)
	}

	// Parse PreviousTxnLgrSeq
	if prevSeq, ok := jsonObj["PreviousTxnLgrSeq"].(float64); ok {
		did.PreviousTxnLgrSeq = uint32(prevSeq)
	}

	return did, nil
}

// serializeDIDEntry serializes a DID entry to binary format
func serializeDIDEntry(did *DIDEntry) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "DID",
	}

	// Add Account
	accountStr, err := state.EncodeAccountID(did.Account)
	if err == nil && accountStr != "" {
		jsonObj["Account"] = accountStr
	}

	// Add OwnerNode
	jsonObj["OwnerNode"] = tx.FormatUint64Hex(did.OwnerNode)

	// Add optional fields only if present and non-empty
	if did.URI != nil && *did.URI != "" {
		jsonObj["URI"] = strings.ToUpper(*did.URI)
	}

	if did.DIDDocument != nil && *did.DIDDocument != "" {
		jsonObj["DIDDocument"] = strings.ToUpper(*did.DIDDocument)
	}

	if did.Data != nil && *did.Data != "" {
		jsonObj["Data"] = strings.ToUpper(*did.Data)
	}

	// Add PreviousTxnID
	var zeroHash [32]byte
	if did.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(did.PreviousTxnID[:]))
	}

	// Add PreviousTxnLgrSeq
	if did.PreviousTxnLgrSeq > 0 {
		jsonObj["PreviousTxnLgrSeq"] = did.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// serializeDIDEntryFromFields creates and serializes a DID entry from a fields map
func serializeDIDEntryFromFields(fields map[string]any) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "DID",
	}

	// Copy all fields
	for k, v := range fields {
		jsonObj[k] = v
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

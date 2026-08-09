package state

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// DIDData represents a DID ledger entry.
// Reference: rippled ledger_entries.macro ltDID
type DIDData struct {
	Account     [20]byte
	OwnerNode   uint64
	URI         string // hex-encoded
	DIDDocument string // hex-encoded
	Data        string // hex-encoded
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// SerializeDID serializes a DID ledger entry using the binary codec.
func SerializeDID(did *DIDData, accountAddress string) ([]byte, error) {
	entry := &ledgerfields.DID{}
	entry.SetAccount(accountAddress)
	entry.SetOwnerNode(strconv.FormatUint(did.OwnerNode, 16))
	entry.SetFlags(0)

	if did.URI != "" {
		entry.SetURI(did.URI)
	}
	if did.DIDDocument != "" {
		entry.SetDIDDocument(did.DIDDocument)
	}
	if did.Data != "" {
		entry.SetData(did.Data)
	}

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if did.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(did.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(did.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

// ParseDID parses a DID ledger entry from binary data.
func ParseDID(data []byte) (*DIDData, error) {
	var decoded ledgerfields.DID
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode DID: %w", err)
	}
	fields := decoded.ToMap()
	did := &DIDData{
		URI:               strings.ToLower(decoded.URI),
		DIDDocument:       strings.ToLower(decoded.DIDDocument),
		Data:              strings.ToLower(decoded.Data),
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
	}

	var err error
	if _, ok := fields["Account"]; ok {
		did.Account, err = decodeLedgerAccount("DID.Account", decoded.Account)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["OwnerNode"]; ok {
		did.OwnerNode, err = parseLedgerUint64("DID.OwnerNode", decoded.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("DID.PreviousTxnID", decoded.PreviousTxnID, did.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}

	return did, nil
}

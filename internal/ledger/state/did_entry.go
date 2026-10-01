package state

import (
	"fmt"
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
	decoded           ledgerfields.DID
}

// SerializeDID serializes a DID ledger entry using the binary codec.
func SerializeDID(did *DIDData, accountAddress string) ([]byte, error) {
	entry := did.decoded
	entry.SetAccount(accountAddress)
	entry.SetOwnerNodeValue(did.OwnerNode)
	if !entry.HasFlags() {
		entry.SetFlagsValue(0)
	}

	if did.URI != "" || (entry.HasURI() && strings.EqualFold(did.URI, entry.URI)) {
		entry.SetURI(did.URI)
	} else {
		entry.ClearURI()
	}
	if did.DIDDocument != "" || (entry.HasDIDDocument() && strings.EqualFold(did.DIDDocument, entry.DIDDocument)) {
		entry.SetDIDDocument(did.DIDDocument)
	} else {
		entry.ClearDIDDocument()
	}
	if did.Data != "" || (entry.HasData() && strings.EqualFold(did.Data, entry.Data)) {
		entry.SetData(did.Data)
	} else {
		entry.ClearData()
	}

	entry.SetPreviousTxnIDValue(did.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(did.PreviousTxnLgrSeq)

	return entry.Encode()
}

// ParseDID parses a DID ledger entry from binary data.
func ParseDID(data []byte) (*DIDData, error) {
	var decoded ledgerfields.DID
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode DID: %w", err)
	}
	did := &DIDData{
		URI:         strings.ToLower(decoded.URI),
		DIDDocument: strings.ToLower(decoded.DIDDocument),
		Data:        strings.ToLower(decoded.Data),
		decoded:     decoded,
	}

	var err error
	if decoded.HasAccount() {
		did.Account, err = decoded.GetAccount()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasOwnerNode() {
		did.OwnerNode, err = decoded.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnID() {
		did.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		did.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, err
		}
	}

	return did, nil
}

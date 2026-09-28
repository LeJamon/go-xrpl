package applystate

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Threading types conditional on fixPreviousTxnID amendment
// These types only support threading if the amendment is enabled
var conditionalThreadingTypes = map[entry.Type]bool{
	entry.TypeDirectoryNode: true,
	entry.TypeAmendments:    true,
	entry.TypeFeeSettings:   true,
	entry.TypeNegativeUNL:   true,
	entry.TypeAMM:           true,
}

// Types that do NOT support threading (no PreviousTxnID field)
var nonThreadedTypes = map[entry.Type]bool{
	entry.TypeLedgerHashes: true,
}

// isThreadedType determines if an entry type supports transaction threading
// An entry is threaded if it has PreviousTxnID/PreviousTxnLgrSeq fields
// Some types are conditional on the fixPreviousTxnID amendment
func isThreadedType(entryType entry.Type, fixPreviousTxnIDEnabled bool) bool {
	// Non-threaded types never support threading
	if nonThreadedTypes[entryType] {
		return false
	}

	// Conditional types require amendment
	if conditionalThreadingTypes[entryType] {
		return fixPreviousTxnIDEnabled
	}

	// All other types with PreviousTxnID field are threaded
	return true
}

// threadItem updates PreviousTxnID and PreviousTxnLgrSeq on the entry
// Returns the previous values for metadata inclusion
// The entry data is modified in place
func threadItem(data []byte, txHash [32]byte, ledgerSeq uint32) (prevTxnID [32]byte, prevLgrSeq uint32, newData []byte, changed bool) {
	typ, err := state.DecodeType(data)
	if err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}
	decoded := entry.New(typ)
	setter, ok := decoded.(interface {
		entry.Entry
		HasPreviousTxnID() bool
		GetPreviousTxnID() ([32]byte, error)
		SetPreviousTxnIDValue([32]byte)
		SetPreviousTxnLgrSeq(uint32)
	})
	if !ok {
		return prevTxnID, prevLgrSeq, data, false
	}
	if err := decoded.Decode(data); err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}

	// Get current PreviousTxnID and PreviousTxnLgrSeq
	if setter.HasPreviousTxnID() {
		prevTxnID, err = setter.GetPreviousTxnID()
		if err != nil {
			return [32]byte{}, 0, data, false
		}
		_, prevLgrSeq = decoded.PreviousTxn()
	}

	// Check if already threaded to this transaction
	if prevTxnID == txHash {
		return prevTxnID, prevLgrSeq, data, false
	}

	// Update with new transaction info and re-encode the entry.
	setter.SetPreviousTxnIDValue(txHash)
	setter.SetPreviousTxnLgrSeq(ledgerSeq)
	newData, err = decoded.Encode()
	if err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}

	return prevTxnID, prevLgrSeq, newData, true
}

// getOwnerAccounts returns the account IDs that own this ledger entry.
// These accounts should have their PreviousTxnID/PreviousTxnLgrSeq updated.
// Reference: rippled ApplyStateTable.cpp threadOwners() lines 659-695.
func getOwnerAccounts(data []byte, entryType entry.Type) [][20]byte {
	var owners [][20]byte

	decoded := entry.New(entryType)
	if decoded == nil {
		return owners
	}
	if err := decoded.Decode(data); err != nil {
		return owners
	}
	if entryType == entry.TypeAccountRoot {
		return owners
	}
	addAccount := func(id [20]byte, err error) {
		if err == nil {
			owners = append(owners, id)
		}
	}
	accountField, hasAccount := decoded.(interface {
		HasAccount() bool
		GetAccount() ([20]byte, error)
	})
	destinationField, hasDestination := decoded.(interface {
		HasDestination() bool
		GetDestination() ([20]byte, error)
	})
	if entryType == entry.TypeRippleState {
		line, ok := decoded.(*entry.RippleState)
		if !ok {
			return owners
		}
		if line.HasLowLimit() {
			limit, err := line.GetLowLimit()
			if err == nil && limit.Issuer != "" {
				if id := decodeAccountAddress(limit.Issuer); id != nil {
					owners = append(owners, *id)
				}
			}
		}
		if line.HasHighLimit() {
			limit, err := line.GetHighLimit()
			if err == nil && limit.Issuer != "" {
				if id := decodeAccountAddress(limit.Issuer); id != nil {
					owners = append(owners, *id)
				}
			}
		}
		return owners
	}
	if hasAccount && accountField.HasAccount() {
		addAccount(accountField.GetAccount())
	}
	if hasDestination && destinationField.HasDestination() {
		addAccount(destinationField.GetDestination())
	}
	return owners
}

// decodeAccountAddress decodes an XRPL classic address to a 20-byte account ID,
// returning nil when the address is malformed (the callers treat a nil result
// as "no owner to thread").
func decodeAccountAddress(address string) *[20]byte {
	id, err := state.DecodeAccountID(address)
	if err != nil {
		return nil
	}
	return &id
}

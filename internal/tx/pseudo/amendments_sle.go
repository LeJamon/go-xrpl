package pseudo

import (
	"errors"
	"fmt"
	"slices"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// AmendmentsSLE represents the parsed Amendments ledger entry.
// Reference: rippled SLE with type ltAMENDMENTS (0x0066)
// Fields: sfAmendments (Vector256), sfMajorities (STArray)
type AmendmentsSLE struct {
	// Amendments is the list of fully enabled amendment hashes.
	Amendments [][32]byte

	// Majorities tracks amendments that have reached majority with their close times.
	// Each entry has an amendment hash and the close time when majority was achieved.
	Majorities []MajorityEntry

	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// MajorityEntry represents a single entry in the sfMajorities array.
// Reference: rippled STObject with sfAmendment (Hash256) + sfCloseTime (UInt32)
type MajorityEntry struct {
	Amendment [32]byte
	CloseTime uint32
}

// ParseAmendmentsSLE parses an Amendments SLE from binary data.
// Returns nil (no entry) if data is nil or empty.
func ParseAmendmentsSLE(data []byte) (*AmendmentsSLE, error) {
	if len(data) == 0 {
		return &AmendmentsSLE{}, nil
	}

	var decoded ledgerfields.Amendments
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode Amendments SLE: %w", err)
	}

	amendments, err := decoded.GetAmendments()
	if err != nil {
		return nil, fmt.Errorf("failed to decode Amendments SLE Amendments: %w", err)
	}
	majorities, err := decoded.GetMajorities()
	if err != nil {
		return nil, fmt.Errorf("failed to decode Amendments SLE Majorities: %w", err)
	}
	sle := &AmendmentsSLE{
		Amendments: amendments,
		Majorities: make([]MajorityEntry, 0, len(majorities)),
	}
	for i, majority := range majorities {
		amendment, err := majority.GetAmendment()
		if err != nil {
			return nil, fmt.Errorf("failed to decode Amendments SLE Majorities[%d] Amendment: %w", i, err)
		}
		closeTime, err := majority.GetCloseTime()
		if err != nil {
			return nil, fmt.Errorf("failed to decode Amendments SLE Majorities[%d] CloseTime: %w", i, err)
		}
		sle.Majorities = append(sle.Majorities, MajorityEntry{Amendment: amendment, CloseTime: closeTime})
	}

	if decoded.HasPreviousTxnID() {
		previousTxnID, err := decoded.GetPreviousTxnID()
		if err != nil {
			return nil, fmt.Errorf("failed to decode Amendments SLE PreviousTxnID: %w", err)
		}
		previousTxnLgrSeq, err := decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, fmt.Errorf("failed to decode Amendments SLE PreviousTxnLgrSeq: %w", err)
		}
		sle.PreviousTxnID = previousTxnID
		sle.PreviousTxnLgrSeq = previousTxnLgrSeq
	}

	return sle, nil
}

// SerializeAmendmentsSLE serializes an AmendmentsSLE to binary data.
func SerializeAmendmentsSLE(sle *AmendmentsSLE) ([]byte, error) {
	if sle == nil {
		return nil, errors.New("failed to encode Amendments SLE: nil entry")
	}

	var entry ledgerfields.Amendments
	entry.SetFlags(0)

	if len(sle.Amendments) > 0 {
		entry.SetAmendmentsValue(sle.Amendments)
	}

	if len(sle.Majorities) > 0 {
		majorities := make([]ledgerfields.MajorityValue, len(sle.Majorities))
		for i, majority := range sle.Majorities {
			majorities[i].SetAmendmentValue(majority.Amendment)
			majorities[i].SetCloseTime(majority.CloseTime)
		}
		if err := entry.SetMajoritiesValue(majorities); err != nil {
			return nil, fmt.Errorf("failed to encode Amendments SLE majorities: %w", err)
		}
	}

	var emptyHash [32]byte
	if sle.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnIDValue(sle.PreviousTxnID)
		entry.SetPreviousTxnLgrSeq(sle.PreviousTxnLgrSeq)
	} else if sle.PreviousTxnLgrSeq != 0 {
		return nil, errors.New("failed to encode Amendments SLE: PreviousTxnLgrSeq set without PreviousTxnID")
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Amendments SLE: %w", err)
	}
	return data, nil
}

// ContainsAmendment checks if the given amendment hash is in the enabled amendments list.
func (sle *AmendmentsSLE) ContainsAmendment(hash [32]byte) bool {
	return slices.Contains(sle.Amendments, hash)
}

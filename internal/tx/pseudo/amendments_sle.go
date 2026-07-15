package pseudo

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
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

	sle := &AmendmentsSLE{
		Amendments: make([][32]byte, 0, len(decoded.Amendments)),
		Majorities: make([]MajorityEntry, 0, len(decoded.Majorities)),
	}

	for i, hashHex := range decoded.Amendments {
		hash, err := decodeAmendmentHash(fmt.Sprintf("Amendments[%d]", i), hashHex)
		if err != nil {
			return nil, err
		}
		sle.Amendments = append(sle.Amendments, hash)
	}

	for i, item := range decoded.Majorities {
		wrapper, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to decode Amendments SLE Majorities[%d]: unexpected entry type %T", i, item)
		}
		inner, ok := wrapper["Majority"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to decode Amendments SLE Majorities[%d]: missing Majority", i)
		}
		amendmentHex, ok := inner["Amendment"].(string)
		if !ok {
			return nil, fmt.Errorf("failed to decode Amendments SLE Majorities[%d]: unexpected Amendment type %T", i, inner["Amendment"])
		}
		amendment, err := decodeAmendmentHash(fmt.Sprintf("Majorities[%d].Amendment", i), amendmentHex)
		if err != nil {
			return nil, err
		}
		sle.Majorities = append(sle.Majorities, MajorityEntry{
			Amendment: amendment,
			CloseTime: toUint32(inner["CloseTime"]),
		})
	}

	if decoded.PreviousTxnID != "" {
		previousTxnID, err := decodeAmendmentHash("PreviousTxnID", decoded.PreviousTxnID)
		if err != nil {
			return nil, err
		}
		sle.PreviousTxnID = previousTxnID
		sle.PreviousTxnLgrSeq = decoded.PreviousTxnLgrSeq
	}

	return sle, nil
}

func decodeAmendmentHash(field, value string) ([32]byte, error) {
	var hash [32]byte
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return hash, fmt.Errorf("failed to decode Amendments SLE %s: %w", field, err)
	}
	if len(decoded) != len(hash) {
		return hash, fmt.Errorf("failed to decode Amendments SLE %s: decoded length %d, want %d", field, len(decoded), len(hash))
	}
	copy(hash[:], decoded)
	return hash, nil
}

// SerializeAmendmentsSLE serializes an AmendmentsSLE to binary data.
func SerializeAmendmentsSLE(sle *AmendmentsSLE) ([]byte, error) {
	if sle == nil {
		return nil, errors.New("failed to encode Amendments SLE: nil entry")
	}

	var entry ledgerfields.Amendments
	entry.SetFlags(0)

	if len(sle.Amendments) > 0 {
		hashes := make([]string, len(sle.Amendments))
		for i, hash := range sle.Amendments {
			hashes[i] = strings.ToUpper(hex.EncodeToString(hash[:]))
		}
		entry.SetAmendments(hashes)
	}

	if len(sle.Majorities) > 0 {
		arr := make([]any, len(sle.Majorities))
		for i, entry := range sle.Majorities {
			arr[i] = map[string]any{
				"Majority": map[string]any{
					"Amendment": strings.ToUpper(hex.EncodeToString(entry.Amendment[:])),
					"CloseTime": entry.CloseTime,
				},
			}
		}
		entry.SetMajorities(arr)
	}

	var emptyHash [32]byte
	if sle.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(sle.PreviousTxnID[:])))
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

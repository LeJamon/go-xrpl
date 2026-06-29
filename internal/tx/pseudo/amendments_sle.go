package pseudo

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
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

	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Amendments SLE: %w", err)
	}

	sle := &AmendmentsSLE{}

	// Parse sfAmendments (Vector256 → []string of uppercase hex hashes)
	if amendments, ok := jsonObj["Amendments"]; ok {
		switch v := amendments.(type) {
		case []string:
			for _, hashHex := range v {
				var hash [32]byte
				b, err := hex.DecodeString(hashHex)
				if err != nil {
					return nil, fmt.Errorf("failed to decode amendment hash: %w", err)
				}
				copy(hash[:], b)
				sle.Amendments = append(sle.Amendments, hash)
			}
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					continue
				}
				var hash [32]byte
				b, err := hex.DecodeString(s)
				if err != nil {
					return nil, fmt.Errorf("failed to decode amendment hash: %w", err)
				}
				copy(hash[:], b)
				sle.Amendments = append(sle.Amendments, hash)
			}
		}
	}

	// Parse sfMajorities (STArray → []any of wrapper objects)
	if majorities, ok := jsonObj["Majorities"]; ok {
		arr, ok := majorities.([]any)
		if !ok {
			return nil, fmt.Errorf("unexpected Majorities type: %T", majorities)
		}
		for _, item := range arr {
			wrapper, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// Each element is wrapped: {"Majority": {"Amendment": "...", "CloseTime": ...}}
			inner, ok := wrapper["Majority"]
			if !ok {
				continue
			}
			innerMap, ok := inner.(map[string]any)
			if !ok {
				continue
			}

			entry := MajorityEntry{}

			if amendHash, ok := innerMap["Amendment"].(string); ok {
				b, err := hex.DecodeString(amendHash)
				if err == nil {
					copy(entry.Amendment[:], b)
				}
			}

			if closeTime, ok := innerMap["CloseTime"]; ok {
				switch v := closeTime.(type) {
				case float64:
					entry.CloseTime = uint32(v)
				case uint32:
					entry.CloseTime = v
				case int:
					entry.CloseTime = uint32(v)
				case int64:
					entry.CloseTime = uint32(v)
				}
			}

			sle.Majorities = append(sle.Majorities, entry)
		}
	}

	// PreviousTxnID/PreviousTxnLgrSeq are threaded as a pair.
	if ptid, ok := jsonObj["PreviousTxnID"].(string); ok {
		if b, err := hex.DecodeString(ptid); err == nil && len(b) == 32 {
			copy(sle.PreviousTxnID[:], b)
			sle.PreviousTxnLgrSeq = toUint32(jsonObj["PreviousTxnLgrSeq"])
		}
	}

	return sle, nil
}

// SerializeAmendmentsSLE serializes an AmendmentsSLE to binary data.
func SerializeAmendmentsSLE(sle *AmendmentsSLE) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "Amendments",
		"Flags":           0,
	}

	// Add sfAmendments (Vector256)
	if len(sle.Amendments) > 0 {
		hashes := make([]string, len(sle.Amendments))
		for i, hash := range sle.Amendments {
			hashes[i] = strings.ToUpper(hex.EncodeToString(hash[:]))
		}
		jsonObj["Amendments"] = hashes
	}

	// Add sfMajorities (STArray)
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
		jsonObj["Majorities"] = arr
	}

	// Carry the pointers through a no-op modify; absent on a brand-new entry.
	var emptyHash [32]byte
	if sle.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(sle.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = sle.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Amendments SLE: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ContainsAmendment checks if the given amendment hash is in the enabled amendments list.
func (sle *AmendmentsSLE) ContainsAmendment(hash [32]byte) bool {
	return slices.Contains(sle.Amendments, hash)
}

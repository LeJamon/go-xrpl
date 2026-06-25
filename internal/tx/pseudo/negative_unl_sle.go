package pseudo

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// DisabledValidator is one entry of the NegativeUNL's sfDisabledValidators
// array. Both PublicKey and FirstLedgerSequence are soeREQUIRED in the
// sfDisabledValidator inner-object template, so both are always serialized.
type DisabledValidator struct {
	// PublicKey is the master public key of the disabled validator.
	PublicKey []byte

	// FirstLedgerSequence is the flag-ledger sequence at which the validator
	// was added to the negative UNL.
	FirstLedgerSequence uint32
}

// NegativeUNLSLE represents the parsed NegativeUNL ledger entry.
// Reference: rippled SLE with type ltNEGATIVE_UNL (0x004e)
// Fields: sfDisabledValidators (STArray), sfValidatorToDisable (Blob),
//
//	sfValidatorToReEnable (Blob)
type NegativeUNLSLE struct {
	// DisabledValidators is the list of currently disabled validators.
	DisabledValidators []DisabledValidator

	// ValidatorToDisable is the validator scheduled for disabling (if any).
	ValidatorToDisable []byte

	// ValidatorToReEnable is the validator scheduled for re-enabling (if any).
	ValidatorToReEnable []byte

	// PreviousTxnID / PreviousTxnLgrSeq are the threading pointers stamped by the
	// last transaction that modified this entry (the UNL_MODIFY pseudo-tx, at
	// creation). The flag-ledger transition (Ledger.UpdateNegativeUNL) is NOT a
	// transaction — rippled rawReplaces the SLE in place and never re-threads it
	// — so these must survive the parse → modify → serialize round-trip. Dropping
	// them re-serialized the entry without its threading pointers and forked
	// account_hash at the flag ledger after a UNLModify (e.g. 99240960).
	PreviousTxnID     []byte
	PreviousTxnLgrSeq uint32
}

// ParseNegativeUNLSLE parses a NegativeUNL SLE from binary data.
func ParseNegativeUNLSLE(data []byte) (*NegativeUNLSLE, error) {
	if len(data) == 0 {
		return &NegativeUNLSLE{}, nil
	}

	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode NegativeUNL SLE: %w", err)
	}

	sle := &NegativeUNLSLE{}

	// Parse sfDisabledValidators (STArray of objects with sfPublicKey +
	// sfFirstLedgerSequence).
	if validators, ok := jsonObj["DisabledValidators"]; ok {
		arr, ok := validators.([]any)
		if !ok {
			return nil, fmt.Errorf("unexpected DisabledValidators type: %T", validators)
		}
		for _, item := range arr {
			wrapper, ok := item.(map[string]any)
			if !ok {
				continue
			}
			inner, ok := wrapper["DisabledValidator"]
			if !ok {
				continue
			}
			innerMap, ok := inner.(map[string]any)
			if !ok {
				continue
			}
			pubKey, ok := innerMap["PublicKey"].(string)
			if !ok {
				continue
			}
			b, err := hex.DecodeString(pubKey)
			if err != nil {
				continue
			}
			sle.DisabledValidators = append(sle.DisabledValidators, DisabledValidator{
				PublicKey:           b,
				FirstLedgerSequence: toUint32(innerMap["FirstLedgerSequence"]),
			})
		}
	}

	// Parse sfValidatorToDisable (Blob)
	if vtd, ok := jsonObj["ValidatorToDisable"].(string); ok {
		b, err := hex.DecodeString(vtd)
		if err == nil {
			sle.ValidatorToDisable = b
		}
	}

	// Parse sfValidatorToReEnable (Blob)
	if vtr, ok := jsonObj["ValidatorToReEnable"].(string); ok {
		b, err := hex.DecodeString(vtr)
		if err == nil {
			sle.ValidatorToReEnable = b
		}
	}

	// Parse the threading pointers (present together once a tx has touched the
	// entry; rippled always threads the pair, so read them as a pair).
	if ptid, ok := jsonObj["PreviousTxnID"].(string); ok {
		if b, err := hex.DecodeString(ptid); err == nil {
			sle.PreviousTxnID = b
			sle.PreviousTxnLgrSeq = toUint32(jsonObj["PreviousTxnLgrSeq"])
		}
	}

	return sle, nil
}

// SerializeNegativeUNLSLE serializes a NegativeUNLSLE to binary data.
func SerializeNegativeUNLSLE(sle *NegativeUNLSLE) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "NegativeUNL",
		"Flags":           0,
	}

	// Add sfDisabledValidators (STArray). Both inner fields are soeREQUIRED,
	// so FirstLedgerSequence is emitted unconditionally (even at 0).
	if len(sle.DisabledValidators) > 0 {
		arr := make([]any, len(sle.DisabledValidators))
		for i, dv := range sle.DisabledValidators {
			arr[i] = map[string]any{
				"DisabledValidator": map[string]any{
					"PublicKey":           strings.ToUpper(hex.EncodeToString(dv.PublicKey)),
					"FirstLedgerSequence": dv.FirstLedgerSequence,
				},
			}
		}
		jsonObj["DisabledValidators"] = arr
	}

	// Add sfValidatorToDisable (Blob)
	if len(sle.ValidatorToDisable) > 0 {
		jsonObj["ValidatorToDisable"] = strings.ToUpper(hex.EncodeToString(sle.ValidatorToDisable))
	}

	// Add sfValidatorToReEnable (Blob)
	if len(sle.ValidatorToReEnable) > 0 {
		jsonObj["ValidatorToReEnable"] = strings.ToUpper(hex.EncodeToString(sle.ValidatorToReEnable))
	}

	// Preserve the threading pointers if present. They are absent on a brand-new
	// entry (the ApplyStateTable threads the creating tx in afterwards) but must
	// be carried through a flag-ledger transition, which re-serializes the entry
	// outside any transaction and so does not re-thread it.
	if len(sle.PreviousTxnID) > 0 {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(sle.PreviousTxnID))
		jsonObj["PreviousTxnLgrSeq"] = sle.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NegativeUNL SLE: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ContainsValidator checks if a validator key is in the disabled validators list.
func (sle *NegativeUNLSLE) ContainsValidator(key []byte) bool {
	for _, dv := range sle.DisabledValidators {
		if bytes.Equal(dv.PublicKey, key) {
			return true
		}
	}
	return false
}

// toUint32 returns v as a uint32. The binarycodec decodes UInt32 fields as
// uint32; an absent or wrongly-typed value yields 0.
func toUint32(v any) uint32 {
	n, _ := v.(uint32)
	return n
}

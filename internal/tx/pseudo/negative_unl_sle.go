package pseudo

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
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

	var decoded ledgerfields.NegativeUNL
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode NegativeUNL SLE: %w", err)
	}

	sle := &NegativeUNLSLE{
		DisabledValidators: make([]DisabledValidator, 0, len(decoded.DisabledValidators)),
	}

	for i, item := range decoded.DisabledValidators {
		wrapper, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE DisabledValidators[%d]: unexpected entry type %T", i, item)
		}
		inner, ok := wrapper["DisabledValidator"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE DisabledValidators[%d]: missing DisabledValidator", i)
		}
		publicKey, ok := inner["PublicKey"].(string)
		if !ok {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE DisabledValidators[%d]: unexpected PublicKey type %T", i, inner["PublicKey"])
		}
		publicKeyBytes, err := hex.DecodeString(publicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE DisabledValidators[%d] PublicKey: %w", i, err)
		}
		sle.DisabledValidators = append(sle.DisabledValidators, DisabledValidator{
			PublicKey:           publicKeyBytes,
			FirstLedgerSequence: toUint32(inner["FirstLedgerSequence"]),
		})
	}

	var err error
	if decoded.ValidatorToDisable != "" {
		sle.ValidatorToDisable, err = hex.DecodeString(decoded.ValidatorToDisable)
		if err != nil {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE ValidatorToDisable: %w", err)
		}
	}
	if decoded.ValidatorToReEnable != "" {
		sle.ValidatorToReEnable, err = hex.DecodeString(decoded.ValidatorToReEnable)
		if err != nil {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE ValidatorToReEnable: %w", err)
		}
	}
	if decoded.PreviousTxnID != "" {
		sle.PreviousTxnID, err = hex.DecodeString(decoded.PreviousTxnID)
		if err != nil {
			return nil, fmt.Errorf("failed to decode NegativeUNL SLE PreviousTxnID: %w", err)
		}
		sle.PreviousTxnLgrSeq = decoded.PreviousTxnLgrSeq
	}

	return sle, nil
}

// SerializeNegativeUNLSLE serializes a NegativeUNLSLE to binary data.
func SerializeNegativeUNLSLE(sle *NegativeUNLSLE) ([]byte, error) {
	if sle == nil {
		return nil, errors.New("failed to encode NegativeUNL SLE: nil entry")
	}

	var entry ledgerfields.NegativeUNL
	entry.SetFlags(0)

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
		entry.SetDisabledValidators(arr)
	}

	if len(sle.ValidatorToDisable) > 0 {
		entry.SetValidatorToDisable(strings.ToUpper(hex.EncodeToString(sle.ValidatorToDisable)))
	}

	if len(sle.ValidatorToReEnable) > 0 {
		entry.SetValidatorToReEnable(strings.ToUpper(hex.EncodeToString(sle.ValidatorToReEnable)))
	}

	if len(sle.PreviousTxnID) > 0 {
		if len(sle.PreviousTxnID) != 32 {
			return nil, fmt.Errorf("failed to encode NegativeUNL SLE: PreviousTxnID is %d bytes, want 32", len(sle.PreviousTxnID))
		}
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(sle.PreviousTxnID)))
		entry.SetPreviousTxnLgrSeq(sle.PreviousTxnLgrSeq)
	} else if sle.PreviousTxnLgrSeq != 0 {
		return nil, errors.New("failed to encode NegativeUNL SLE: PreviousTxnLgrSeq set without PreviousTxnID")
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode NegativeUNL SLE: %w", err)
	}
	return data, nil
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

package pseudo

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// NegativeUNLSLE represents the parsed NegativeUNL ledger entry.
// Reference: rippled SLE with type ltNEGATIVE_UNL (0x004e)
// Fields: sfDisabledValidators (STArray), sfValidatorToDisable (Blob),
//
//	sfValidatorToReEnable (Blob)
type NegativeUNLSLE struct {
	// DisabledValidators is the list of currently disabled validator public keys.
	DisabledValidators [][]byte

	// ValidatorToDisable is the validator scheduled for disabling (if any).
	ValidatorToDisable []byte

	// ValidatorToReEnable is the validator scheduled for re-enabling (if any).
	ValidatorToReEnable []byte
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

	// Parse sfDisabledValidators (STArray of objects with sfPublicKey)
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
			if pubKey, ok := innerMap["PublicKey"].(string); ok {
				b, err := hex.DecodeString(pubKey)
				if err == nil {
					sle.DisabledValidators = append(sle.DisabledValidators, b)
				}
			}
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

	return sle, nil
}

// SerializeNegativeUNLSLE serializes a NegativeUNLSLE to binary data.
func SerializeNegativeUNLSLE(sle *NegativeUNLSLE) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "NegativeUNL",
		"Flags":           0,
	}

	// Add sfDisabledValidators (STArray)
	if len(sle.DisabledValidators) > 0 {
		arr := make([]any, len(sle.DisabledValidators))
		for i, key := range sle.DisabledValidators {
			arr[i] = map[string]any{
				"DisabledValidator": map[string]any{
					"PublicKey": strings.ToUpper(hex.EncodeToString(key)),
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

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NegativeUNL SLE: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ContainsValidator checks if a validator key is in the disabled validators list.
func (sle *NegativeUNLSLE) ContainsValidator(key []byte) bool {
	for _, k := range sle.DisabledValidators {
		if bytesEqual(k, key) {
			return true
		}
	}
	return false
}

// bytesEqual compares two byte slices for equality.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

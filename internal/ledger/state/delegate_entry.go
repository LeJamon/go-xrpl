package state

import (
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
)

// DelegateData holds parsed fields from a Delegate ledger entry.
// Reference: rippled ledger_entries.macro ltDELEGATE
type DelegateData struct {
	Account     [20]byte // Account that granted the delegation
	Authorize   [20]byte // Account that received the delegation
	OwnerNode   uint64
	Permissions []uint32 // Permission values (txType+1 or granular permission)
}

// ParseDelegate parses a Delegate ledger entry from binary data.
// Extracts Account, Authorize, OwnerNode, and the Permissions array.
// Reference: rippled DelegateUtils.cpp — sfPermissions array with sfPermissionValue fields
func ParseDelegate(data []byte) (*DelegateData, error) {
	hexStr := hex.EncodeToString(data)
	decoded, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Delegate: %w", err)
	}

	entry := &DelegateData{}

	// Parse Account
	if account, ok := decoded["Account"].(string); ok {
		accountID, err := DecodeAccountID(account)
		if err != nil {
			return nil, fmt.Errorf("failed to decode Account: %w", err)
		}
		entry.Account = accountID
	}

	// Parse Authorize
	if authorize, ok := decoded["Authorize"].(string); ok {
		authorizeID, err := DecodeAccountID(authorize)
		if err != nil {
			return nil, fmt.Errorf("failed to decode Authorize: %w", err)
		}
		entry.Authorize = authorizeID
	}

	// Parse OwnerNode
	if ownerNode, ok := decoded["OwnerNode"].(string); ok {
		entry.OwnerNode = parseUint64Hex(ownerNode)
	}

	// Parse Permissions array
	// The binary codec decodes this as:
	//   [{"Permission": {"PermissionValue": <string_or_uint32>}}, ...]
	// PermissionValue.ToJSON() returns a string name if known, or a uint32.
	if perms, ok := decoded["Permissions"]; ok {
		if permsArray, ok := perms.([]interface{}); ok {
			for _, permWrapper := range permsArray {
				permMap, ok := permWrapper.(map[string]interface{})
				if !ok {
					continue
				}
				// Unwrap the "Permission" wrapper
				var innerMap map[string]interface{}
				if inner, ok := permMap["Permission"]; ok {
					innerMap, _ = inner.(map[string]interface{})
				} else {
					innerMap = permMap
				}
				if innerMap == nil {
					continue
				}
				// Extract PermissionValue
				if pv, ok := innerMap["PermissionValue"]; ok {
					permValue := parsePermissionValue(pv)
					if permValue > 0 {
						entry.Permissions = append(entry.Permissions, permValue)
					}
				}
			}
		}
	}

	return entry, nil
}

// parsePermissionValue converts a decoded PermissionValue to uint32.
// The binary codec may return:
// - A string name (e.g., "Payment") which needs to be looked up
// - A uint32/float64/int numeric value
func parsePermissionValue(v interface{}) uint32 {
	switch val := v.(type) {
	case string:
		// Look up the string name in the delegatable permissions map.
		// The definitions package maps "Payment" -> 1, etc.
		pv, err := definitions.Get().GetDelegatablePermissionValueByName(val)
		if err == nil {
			return uint32(pv)
		}
		return 0
	case float64:
		return uint32(val)
	case int:
		return uint32(val)
	case uint32:
		return val
	case int32:
		return uint32(val)
	default:
		return 0
	}
}

// SerializeDelegate serializes a Delegate ledger entry.
// Reference: rippled DelegateSet.cpp doApply()
func SerializeDelegate(account, authorize [20]byte, permissions []uint32, ownerNode uint64) ([]byte, error) {
	accountAddr, err := addresscodec.EncodeAccountIDToClassicAddress(account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}
	authorizeAddr, err := addresscodec.EncodeAccountIDToClassicAddress(authorize[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode authorize address: %w", err)
	}

	// Build Permissions array
	permsArray := make([]map[string]any, len(permissions))
	for i, pv := range permissions {
		permsArray[i] = map[string]any{
			"Permission": map[string]any{
				"PermissionValue": pv,
			},
		}
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Delegate",
		"Account":         accountAddr,
		"Authorize":       authorizeAddr,
		"Permissions":     permsArray,
		"OwnerNode":       fmt.Sprintf("%X", ownerNode),
		"Flags":           uint32(0),
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Delegate: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// HasTxPermission checks if the Delegate SLE grants permission for the given
// transaction type. The permission value for a tx type is txType + 1.
// Reference: rippled DelegateUtils.cpp checkTxPermission()
func (d *DelegateData) HasTxPermission(txType uint32) bool {
	txPermission := txType + 1
	for _, pv := range d.Permissions {
		if pv == txPermission {
			return true
		}
	}
	return false
}

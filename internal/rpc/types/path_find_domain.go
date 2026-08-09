package types

import (
	"encoding/hex"
	"encoding/json"
)

// ParsePathFindDomain parses the optional path-finding domain value. A missing
// field is represented by the caller as a nil raw message and is unrestricted;
// a present "0" uses rippled's zero-value uint256 exception, while other
// values must be a full 256-bit hexadecimal ID.
func ParsePathFindDomain(raw json.RawMessage) (*[32]byte, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}

	var domainID [32]byte
	if value == "0" {
		return &domainID, true
	}

	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(domainID) {
		return nil, false
	}
	copy(domainID[:], decoded)
	return &domainID, true
}

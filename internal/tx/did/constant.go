package did

import "errors"

// DID field length constants
// Reference: rippled Protocol.h
const (
	// MaxDIDURILength is the maximum length of the URI field (in bytes after hex decode)
	MaxDIDURILength = 256

	// MaxDIDDocumentLength is the maximum length of the DIDDocument field (in bytes after hex decode)
	MaxDIDDocumentLength = 256

	// MaxDIDAttestationLength is the maximum length of the Data field (in bytes after hex decode)
	MaxDIDAttestationLength = 256
)

// DID validation errors
var (
	ErrDIDEmpty       = errors.New("temEMPTY_DID: DID transaction must have at least one non-empty field")
	ErrDIDURITooLong  = errors.New("temMALFORMED: URI exceeds maximum length of 256 bytes")
	ErrDIDDocTooLong  = errors.New("temMALFORMED: DIDDocument exceeds maximum length of 256 bytes")
	ErrDIDDataTooLong = errors.New("temMALFORMED: Data exceeds maximum length of 256 bytes")
	ErrDIDInvalidHex  = errors.New("temMALFORMED: field must be valid hex string")
)

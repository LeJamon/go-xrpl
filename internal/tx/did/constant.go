package did

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

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
	ErrDIDEmpty       = ter.Errorf(ter.TemEMPTY_DID, "DID transaction must have at least one non-empty field")
	ErrDIDURITooLong  = ter.Errorf(ter.TemMALFORMED, "URI exceeds maximum length of 256 bytes")
	ErrDIDDocTooLong  = ter.Errorf(ter.TemMALFORMED, "DIDDocument exceeds maximum length of 256 bytes")
	ErrDIDDataTooLong = ter.Errorf(ter.TemMALFORMED, "Data exceeds maximum length of 256 bytes")
	ErrDIDInvalidHex  = ter.Errorf(ter.TemMALFORMED, "field must be valid hex string")
)

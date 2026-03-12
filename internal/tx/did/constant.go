package did

import "github.com/LeJamon/goXRPLd/internal/tx"

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
	ErrDIDEmpty       = tx.Errorf(tx.TemEMPTY_DID, "DID transaction must have at least one non-empty field")
	ErrDIDURITooLong  = tx.Errorf(tx.TemMALFORMED, "URI exceeds maximum length of 256 bytes")
	ErrDIDDocTooLong  = tx.Errorf(tx.TemMALFORMED, "DIDDocument exceeds maximum length of 256 bytes")
	ErrDIDDataTooLong = tx.Errorf(tx.TemMALFORMED, "Data exceeds maximum length of 256 bytes")
	ErrDIDInvalidHex  = tx.Errorf(tx.TemMALFORMED, "field must be valid hex string")
)

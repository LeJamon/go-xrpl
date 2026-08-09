package did

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

const maxDIDFieldLength = 256

var (
	errDIDEmpty       = ter.Errorf(ter.TemEMPTY_DID, "DID transaction must have at least one non-empty field")
	errDIDURITooLong  = ter.Errorf(ter.TemMALFORMED, "URI exceeds maximum length of 256 bytes")
	errDIDDocTooLong  = ter.Errorf(ter.TemMALFORMED, "DIDDocument exceeds maximum length of 256 bytes")
	errDIDDataTooLong = ter.Errorf(ter.TemMALFORMED, "Data exceeds maximum length of 256 bytes")
	errDIDInvalidHex  = ter.Errorf(ter.TemMALFORMED, "field must be valid hex string")
)

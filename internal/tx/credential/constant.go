package credential

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// Credential constants matching rippled Protocol.h
const (
	// MaxCredentialURILength is the maximum length of a URI inside a Credential (256 bytes)
	MaxCredentialURILength = 256

	// MaxCredentialTypeLength is the maximum length of CredentialType (64 bytes)
	MaxCredentialTypeLength = 64
)

// Credential validation errors
var (
	ErrCredentialTypeTooLong = ter.Errorf(ter.TemMALFORMED, "CredentialType exceeds maximum length")
	ErrCredentialTypeEmpty   = ter.Errorf(ter.TemMALFORMED, "CredentialType is empty")
	ErrCredentialURITooLong  = ter.Errorf(ter.TemMALFORMED, "URI exceeds maximum length")
	ErrCredentialURIEmpty    = ter.Errorf(ter.TemMALFORMED, "URI is empty")
	ErrCredentialNoSubject   = ter.Errorf(ter.TemMALFORMED, "Subject is required")
	ErrCredentialNoIssuer    = ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "Issuer field zeroed")
	ErrCredentialNoFields    = ter.Errorf(ter.TemMALFORMED, "No Subject or Issuer fields")
	ErrCredentialZeroAccount = ter.Errorf(ter.TemINVALID_ACCOUNT_ID, "Subject or Issuer field zeroed")
)

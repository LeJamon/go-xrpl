package credential

import "errors"

// Credential constants matching rippled Protocol.h
const (
	// MaxCredentialURILength is the maximum length of a URI inside a Credential (256 bytes)
	MaxCredentialURILength = 256

	// MaxCredentialTypeLength is the maximum length of CredentialType (64 bytes)
	MaxCredentialTypeLength = 64
)

// Credential validation errors
var (
	ErrCredentialTypeTooLong = errors.New("temMALFORMED: CredentialType exceeds maximum length")
	ErrCredentialTypeEmpty   = errors.New("temMALFORMED: CredentialType is empty")
	ErrCredentialURITooLong  = errors.New("temMALFORMED: URI exceeds maximum length")
	ErrCredentialURIEmpty    = errors.New("temMALFORMED: URI is empty")
	ErrCredentialNoSubject   = errors.New("temMALFORMED: Subject is required")
	ErrCredentialNoIssuer    = errors.New("temINVALID_ACCOUNT_ID: Issuer field zeroed")
	ErrCredentialNoFields    = errors.New("temMALFORMED: No Subject or Issuer fields")
	ErrCredentialZeroAccount = errors.New("temINVALID_ACCOUNT_ID: Subject or Issuer field zeroed")
)

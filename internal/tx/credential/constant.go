package credential

import "github.com/LeJamon/goXRPLd/internal/tx"

// Credential constants matching rippled Protocol.h
const (
	// MaxCredentialURILength is the maximum length of a URI inside a Credential (256 bytes)
	MaxCredentialURILength = 256

	// MaxCredentialTypeLength is the maximum length of CredentialType (64 bytes)
	MaxCredentialTypeLength = 64
)

// Credential validation errors
var (
	ErrCredentialTypeTooLong = tx.Errorf(tx.TemMALFORMED, "CredentialType exceeds maximum length")
	ErrCredentialTypeEmpty   = tx.Errorf(tx.TemMALFORMED, "CredentialType is empty")
	ErrCredentialURITooLong  = tx.Errorf(tx.TemMALFORMED, "URI exceeds maximum length")
	ErrCredentialURIEmpty    = tx.Errorf(tx.TemMALFORMED, "URI is empty")
	ErrCredentialNoSubject   = tx.Errorf(tx.TemMALFORMED, "Subject is required")
	ErrCredentialNoIssuer    = tx.Errorf(tx.TemINVALID_ACCOUNT_ID, "Issuer field zeroed")
	ErrCredentialNoFields    = tx.Errorf(tx.TemMALFORMED, "No Subject or Issuer fields")
	ErrCredentialZeroAccount = tx.Errorf(tx.TemINVALID_ACCOUNT_ID, "Subject or Issuer field zeroed")
)

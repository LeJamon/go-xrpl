package permissioneddomain

import "github.com/LeJamon/goXRPLd/internal/tx"

// Permissioned domain constants
const (
	// MaxPermissionedDomainCredentials is the maximum number of credentials per domain
	MaxPermissionedDomainCredentials = 10
)

// Permissioned domain errors
var (
	ErrPermDomainDomainIDZero        = tx.Errorf(tx.TemMALFORMED, "DomainID cannot be zero")
	ErrPermDomainTooManyCredentials  = tx.Errorf(tx.TemARRAY_TOO_LARGE, "too many AcceptedCredentials")
	ErrPermDomainEmptyCredentials    = tx.Errorf(tx.TemARRAY_EMPTY, "AcceptedCredentials cannot be empty")
	ErrPermDomainDuplicateCredential = tx.Errorf(tx.TemMALFORMED, "duplicate credential in AcceptedCredentials")
	ErrPermDomainEmptyCredType       = tx.Errorf(tx.TemMALFORMED, "CredentialType cannot be empty")
	ErrPermDomainCredTypeTooLong     = tx.Errorf(tx.TemMALFORMED, "CredentialType exceeds maximum length")
	ErrPermDomainNoIssuer            = tx.Errorf(tx.TemMALFORMED, "Issuer is required for each credential")
	ErrPermDomainIDRequired          = tx.Errorf(tx.TemMALFORMED, "DomainID is required for delete")
)

// AcceptedCredential defines an accepted credential type (wrapper for XRPL STArray format)
// The inner field uses "Credential" to match the binary codec STObject field (nth=33).
type AcceptedCredential struct {
	Credential AcceptedCredentialData `json:"Credential"`
}

// AcceptedCredentialData contains the credential data
type AcceptedCredentialData struct {
	Issuer         string `json:"Issuer"`
	CredentialType string `json:"CredentialType"`
}

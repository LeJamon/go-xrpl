package permissioneddomain

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// Permissioned domain constants
const (
	// MaxPermissionedDomainCredentials is the maximum number of credentials per domain
	MaxPermissionedDomainCredentials = 10
)

// Permissioned domain errors
var (
	ErrPermDomainTooManyCredentials  = ter.Errorf(ter.TemARRAY_TOO_LARGE, "too many AcceptedCredentials")
	ErrPermDomainEmptyCredentials    = ter.Errorf(ter.TemARRAY_EMPTY, "AcceptedCredentials cannot be empty")
	ErrPermDomainDuplicateCredential = ter.Errorf(ter.TemMALFORMED, "duplicate credential in AcceptedCredentials")
	ErrPermDomainEmptyCredType       = ter.Errorf(ter.TemMALFORMED, "CredentialType cannot be empty")
	ErrPermDomainCredTypeTooLong     = ter.Errorf(ter.TemMALFORMED, "CredentialType exceeds maximum length")
	ErrPermDomainNoIssuer            = ter.Errorf(ter.TemMALFORMED, "Issuer is required for each credential")
	ErrPermDomainIDRequired          = ter.Errorf(ter.TemMALFORMED, "DomainID is required for delete")
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

package permissioneddomain

import "errors"

// Permissioned domain constants
const (
	// MaxPermissionedDomainCredentials is the maximum number of credentials per domain
	MaxPermissionedDomainCredentials = 10
)

// Permissioned domain errors
var (
	ErrPermDomainDomainIDZero        = errors.New("temMALFORMED: DomainID cannot be zero")
	ErrPermDomainTooManyCredentials  = errors.New("temARRAY_TOO_LARGE: too many AcceptedCredentials")
	ErrPermDomainEmptyCredentials    = errors.New("temARRAY_EMPTY: AcceptedCredentials cannot be empty")
	ErrPermDomainDuplicateCredential = errors.New("temMALFORMED: duplicate credential in AcceptedCredentials")
	ErrPermDomainEmptyCredType       = errors.New("temMALFORMED: CredentialType cannot be empty")
	ErrPermDomainCredTypeTooLong     = errors.New("temMALFORMED: CredentialType exceeds maximum length")
	ErrPermDomainNoIssuer            = errors.New("temMALFORMED: Issuer is required for each credential")
	ErrPermDomainIDRequired          = errors.New("temMALFORMED: DomainID is required for delete")
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

package entry

import (
	"errors"
)

// Credential represents a verifiable credential ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltCREDENTIAL
type Credential struct {
	BaseEntry

	// Required fields
	Subject        [20]byte // Account that is the subject of the credential
	Issuer         [20]byte // Account that issued the credential
	CredentialType []byte   // Type of credential (up to 64 bytes)
	IssuerNode     uint64   // Issuer directory node hint
	SubjectNode    uint64   // Subject directory node hint

	// Optional fields
	Expiration *uint32 // Unix timestamp when credential expires
	URI        *string // URI for additional credential information
}

func (c *Credential) Type() Type {
	return TypeCredential
}

func (c *Credential) Validate() error {
	if c.Subject == [20]byte{} {
		return errors.New("subject is required")
	}
	if c.Issuer == [20]byte{} {
		return errors.New("issuer is required")
	}
	if len(c.CredentialType) == 0 {
		return errors.New("credential type is required")
	}
	if len(c.CredentialType) > 64 {
		return errors.New("credential type cannot exceed 64 bytes")
	}
	return nil
}

func (c *Credential) Hash() ([32]byte, error) {
	hash := c.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= c.Subject[i]
		hash[i] ^= c.Issuer[i]
	}
	return hash, nil
}

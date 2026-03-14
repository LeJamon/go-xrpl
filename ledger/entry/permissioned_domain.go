package entry

import (
	"errors"
)

// AcceptedCredential represents a credential type accepted by a permissioned domain
type AcceptedCredential struct {
	Issuer         [20]byte // Issuer of the credential
	CredentialType []byte   // Type of credential accepted
}

// PermissionedDomain represents a permissioned domain ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltPERMISSIONED_DOMAIN
type PermissionedDomain struct {
	BaseEntry

	// Required fields
	Owner               [20]byte             // Account that owns this domain
	Sequence            uint32               // Sequence number when created
	AcceptedCredentials []AcceptedCredential // List of accepted credential types
	OwnerNode           uint64               // Directory node hint
}

func (p *PermissionedDomain) Type() Type {
	return TypePermissionedDomain
}

func (p *PermissionedDomain) Validate() error {
	if p.Owner == [20]byte{} {
		return errors.New("owner is required")
	}
	if len(p.AcceptedCredentials) == 0 {
		return errors.New("at least one accepted credential is required")
	}
	if len(p.AcceptedCredentials) > 10 {
		return errors.New("accepted credentials cannot exceed 10 entries")
	}
	for _, cred := range p.AcceptedCredentials {
		if cred.Issuer == [20]byte{} {
			return errors.New("credential issuer is required")
		}
		if len(cred.CredentialType) == 0 {
			return errors.New("credential type is required")
		}
	}
	return nil
}

func (p *PermissionedDomain) Hash() ([32]byte, error) {
	hash := p.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= p.Owner[i]
	}
	return hash, nil
}

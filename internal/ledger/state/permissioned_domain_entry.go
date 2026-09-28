package state

import (
	"fmt"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// PermissionedDomainData holds the parsed fields of a PermissionedDomain ledger entry.
// Reference: rippled ledger_entries.macro ltPERMISSIONED_DOMAIN
type PermissionedDomainData struct {
	Owner               [20]byte
	Sequence            uint32
	OwnerNode           uint64
	AcceptedCredentials []PermissionedDomainCredential
	// Round-trips so a no-op modify re-serializes byte-identically and the apply
	// layer's unchanged-entry guard prunes it (ApplyStateTable.cpp:154-157).
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	decoded           ledgerfields.PermissionedDomain
}

// PermissionedDomainCredential is a single accepted credential entry within a PermissionedDomain.
type PermissionedDomainCredential struct {
	Issuer         [20]byte
	CredentialType []byte
}

// SerializePermissionedDomain serializes a PermissionedDomain ledger entry using the binary codec.
// Reference: rippled PermissionedDomainSet.cpp doApply()
func SerializePermissionedDomain(pd *PermissionedDomainData, ownerAddress string) ([]byte, error) {
	credentials := make([]ledgerfields.CredentialValue, len(pd.AcceptedCredentials))
	for i, value := range pd.AcceptedCredentials {
		if err := credentials[i].SetIssuerValue(value.Issuer); err != nil {
			return nil, fmt.Errorf("failed to encode PermissionedDomain.AcceptedCredentials[%d].Issuer: %w", i, err)
		}
		credentials[i].SetCredentialTypeValue(value.CredentialType)
	}

	entry := pd.decoded
	entry.SetOwner(ownerAddress)
	entry.SetSequenceValue(pd.Sequence)
	entry.SetOwnerNodeValue(pd.OwnerNode)
	if !entry.HasFlags() {
		entry.SetFlagsValue(0)
	}
	if err := entry.SetAcceptedCredentialsValue(credentials); err != nil {
		return nil, fmt.Errorf("failed to encode PermissionedDomain.AcceptedCredentials: %w", err)
	}
	entry.SetPreviousTxnIDValue(pd.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(pd.PreviousTxnLgrSeq)

	return entry.Encode()
}

// ParsePermissionedDomain parses a PermissionedDomain ledger entry from binary data.
func ParsePermissionedDomain(data []byte) (*PermissionedDomainData, error) {
	var decoded ledgerfields.PermissionedDomain
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode PermissionedDomain: %w", err)
	}
	pd := &PermissionedDomainData{decoded: decoded}

	var err error
	if decoded.HasOwner() {
		pd.Owner, err = decoded.GetOwner()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasSequence() {
		pd.Sequence, err = decoded.GetSequence()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasOwnerNode() {
		pd.OwnerNode, err = decoded.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnID() {
		pd.PreviousTxnID, err = decoded.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if decoded.HasPreviousTxnLgrSeq() {
		pd.PreviousTxnLgrSeq, err = decoded.GetPreviousTxnLgrSeq()
		if err != nil {
			return nil, err
		}
	}
	values, err := decoded.GetAcceptedCredentials()
	if err != nil {
		return nil, err
	}
	pd.AcceptedCredentials = make([]PermissionedDomainCredential, len(values))
	for i, value := range values {
		pd.AcceptedCredentials[i].Issuer, err = value.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("PermissionedDomain.AcceptedCredentials[%d].Issuer: %w", i, err)
		}
		credentialType, err := value.GetCredentialType()
		if err != nil {
			return nil, fmt.Errorf("PermissionedDomain.AcceptedCredentials[%d].CredentialType: %w", i, err)
		}
		pd.AcceptedCredentials[i].CredentialType = append([]byte(nil), credentialType...)
	}

	return pd, nil
}

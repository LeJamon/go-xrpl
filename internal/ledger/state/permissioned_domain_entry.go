package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
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
}

// PermissionedDomainCredential is a single accepted credential entry within a PermissionedDomain.
type PermissionedDomainCredential struct {
	Issuer         [20]byte
	CredentialType []byte
}

// SerializePermissionedDomain serializes a PermissionedDomain ledger entry using the binary codec.
// Reference: rippled PermissionedDomainSet.cpp doApply()
func SerializePermissionedDomain(pd *PermissionedDomainData, ownerAddress string) ([]byte, error) {
	creds := make([]any, 0, len(pd.AcceptedCredentials))
	for _, c := range pd.AcceptedCredentials {
		issuerStr, err := EncodeAccountID(c.Issuer)
		if err != nil {
			return nil, err
		}
		creds = append(creds, map[string]any{
			"Credential": map[string]any{
				"Issuer":         issuerStr,
				"CredentialType": hex.EncodeToString(c.CredentialType),
			},
		})
	}

	entry := &ledgerfields.PermissionedDomain{}
	entry.SetOwner(ownerAddress)
	entry.SetSequence(pd.Sequence)
	entry.SetOwnerNode(fmt.Sprintf("%X", pd.OwnerNode))
	entry.SetFlags(0)
	entry.SetAcceptedCredentials(creds)

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if pd.PreviousTxnID != emptyHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(pd.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(pd.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

// ParsePermissionedDomain parses a PermissionedDomain ledger entry from binary data.
func ParsePermissionedDomain(data []byte) (*PermissionedDomainData, error) {
	var decoded ledgerfields.PermissionedDomain
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("failed to decode PermissionedDomain: %w", err)
	}
	fields := decoded.ToMap()
	pd := &PermissionedDomainData{
		Sequence:          decoded.Sequence,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
	}

	var err error
	if _, ok := fields["Owner"]; ok {
		pd.Owner, err = decodeLedgerAccount("PermissionedDomain.Owner", decoded.Owner)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["OwnerNode"]; ok {
		pd.OwnerNode, err = parseLedgerUint64("PermissionedDomain.OwnerNode", decoded.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := fields["PreviousTxnID"]; ok {
		if err := decodeLedgerHex("PermissionedDomain.PreviousTxnID", decoded.PreviousTxnID, pd.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	pd.AcceptedCredentials, err = decodeAcceptedCredentials(decoded.AcceptedCredentials)
	if err != nil {
		return nil, err
	}

	return pd, nil
}

func decodeAcceptedCredentials(values []any) ([]PermissionedDomainCredential, error) {
	var creds []PermissionedDomainCredential
	for i, value := range values {
		wrapper, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("PermissionedDomain.AcceptedCredentials[%d]: expected object, got %T", i, value)
		}
		value, ok = wrapper["Credential"]
		if !ok {
			continue
		}
		fields, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("PermissionedDomain.AcceptedCredentials[%d].Credential: expected object, got %T", i, value)
		}

		var credential PermissionedDomainCredential
		if issuer, ok := fields["Issuer"].(string); ok {
			decodedIssuer, err := decodeLedgerAccount(fmt.Sprintf("PermissionedDomain.AcceptedCredentials[%d].Credential.Issuer", i), issuer)
			if err != nil {
				return nil, err
			}
			credential.Issuer = decodedIssuer
		}
		if credentialType, ok := fields["CredentialType"].(string); ok {
			decodedType, err := hex.DecodeString(credentialType)
			if err != nil {
				return nil, fmt.Errorf("PermissionedDomain.AcceptedCredentials[%d].Credential.CredentialType: invalid hex: %w", i, err)
			}
			credential.CredentialType = decodedType
		}
		creds = append(creds, credential)
	}
	return creds, nil
}

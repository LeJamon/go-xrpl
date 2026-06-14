package state

import (
	"encoding/hex"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// PermissionedDomainData holds the parsed fields of a PermissionedDomain ledger entry.
// Reference: rippled ledger_entries.macro ltPERMISSIONED_DOMAIN
type PermissionedDomainData struct {
	Owner               [20]byte
	Sequence            uint32
	OwnerNode           uint64
	AcceptedCredentials []PermissionedDomainCredential
}

// PermissionedDomainCredential is a single accepted credential entry within a PermissionedDomain.
type PermissionedDomainCredential struct {
	Issuer         [20]byte
	CredentialType []byte
}

// SerializePermissionedDomain serializes a PermissionedDomain ledger entry using the binary codec.
// Reference: rippled PermissionedDomainSet.cpp doApply()
func SerializePermissionedDomain(pd *PermissionedDomainData, ownerAddress string) ([]byte, error) {
	creds := make([]map[string]any, 0, len(pd.AcceptedCredentials))
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

	jsonObj := map[string]any{
		"LedgerEntryType":     "PermissionedDomain",
		"Owner":               ownerAddress,
		"Sequence":            pd.Sequence,
		"OwnerNode":           fmt.Sprintf("%X", pd.OwnerNode),
		"Flags":               uint32(0),
		"AcceptedCredentials": creds,
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// ParsePermissionedDomain parses a PermissionedDomain ledger entry from binary data.
func ParsePermissionedDomain(data []byte) (*PermissionedDomainData, error) {
	pd := &PermissionedDomainData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			if f.FieldCode == 4 { // Sequence
				pd.Sequence = f.UInt32()
			}
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				pd.OwnerNode = f.UInt64()
			}
		case stAccountID:
			if f.FieldCode == 2 { // Owner
				if id, ok := f.AccountID(); ok {
					pd.Owner = id
				}
			}
		case stArray:
			if f.FieldCode == 28 { // AcceptedCredentials
				pd.AcceptedCredentials = parseAcceptedCredentials(f.Value)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return pd, nil
}

// parseAcceptedCredentials decodes the AcceptedCredentials STArray content; each
// element is a Credential STObject.
func parseAcceptedCredentials(content []byte) []PermissionedDomainCredential {
	var creds []PermissionedDomainCredential
	_ = WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject || elem.FieldCode != 33 { // Credential
			return nil
		}
		var c PermissionedDomainCredential
		_ = WalkFields(elem.Value, func(inner Field) error {
			switch inner.TypeCode {
			case stAccountID:
				if inner.FieldCode == 4 { // Issuer
					if id, ok := inner.AccountID(); ok {
						c.Issuer = id
					}
				}
			case stBlob:
				if inner.FieldCode == 31 { // CredentialType
					c.CredentialType = append([]byte(nil), inner.VLBytes()...)
				}
			}
			return nil
		})
		creds = append(creds, c)
		return nil
	})
	return creds
}

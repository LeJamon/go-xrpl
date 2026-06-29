package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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

	// Emit only once threaded; a fresh entry's pointers are stamped by the apply layer.
	var emptyHash [32]byte
	if pd.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(pd.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = pd.PreviousTxnLgrSeq
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
			switch f.FieldCode {
			case 4: // Sequence
				pd.Sequence = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				pd.PreviousTxnLgrSeq = f.UInt32()
			}
		case stUInt64:
			if f.FieldCode == 4 { // OwnerNode
				pd.OwnerNode = f.UInt64()
			}
		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				pd.PreviousTxnID = f.Hash256()
			}
		case stAccountID:
			if f.FieldCode == 2 { // Owner
				if id, ok := f.AccountID(); ok {
					pd.Owner = id
				}
			}
		case stArray:
			if f.FieldCode == 28 { // AcceptedCredentials
				creds, err := parseAcceptedCredentials(f.Value)
				if err != nil {
					return err
				}
				pd.AcceptedCredentials = creds
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
func parseAcceptedCredentials(content []byte) ([]PermissionedDomainCredential, error) {
	var creds []PermissionedDomainCredential
	err := WalkFields(content, func(elem Field) error {
		if elem.TypeCode != stObject || elem.FieldCode != 33 { // Credential
			return nil
		}
		var c PermissionedDomainCredential
		if err := WalkFields(elem.Value, func(inner Field) error {
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
		}); err != nil {
			return err
		}
		creds = append(creds, c)
		return nil
	})
	return creds, err
}

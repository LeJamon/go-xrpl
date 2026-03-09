package state

import (
	"encoding/hex"
	"fmt"
	"strconv"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
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
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, err
	}

	pd := &PermissionedDomainData{}

	if owner, ok := jsonObj["Owner"].(string); ok {
		ownerID, err := DecodeAccountID(owner)
		if err == nil {
			pd.Owner = ownerID
		}
	}

	if seq := jsonObj["Sequence"]; seq != nil {
		switch v := seq.(type) {
		case float64:
			pd.Sequence = uint32(v)
		case uint32:
			pd.Sequence = v
		case int:
			pd.Sequence = uint32(v)
		}
	}

	if ownerNode, ok := jsonObj["OwnerNode"].(string); ok {
		pd.OwnerNode, _ = strconv.ParseUint(ownerNode, 16, 64)
	}

	if creds, ok := jsonObj["AcceptedCredentials"].([]any); ok {
		for _, credItem := range creds {
			credWrapper, ok := credItem.(map[string]any)
			if !ok {
				continue
			}
			credData, ok := credWrapper["Credential"].(map[string]any)
			if !ok {
				continue
			}
			var c PermissionedDomainCredential
			if issuer, ok := credData["Issuer"].(string); ok {
				issuerID, err := DecodeAccountID(issuer)
				if err == nil {
					c.Issuer = issuerID
				}
			}
			if credType, ok := credData["CredentialType"].(string); ok {
				c.CredentialType, _ = hex.DecodeString(credType)
			}
			pd.AcceptedCredentials = append(pd.AcceptedCredentials, c)
		}
	}

	return pd, nil
}

package credential

import (
	"encoding/hex"
	"fmt"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/crypto/common"
)

// Credential ledger entry flags
const (
	// LsfAccepted indicates the credential has been accepted by the subject
	LsfCredentialAccepted uint32 = 0x00010000
)

// CredentialEntry represents a Credential ledger entry
// Reference: rippled ledger_entries.macro ltCREDENTIAL (0x0081)
type CredentialEntry struct {
	Subject        [20]byte // Account the credential is about
	Issuer         [20]byte // Account that issued the credential
	CredentialType []byte   // Type of credential (max 64 bytes)
	Expiration     *uint32  // Optional expiration time
	URI            []byte   // Optional URI (max 256 bytes)
	Flags          uint32   // Credential flags (lsfAccepted)

	// Directory node hints
	IssuerNode  uint64
	SubjectNode uint64

	// Transaction threading
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// IsAccepted returns true if the credential has been accepted
func (c *CredentialEntry) IsAccepted() bool {
	return c.Flags&LsfCredentialAccepted != 0
}

// SetAccepted sets the accepted flag
func (c *CredentialEntry) SetAccepted() {
	c.Flags |= LsfCredentialAccepted
}

// credentialKeylet calculates the keylet for a credential
// Keylet = SHA512Half(spaceCredential + subject + issuer + credentialType)
func credentialKeylet(subject, issuer [20]byte, credentialType []byte) [32]byte {
	// Space for Credential is 0x0044 ('D')
	const spaceCredential = 0x0044

	// Build the hash input: space (2 bytes) + subject (20 bytes) + issuer (20 bytes) + credentialType
	input := make([]byte, 2+20+20+len(credentialType))
	input[0] = byte(spaceCredential >> 8)
	input[1] = byte(spaceCredential & 0xFF)
	copy(input[2:22], subject[:])
	copy(input[22:42], issuer[:])
	copy(input[42:], credentialType)

	return common.Sha512Half(input)
}

// ParseCredentialEntry parses a Credential ledger entry from binary data
func ParseCredentialEntry(data []byte) (*CredentialEntry, error) {
	hexStr := hex.EncodeToString(data)
	jsonObj, err := binarycodec.Decode(hexStr)
	if err != nil {
		return nil, err
	}

	cred := &CredentialEntry{}

	// Parse Subject
	if subject, ok := jsonObj["Subject"].(string); ok {
		subjectID, err := state.DecodeAccountID(subject)
		if err == nil {
			cred.Subject = subjectID
		}
	}

	// Parse Issuer
	if issuer, ok := jsonObj["Issuer"].(string); ok {
		issuerID, err := state.DecodeAccountID(issuer)
		if err == nil {
			cred.Issuer = issuerID
		}
	}

	// Parse CredentialType (Blob/VL field stored as hex)
	if credType, ok := jsonObj["CredentialType"].(string); ok {
		decoded, err := hex.DecodeString(credType)
		if err == nil {
			cred.CredentialType = decoded
		}
	}

	// Parse Expiration (optional)
	// The binary codec returns UInt32 fields as native uint32, not float64.
	if exp := jsonObj["Expiration"]; exp != nil {
		switch v := exp.(type) {
		case uint32:
			cred.Expiration = &v
		case float64:
			expVal := uint32(v)
			cred.Expiration = &expVal
		case int:
			expVal := uint32(v)
			cred.Expiration = &expVal
		case int64:
			expVal := uint32(v)
			cred.Expiration = &expVal
		}
	}

	// Parse URI (optional, Blob/VL field stored as hex)
	if uri, ok := jsonObj["URI"].(string); ok {
		decoded, err := hex.DecodeString(uri)
		if err == nil {
			cred.URI = decoded
		}
	}

	// Parse Flags - handle multiple possible types from JSON decoder
	if flags := jsonObj["Flags"]; flags != nil {
		switch v := flags.(type) {
		case float64:
			cred.Flags = uint32(v)
		case uint32:
			cred.Flags = v
		case int:
			cred.Flags = uint32(v)
		case int64:
			cred.Flags = uint32(v)
		}
	}

	// Parse IssuerNode
	if issuerNode, ok := jsonObj["IssuerNode"].(string); ok {
		cred.IssuerNode, _ = tx.ParseUint64Hex(issuerNode)
	}

	// Parse SubjectNode
	if subjectNode, ok := jsonObj["SubjectNode"].(string); ok {
		cred.SubjectNode, _ = tx.ParseUint64Hex(subjectNode)
	}

	// Parse PreviousTxnID
	if prevTxnID, ok := jsonObj["PreviousTxnID"].(string); ok {
		bytes, _ := hex.DecodeString(prevTxnID)
		copy(cred.PreviousTxnID[:], bytes)
	}

	// Parse PreviousTxnLgrSeq
	if prevSeq, ok := jsonObj["PreviousTxnLgrSeq"].(float64); ok {
		cred.PreviousTxnLgrSeq = uint32(prevSeq)
	}

	return cred, nil
}

// serializeCredentialEntry serializes a Credential entry to binary format
func serializeCredentialEntry(cred *CredentialEntry) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "Credential",
	}

	// Add Subject
	subjectStr, err := state.EncodeAccountID(cred.Subject)
	if err == nil && subjectStr != "" {
		jsonObj["Subject"] = subjectStr
	}

	// Add Issuer
	issuerStr, err := state.EncodeAccountID(cred.Issuer)
	if err == nil && issuerStr != "" {
		jsonObj["Issuer"] = issuerStr
	}

	// Add CredentialType (hex-encoded)
	if len(cred.CredentialType) > 0 {
		jsonObj["CredentialType"] = hex.EncodeToString(cred.CredentialType)
	}

	// Add Expiration (optional)
	if cred.Expiration != nil {
		jsonObj["Expiration"] = *cred.Expiration
	}

	// Add URI (optional, hex-encoded)
	if len(cred.URI) > 0 {
		jsonObj["URI"] = hex.EncodeToString(cred.URI)
	}

	// Add Flags
	if cred.Flags != 0 {
		jsonObj["Flags"] = cred.Flags
	}

	// Add IssuerNode
	jsonObj["IssuerNode"] = tx.FormatUint64Hex(cred.IssuerNode)

	// Add SubjectNode (if subject != issuer)
	if cred.Subject != cred.Issuer {
		jsonObj["SubjectNode"] = tx.FormatUint64Hex(cred.SubjectNode)
	}

	// Add PreviousTxnID
	var zeroHash [32]byte
	if cred.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = hex.EncodeToString(cred.PreviousTxnID[:])
	}

	// Add PreviousTxnLgrSeq
	if cred.PreviousTxnLgrSeq > 0 {
		jsonObj["PreviousTxnLgrSeq"] = cred.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// checkCredentialExpired checks if a credential has expired
// Reference: rippled CredentialHelpers.cpp checkExpired()
func checkCredentialExpired(cred *CredentialEntry, closeTime uint32) bool {
	if cred.Expiration == nil {
		return false
	}
	return closeTime > *cred.Expiration
}

// DeleteSLE deletes a credential from the ledger, removing it from both the
// issuer's and subject's owner directories and adjusting owner counts.
// Reference: rippled CredentialHelpers.cpp credentials::deleteSLE()
func DeleteSLE(view tx.LedgerView, credKey keylet.Keylet, cred *CredentialEntry) error {
	// Remove from issuer's owner directory
	issuerDirKey := keylet.OwnerDir(cred.Issuer)
	_, err := state.DirRemove(view, issuerDirKey, cred.IssuerNode, credKey.Key, false)
	if err != nil {
		return fmt.Errorf("failed to remove credential from issuer directory: %w", err)
	}

	// Adjust issuer's owner count if they own the credential slot
	// Owner logic: if not accepted, issuer owns it. If accepted and subject==issuer, issuer owns it.
	issuerOwns := !cred.IsAccepted() || (cred.Subject == cred.Issuer)
	if issuerOwns {
		if err := adjustOwnerCount(view, cred.Issuer, -1); err != nil {
			return err
		}
	}

	// Remove from subject's owner directory (if different from issuer)
	if cred.Subject != cred.Issuer {
		subjectDirKey := keylet.OwnerDir(cred.Subject)
		_, err := state.DirRemove(view, subjectDirKey, cred.SubjectNode, credKey.Key, false)
		if err != nil {
			return fmt.Errorf("failed to remove credential from subject directory: %w", err)
		}

		// Adjust subject's owner count if they own the credential slot
		if cred.IsAccepted() {
			if err := adjustOwnerCount(view, cred.Subject, -1); err != nil {
				return err
			}
		}
	}

	// Erase the credential from the ledger
	if err := view.Erase(credKey); err != nil {
		return fmt.Errorf("failed to erase credential: %w", err)
	}

	return nil
}

// adjustOwnerCount reads an account, adjusts its OwnerCount, and writes it back.
func adjustOwnerCount(view tx.LedgerView, accountID [20]byte, delta int) error {
	return tx.AdjustOwnerCount(view, accountID, delta)
}

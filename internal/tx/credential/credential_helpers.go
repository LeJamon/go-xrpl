package credential

import (
	"encoding/hex"
	"fmt"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
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

	subjectStr, err := state.EncodeAccountID(cred.Subject)
	if err == nil && subjectStr != "" {
		jsonObj["Subject"] = subjectStr
	}

	issuerStr, err := state.EncodeAccountID(cred.Issuer)
	if err == nil && issuerStr != "" {
		jsonObj["Issuer"] = issuerStr
	}

	if len(cred.CredentialType) > 0 {
		jsonObj["CredentialType"] = hex.EncodeToString(cred.CredentialType)
	}

	if cred.Expiration != nil {
		jsonObj["Expiration"] = *cred.Expiration
	}

	if len(cred.URI) > 0 {
		jsonObj["URI"] = hex.EncodeToString(cred.URI)
	}

	if cred.Flags != 0 {
		jsonObj["Flags"] = cred.Flags
	}

	// sfIssuerNode and sfSubjectNode are both soeREQUIRED on ltCREDENTIAL, so
	// rippled always serializes them — including SubjectNode:0 for a self-issued
	// credential (subject == issuer), where doApply leaves it at the template
	// default instead of inserting into the subject's directory.
	// Reference: rippled Credentials.cpp:175,180-195; ledger_entries.macro ltCREDENTIAL.
	jsonObj["IssuerNode"] = tx.FormatUint64Hex(cred.IssuerNode)
	jsonObj["SubjectNode"] = tx.FormatUint64Hex(cred.SubjectNode)

	var zeroHash [32]byte
	if cred.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = hex.EncodeToString(cred.PreviousTxnID[:])
	}

	if cred.PreviousTxnLgrSeq > 0 {
		jsonObj["PreviousTxnLgrSeq"] = cred.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, err
	}

	return hex.DecodeString(hexStr)
}

// CheckCredentialExpired checks if a credential has expired
// Reference: rippled CredentialHelpers.cpp checkExpired()
func CheckCredentialExpired(cred *CredentialEntry, closeTime uint32) bool {
	if cred.Expiration == nil {
		return false
	}
	return closeTime > *cred.Expiration
}

// ValidateCredentialIDs validates a transaction's CredentialIDs: each
// credential must exist in the ledger, have the transaction sender as its
// Subject, and be accepted, otherwise tecBAD_CREDENTIALS. When checkExpiry is
// set, a credential whose Expiration is at or before the parent close time
// returns tecEXPIRED.
// Reference: rippled CredentialHelpers.cpp credentials::valid()
func ValidateCredentialIDs(ctx *tx.ApplyContext, credentialIDs []string, checkExpiry bool) tx.Result {
	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TecBAD_CREDENTIALS
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credData, err := ctx.View.Read(keylet.CredentialByID(credID))
		if err != nil || credData == nil {
			return tx.TecBAD_CREDENTIALS
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		if cred.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		if !cred.IsAccepted() {
			return tx.TecBAD_CREDENTIALS
		}

		if checkExpiry && cred.Expiration != nil && ctx.Config.ParentCloseTime >= *cred.Expiration {
			return tx.TecEXPIRED
		}
	}

	return tx.TesSUCCESS
}

// RemoveExpiredCredentials deletes any expired credentials in credentialIDs
// from the ledger, adjusting owner directories and counts. It returns true if
// at least one credential was expired.
// Reference: rippled CredentialHelpers.cpp credentials::removeExpired()
func RemoveExpiredCredentials(ctx *tx.ApplyContext, credentialIDs []string) bool {
	closeTime := ctx.Config.ParentCloseTime
	anyExpired := false

	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			continue
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			continue
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			continue
		}

		if CheckCredentialExpired(cred, closeTime) {
			_ = DeleteSLE(ctx.View, credKey, cred)
			anyExpired = true
		}
	}

	return anyExpired
}

// DeleteSLE deletes a credential from the ledger, removing it from both the
// issuer's and subject's owner directories and adjusting owner counts.
// Reference: rippled CredentialHelpers.cpp credentials::deleteSLE()
func DeleteSLE(view tx.LedgerView, credKey keylet.Keylet, cred *CredentialEntry) error {
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

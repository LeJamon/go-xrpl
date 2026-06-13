package credential

import (
	"bytes"
	"encoding/hex"
	"sort"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// Credential ledger entry flags
const (
	// LsfCredentialAccepted indicates the credential has been accepted by the subject.
	LsfCredentialAccepted = entry.LsfAccepted
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

	// sfFlags is a soeREQUIRED common field on every ledger entry, so rippled
	// always serializes it (including Flags:0 before lsfAccepted is set). Omitting
	// it when zero diverges the SLE state and drops PreviousFields.Flags on accept.
	jsonObj["Flags"] = cred.Flags

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

// CheckFields validates a transaction's CredentialIDs field shape, matching
// rippled's credentials::checkFields(): when the field is present it must hold
// between 1 and maxCredentialsArraySize (8) entries with no duplicates. present
// must reflect whether the field was supplied (callers compute it from the
// slice plus HasField, since an empty array parses back to a nil slice under
// omitempty). dupDetail is the detail string used for the duplicate error so
// each call site keeps its existing message. A malformed field returns
// temMALFORMED.
// Reference: rippled CredentialHelpers.cpp credentials::checkFields().
func CheckFields(ids []string, present bool, dupDetail string) error {
	if !present {
		return nil
	}
	if len(ids) == 0 || len(ids) > 8 {
		return tx.Errorf(tx.TemMALFORMED, "CredentialIDs array size is invalid")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return tx.Errorf(tx.TemMALFORMED, "%s", dupDetail)
		}
		seen[id] = true
	}
	return nil
}

// ValidateCredentialIDs validates a transaction's CredentialIDs: each
// credential must exist in the ledger, have the transaction sender as its
// Subject, and be accepted, otherwise tecBAD_CREDENTIALS. Expiry is never
// checked here — it is deferred to RemoveExpiredCredentials.
// Reference: rippled CredentialHelpers.cpp credentials::valid()
func ValidateCredentialIDs(ctx *tx.ApplyContext, credentialIDs []string) tx.Result {
	return ValidCredentials(ctx.View, ctx.AccountID, credentialIDs)
}

// ValidCredentials is the view-based form of ValidateCredentialIDs, usable from
// Preclaim where only a LedgerView (not an ApplyContext) is available.
func ValidCredentials(view tx.LedgerView, subject [20]byte, credentialIDs []string) tx.Result {
	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TecBAD_CREDENTIALS
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credData, err := view.Read(keylet.CredentialByID(credID))
		if err != nil || credData == nil {
			return tx.TecBAD_CREDENTIALS
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		if cred.Subject != subject {
			return tx.TecBAD_CREDENTIALS
		}

		if !cred.IsAccepted() {
			return tx.TecBAD_CREDENTIALS
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
			_ = DeleteSLE(ctx, credKey, cred)
			anyExpired = true
		}
	}

	return anyExpired
}

// VerifyDepositPreauth enforces deposit authorization for a transaction
// moving funds from src to dst. Expired credentials in credentialIDs are
// deleted first, failing the transaction with tecEXPIRED if any were expired
// (the deletion is re-applied on the tec path via ApplyOnTec). If dst has
// lsfDepositAuth set and src != dst, the deposit must be preauthorized by
// dst, either by account or by the supplied credentials.
// Reference: rippled CredentialHelpers.cpp verifyDepositPreauth()
func VerifyDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, src, dst [20]byte, dstAccount *state.AccountRoot) tx.Result {
	credentialsPresent := len(credentialIDs) > 0

	if credentialsPresent && RemoveExpiredCredentials(ctx, credentialIDs) {
		return tx.TecEXPIRED
	}

	if dstAccount != nil && (dstAccount.Flags&state.LsfDepositAuth) != 0 && src != dst {
		if exists, _ := ctx.View.Exists(keylet.DepositPreauth(dst, src)); !exists {
			if !credentialsPresent {
				return tx.TecNO_PERMISSION
			}
			return authorizedDepositPreauth(ctx, credentialIDs, dst)
		}
	}

	return tx.TesSUCCESS
}

// authorizedDepositPreauth checks whether the (Issuer, CredentialType) pairs
// of the supplied credentials match a credential-based DepositPreauth entry
// on dst. A duplicate pair is reported as tefINTERNAL: it cannot occur for
// credentials that passed preflight and preclaim, since credential IDs are
// deduplicated there and all credentials share the sender as Subject.
// Reference: rippled CredentialHelpers.cpp credentials::authorizedDepositPreauth()
func authorizedDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, dst [20]byte) tx.Result {
	pairs := make([]keylet.CredentialPair, 0, len(credentialIDs))
	seen := make(map[string]bool, len(credentialIDs))

	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TefINTERNAL
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		// Credential existence was already checked in preclaim.
		credData, err := ctx.View.Read(keylet.CredentialByID(credID))
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		pairKey := hex.EncodeToString(cred.Issuer[:]) + ":" + hex.EncodeToString(cred.CredentialType)
		if seen[pairKey] {
			return tx.TefINTERNAL
		}
		seen[pairKey] = true

		pairs = append(pairs, keylet.CredentialPair{Issuer: cred.Issuer, CredentialType: cred.CredentialType})
	}

	// Sort pairs by (Issuer, CredentialType) to match rippled's sorted set,
	// which the credential-based DepositPreauth keylet is computed over.
	sort.Slice(pairs, func(i, j int) bool {
		if c := bytes.Compare(pairs[i].Issuer[:], pairs[j].Issuer[:]); c != 0 {
			return c < 0
		}
		return bytes.Compare(pairs[i].CredentialType, pairs[j].CredentialType) < 0
	})

	if exists, _ := ctx.View.Exists(keylet.DepositPreauthCredentials(dst, pairs)); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// DeleteSLE deletes a credential from the ledger, removing it from both the
// issuer's and subject's owner directories and adjusting owner counts on the
// view. Owner counts are written through the view so the deletion persists on
// the tec-recovery path (removeExpiredCredentials), where ctx.Account is a
// discarded copy. Success-path callers whose sender owns the credential must
// resync ctx.Account from the view afterwards. A missing owner account yields
// tecINTERNAL and a failed directory removal tefBAD_LEDGER, matching rippled.
// Reference: rippled CredentialHelpers.cpp credentials::deleteSLE()
func DeleteSLE(ctx *tx.ApplyContext, credKey keylet.Keylet, cred *CredentialEntry) tx.Result {
	removeFromDir := func(account [20]byte, page uint64, isOwner bool) tx.Result {
		if exists, err := ctx.View.Exists(keylet.Account(account)); err != nil || !exists {
			return tx.TecINTERNAL
		}
		result, err := state.DirRemove(ctx.View, keylet.OwnerDir(account), page, credKey.Key, false)
		if err != nil || result == nil || !result.Success {
			return tx.TefBAD_LEDGER
		}
		if isOwner {
			if err := tx.AdjustOwnerCount(ctx.View, account, -1); err != nil {
				return tx.TefBAD_LEDGER
			}
		}
		return tx.TesSUCCESS
	}

	// If not accepted, the issuer owns it; if accepted and subject == issuer,
	// the issuer owns it.
	issuerOwns := !cred.IsAccepted() || (cred.Subject == cred.Issuer)
	if result := removeFromDir(cred.Issuer, cred.IssuerNode, issuerOwns); result != tx.TesSUCCESS {
		return result
	}

	if cred.Subject != cred.Issuer {
		if result := removeFromDir(cred.Subject, cred.SubjectNode, cred.IsAccepted()); result != tx.TesSUCCESS {
			return result
		}
	}

	if err := ctx.View.Erase(credKey); err != nil {
		return tx.TefINTERNAL
	}
	return tx.TesSUCCESS
}

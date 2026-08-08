package credential

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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
	IssuerNode     uint64
	SubjectNode    uint64
	HasSubjectNode bool

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
	var decoded entry.Credential
	if err := decoded.Decode(data); err != nil {
		return nil, fmt.Errorf("parse credential: %w", err)
	}
	fields := decoded.ToMap()

	subject, err := state.DecodeAccountID(decoded.Subject)
	if err != nil {
		return nil, fmt.Errorf("parse credential Subject: %w", err)
	}
	issuer, err := state.DecodeAccountID(decoded.Issuer)
	if err != nil {
		return nil, fmt.Errorf("parse credential Issuer: %w", err)
	}
	credentialType, err := hex.DecodeString(decoded.CredentialType)
	if err != nil {
		return nil, fmt.Errorf("parse credential CredentialType: %w", err)
	}
	issuerNode, err := tx.ParseUint64Hex(decoded.IssuerNode)
	if err != nil {
		return nil, fmt.Errorf("parse credential IssuerNode: %w", err)
	}

	cred := &CredentialEntry{
		Subject:           subject,
		Issuer:            issuer,
		CredentialType:    credentialType,
		Flags:             decoded.Flags,
		IssuerNode:        issuerNode,
		PreviousTxnLgrSeq: decoded.PreviousTxnLgrSeq,
	}

	if _, ok := fields["Expiration"]; ok {
		expiration := decoded.Expiration
		cred.Expiration = &expiration
	}

	if _, ok := fields["URI"]; ok {
		cred.URI, err = hex.DecodeString(decoded.URI)
		if err != nil {
			return nil, fmt.Errorf("parse credential URI: %w", err)
		}
	}

	if _, ok := fields["SubjectNode"]; ok {
		cred.SubjectNode, err = tx.ParseUint64Hex(decoded.SubjectNode)
		if err != nil {
			return nil, fmt.Errorf("parse credential SubjectNode: %w", err)
		}
		cred.HasSubjectNode = true
	}

	if _, ok := fields["PreviousTxnID"]; ok {
		previousTxnID, err := hex.DecodeString(decoded.PreviousTxnID)
		if err != nil {
			return nil, fmt.Errorf("parse credential PreviousTxnID: %w", err)
		}
		if len(previousTxnID) != len(cred.PreviousTxnID) {
			return nil, fmt.Errorf("parse credential PreviousTxnID: decoded length %d, want %d", len(previousTxnID), len(cred.PreviousTxnID))
		}
		copy(cred.PreviousTxnID[:], previousTxnID)
	}

	return cred, nil
}

// serializeCredentialEntry serializes a Credential entry to binary format
func serializeCredentialEntry(cred *CredentialEntry) ([]byte, error) {
	if cred == nil {
		return nil, errors.New("serialize credential: nil entry")
	}

	subjectStr, err := state.EncodeAccountID(cred.Subject)
	if err != nil {
		return nil, fmt.Errorf("serialize credential subject: %w", err)
	}
	if subjectStr == "" {
		return nil, errors.New("serialize credential: empty subject")
	}

	issuerStr, err := state.EncodeAccountID(cred.Issuer)
	if err != nil {
		return nil, fmt.Errorf("serialize credential issuer: %w", err)
	}
	if issuerStr == "" {
		return nil, errors.New("serialize credential: empty issuer")
	}

	if len(cred.CredentialType) == 0 {
		return nil, errors.New("serialize credential: empty credential type")
	}

	var sle entry.Credential
	sle.SetSubject(subjectStr)
	sle.SetIssuer(issuerStr)
	sle.SetCredentialType(hex.EncodeToString(cred.CredentialType))
	sle.SetIssuerNode(tx.FormatUint64Hex(cred.IssuerNode))
	sle.SetFlags(cred.Flags)

	if cred.Expiration != nil {
		sle.SetExpiration(*cred.Expiration)
	}

	if len(cred.URI) > 0 {
		sle.SetURI(hex.EncodeToString(cred.URI))
	}

	if cred.HasSubjectNode && cred.Subject != cred.Issuer {
		sle.SetSubjectNode(tx.FormatUint64Hex(cred.SubjectNode))
	}

	var zeroHash [32]byte
	if cred.PreviousTxnID != zeroHash {
		sle.SetPreviousTxnID(hex.EncodeToString(cred.PreviousTxnID[:]))
		sle.SetPreviousTxnLgrSeq(cred.PreviousTxnLgrSeq)
	} else if cred.PreviousTxnLgrSeq != 0 {
		return nil, errors.New("serialize credential: PreviousTxnLgrSeq set without PreviousTxnID")
	}

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("serialize credential: %w", err)
	}
	return data, nil
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
		return ter.Errorf(ter.TemMALFORMED, "CredentialIDs array size is invalid")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return ter.Errorf(ter.TemMALFORMED, "%s", dupDetail)
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
func ValidateCredentialIDs(ctx *tx.ApplyContext, credentialIDs []string) ter.Result {
	return ValidCredentials(ctx.View, ctx.AccountID, credentialIDs)
}

// ValidCredentials is the view-based form of ValidateCredentialIDs, usable from
// Preclaim where only a LedgerView (not an ApplyContext) is available.
func ValidCredentials(view tx.LedgerView, subject [20]byte, credentialIDs []string) ter.Result {
	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return ter.TecBAD_CREDENTIALS
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credData, err := view.Read(keylet.CredentialByID(credID))
		if err != nil || credData == nil {
			return ter.TecBAD_CREDENTIALS
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			return ter.TecBAD_CREDENTIALS
		}

		if cred.Subject != subject {
			return ter.TecBAD_CREDENTIALS
		}

		if !cred.IsAccepted() {
			return ter.TecBAD_CREDENTIALS
		}
	}

	return ter.TesSUCCESS
}

// ValidDomain checks whether subject has an accepted, unexpired credential
// matching a permissioned domain. It is read-only so callers that suppress
// tecEXPIRED during preclaim must call VerifyValidDomain during apply.
func ValidDomain(view state.LedgerView, domainID [32]byte, subject [20]byte, closeTime uint32) ter.Result {
	domain, result := readPermissionedDomain(view, domainID)
	if result != ter.TesSUCCESS {
		return result
	}

	foundExpired := false
	for _, accepted := range domain.AcceptedCredentials {
		credentialRaw, err := view.Read(keylet.Credential(subject, accepted.Issuer, accepted.CredentialType))
		if err != nil {
			return ter.TefINTERNAL
		}
		if credentialRaw == nil {
			continue
		}
		credentialEntry, err := ParseCredentialEntry(credentialRaw)
		if err != nil {
			return ter.TefINTERNAL
		}
		if CheckCredentialExpired(credentialEntry, closeTime) {
			foundExpired = true
			continue
		}
		if credentialEntry.IsAccepted() {
			return ter.TesSUCCESS
		}
	}
	if foundExpired {
		return ter.TecEXPIRED
	}
	return ter.TecNO_AUTH
}

func readPermissionedDomain(view state.LedgerView, domainID [32]byte) (*state.PermissionedDomainData, ter.Result) {
	raw, err := view.Read(keylet.PermissionedDomainByID(domainID))
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if raw == nil {
		return nil, ter.TecOBJECT_NOT_FOUND
	}
	domain, err := state.ParsePermissionedDomain(raw)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	return domain, ter.TesSUCCESS
}

func domainCredentialIDs(view state.LedgerView, domainID [32]byte, subject [20]byte) ([]string, ter.Result) {
	domain, result := readPermissionedDomain(view, domainID)
	if result != ter.TesSUCCESS {
		return nil, result
	}

	ids := make([]string, 0, len(domain.AcceptedCredentials))
	for _, accepted := range domain.AcceptedCredentials {
		credentialKey := keylet.Credential(subject, accepted.Issuer, accepted.CredentialType)
		exists, err := view.Exists(credentialKey)
		if err != nil {
			return nil, ter.TefINTERNAL
		}
		if exists {
			ids = append(ids, hex.EncodeToString(credentialKey.Key[:]))
		}
	}
	return ids, ter.TesSUCCESS
}

// VerifyValidDomain removes expired matching credentials and then verifies that
// an accepted credential remains. Deletion failures are propagated when
// fixCleanup3_1_3 is enabled by RemoveExpiredCredentials.
func VerifyValidDomain(ctx *tx.ApplyContext, subject [20]byte, domainID [32]byte) ter.Result {
	ids, result := domainCredentialIDs(ctx.View, domainID, subject)
	if result != ter.TesSUCCESS {
		return result
	}
	foundExpired, result := RemoveExpiredCredentials(ctx, ids)
	if foundExpired && subject == ctx.AccountID {
		ctx.SyncSenderOwnerCount()
	}
	if result != ter.TesSUCCESS {
		return result
	}

	for _, idHex := range ids {
		idBytes, err := hex.DecodeString(idHex)
		if err != nil || len(idBytes) != 32 {
			return ter.TefINTERNAL
		}
		var id [32]byte
		copy(id[:], idBytes)
		raw, err := ctx.View.Read(keylet.CredentialByID(id))
		if err != nil {
			return ter.TefINTERNAL
		}
		if raw == nil {
			continue
		}
		credentialEntry, err := ParseCredentialEntry(raw)
		if err != nil {
			return ter.TefINTERNAL
		}
		if credentialEntry.IsAccepted() {
			return ter.TesSUCCESS
		}
	}
	if foundExpired {
		return ter.TecEXPIRED
	}
	return ter.TecNO_PERMISSION
}

// RemoveExpiredDomainCredentialsOnTec reapplies permissioned-domain credential
// cleanup after a tecEXPIRED sandbox rollback.
func RemoveExpiredDomainCredentialsOnTec(ctx *tx.ApplyContext, subject [20]byte, domainID [32]byte) {
	ids, result := domainCredentialIDs(ctx.View, domainID, subject)
	if result == ter.TesSUCCESS {
		RemoveExpiredCredentialsOnTec(ctx, ids)
	}
}

// removeExpired is the shared per-credential deletion loop. anyExpired reports
// whether any credential was expired; failTER is the first failing deletion TER
// (tesSUCCESS if none failed). When stopOnFailure is true it returns immediately
// on the first deletion failure — the success-path behaviour rippled gates on
// fixCleanup3_1_3; otherwise every expired credential is processed and failures
// are only logged (the tec-recovery cleanup).
// Reference: rippled CredentialHelpers.cpp credentials::removeExpired()
func removeExpired(ctx *tx.ApplyContext, credentialIDs []string, stopOnFailure bool) (anyExpired bool, failTER ter.Result) {
	closeTime := ctx.Config.ParentCloseTime
	failTER = ter.TesSUCCESS

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
			if r := DeleteSLE(ctx, credKey, cred); r != ter.TesSUCCESS {
				ctx.Log.Error("removeExpiredCredentials: failed to delete expired credential", "ter", r.String())
				if stopOnFailure {
					return anyExpired, r
				}
				if failTER == ter.TesSUCCESS {
					failTER = r
				}
			}
			anyExpired = true
		}
	}

	return anyExpired, failTER
}

// RemoveExpiredCredentials deletes any expired credentials in credentialIDs on a
// transaction's success path, adjusting owner directories and counts. It returns
// whether at least one credential was expired and the TER to abort with. Under
// fixCleanup3_1_3 a deletion failure aborts the transaction (returns the failing
// TER); before the amendment the failure is swallowed (returns tesSUCCESS),
// matching rippled removeExpired.
func RemoveExpiredCredentials(ctx *tx.ApplyContext, credentialIDs []string) (bool, ter.Result) {
	fix313 := ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3)
	anyExpired, failTER := removeExpired(ctx, credentialIDs, fix313)
	if fix313 {
		return anyExpired, failTER
	}
	return anyExpired, ter.TesSUCCESS
}

// RemoveExpiredCredentialsOnTec runs the tec-recovery cleanup: every expired
// credential is deleted and a deletion failure is only logged, never propagated,
// matching rippled Transactor::removeExpiredCredentials. This path is unchanged
// by fixCleanup3_1_3.
func RemoveExpiredCredentialsOnTec(ctx *tx.ApplyContext, credentialIDs []string) {
	removeExpired(ctx, credentialIDs, false)
}

// VerifyDepositPreauth enforces deposit authorization for a transaction
// moving funds from src to dst. Expired credentials in credentialIDs are
// deleted first, failing the transaction with tecEXPIRED if any were expired
// (the deletion is re-applied on the tec path via ApplyOnTec). If dst has
// lsfDepositAuth set and src != dst, the deposit must be preauthorized by
// dst, either by account or by the supplied credentials.
// Reference: rippled CredentialHelpers.cpp verifyDepositPreauth()
func VerifyDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, src, dst [20]byte, dstAccount *state.AccountRoot) ter.Result {
	credentialsPresent := len(credentialIDs) > 0

	if credentialsPresent {
		anyExpired, r := RemoveExpiredCredentials(ctx, credentialIDs)
		if r != ter.TesSUCCESS {
			return r
		}
		if anyExpired {
			return ter.TecEXPIRED
		}
	}

	if dstAccount != nil && (dstAccount.Flags&state.LsfDepositAuth) != 0 && src != dst {
		if exists, _ := ctx.View.Exists(keylet.DepositPreauth(dst, src)); !exists {
			if !credentialsPresent {
				return ter.TecNO_PERMISSION
			}
			return authorizedDepositPreauth(ctx, credentialIDs, dst)
		}
	}

	return ter.TesSUCCESS
}

// authorizedDepositPreauth checks whether the (Issuer, CredentialType) pairs
// of the supplied credentials match a credential-based DepositPreauth entry
// on dst. A duplicate pair is reported as tefINTERNAL: it cannot occur for
// credentials that passed preflight and preclaim, since credential IDs are
// deduplicated there and all credentials share the sender as Subject.
// Reference: rippled CredentialHelpers.cpp credentials::authorizedDepositPreauth()
func authorizedDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, dst [20]byte) ter.Result {
	pairs := make([]keylet.CredentialPair, 0, len(credentialIDs))
	seen := make(map[string]bool, len(credentialIDs))

	for _, idHex := range credentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return ter.TefINTERNAL
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		// Credential existence was already checked in preclaim.
		credData, err := ctx.View.Read(keylet.CredentialByID(credID))
		if err != nil || credData == nil {
			return ter.TefINTERNAL
		}

		cred, err := ParseCredentialEntry(credData)
		if err != nil {
			return ter.TefINTERNAL
		}

		pairKey := hex.EncodeToString(cred.Issuer[:]) + ":" + hex.EncodeToString(cred.CredentialType)
		if seen[pairKey] {
			return ter.TefINTERNAL
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
		return ter.TecNO_PERMISSION
	}

	return ter.TesSUCCESS
}

// DeleteSLE deletes a credential from the ledger, removing it from both the
// issuer's and subject's owner directories and adjusting owner counts on the
// view. Owner counts are written through the view so the deletion persists on
// the tec-recovery path (removeExpiredCredentials), where ctx.Account is a
// discarded copy. Success-path callers whose sender owns the credential must
// resync ctx.Account from the view afterwards. A missing owner account yields
// tecINTERNAL and a failed directory removal tefBAD_LEDGER, matching rippled.
// Reference: rippled CredentialHelpers.cpp credentials::deleteSLE()
func DeleteSLE(ctx *tx.ApplyContext, credKey keylet.Keylet, cred *CredentialEntry) ter.Result {
	if cred == nil {
		return ter.TecNO_ENTRY
	}

	removeFromDir := func(account [20]byte, page uint64, isOwner bool) ter.Result {
		if exists, err := ctx.View.Exists(keylet.Account(account)); err != nil || !exists {
			return ter.TecINTERNAL
		}
		result, err := state.DirRemove(ctx.View, keylet.OwnerDir(account), page, credKey.Key, false)
		if err != nil || result == nil || !result.Success {
			return ter.TefBAD_LEDGER
		}
		if isOwner {
			if err := tx.AdjustOwnerCount(ctx.View, account, -1); err != nil {
				return ter.TefBAD_LEDGER
			}
		}
		return ter.TesSUCCESS
	}

	// If not accepted, the issuer owns it; if accepted and subject == issuer,
	// the issuer owns it.
	issuerOwns := !cred.IsAccepted() || (cred.Subject == cred.Issuer)
	if result := removeFromDir(cred.Issuer, cred.IssuerNode, issuerOwns); result != ter.TesSUCCESS {
		return result
	}

	if cred.Subject != cred.Issuer {
		if result := removeFromDir(cred.Subject, cred.SubjectNode, cred.IsAccepted()); result != ter.TesSUCCESS {
			return result
		}
	}

	if err := ctx.View.Erase(credKey); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

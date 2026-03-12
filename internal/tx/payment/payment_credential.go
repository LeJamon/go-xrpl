package payment

import (
	"bytes"
	"encoding/hex"
	"sort"

	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// validateCredentials performs preclaim-level validation of CredentialIDs.
// Checks each credential exists in the ledger, belongs to the sender, and is accepted.
// Reference: rippled credentials::valid() in CredentialHelpers.cpp
func (p *Payment) validateCredentials(ctx *tx.ApplyContext) tx.Result {
	if len(p.CredentialIDs) == 0 {
		return tx.TesSUCCESS
	}

	for _, idHex := range p.CredentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TecBAD_CREDENTIALS
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TecBAD_CREDENTIALS
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		// Subject must be the transaction sender
		if cred.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		// Credential must be accepted
		if !cred.IsAccepted() {
			return tx.TecBAD_CREDENTIALS
		}
	}

	return tx.TesSUCCESS
}

// removeExpiredCredentials checks for expired credentials and deletes them.
// Returns true if any credentials were expired.
// Reference: rippled credentials::removeExpired() in CredentialHelpers.cpp
func (p *Payment) removeExpiredCredentials(ctx *tx.ApplyContext) bool {
	if len(p.CredentialIDs) == 0 {
		return false
	}

	closeTime := ctx.Config.ParentCloseTime
	anyExpired := false

	for _, idHex := range p.CredentialIDs {
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

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			continue
		}

		// Check expiration
		if cred.Expiration != nil && closeTime > *cred.Expiration {
			// Delete expired credential from ledger
			_ = credential.DeleteSLE(ctx.View, credKey, cred)
			anyExpired = true
		}
	}

	return anyExpired
}

// ApplyOnTec implements TecApplier. When tecEXPIRED is returned, this re-runs
// credential expiration deletion against the engine's view so the side-effects persist.
// Reference: rippled Transactor.cpp - tecEXPIRED re-applies removeExpiredCredentials
func (p *Payment) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	p.removeExpiredCredentials(ctx)
	return tx.TecEXPIRED
}

// authorizedDepositPreauth checks if the provided credentials match a
// credential-based DepositPreauth entry on the destination account.
// Reference: rippled credentials::authorizedDepositPreauth() in CredentialHelpers.cpp
func (p *Payment) authorizedDepositPreauth(ctx *tx.ApplyContext, dstAccountID [20]byte) tx.Result {
	// Read each credential, extract (Issuer, CredentialType) pairs
	type credPair struct {
		issuer   [20]byte
		credType []byte
	}
	pairs := make([]credPair, 0, len(p.CredentialIDs))

	seen := make(map[string]bool)
	for _, idHex := range p.CredentialIDs {
		credIDBytes, err := hex.DecodeString(idHex)
		if err != nil || len(credIDBytes) != 32 {
			return tx.TefINTERNAL
		}
		var credID [32]byte
		copy(credID[:], credIDBytes)

		credKey := keylet.CredentialByID(credID)
		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Build a dedup key from (Issuer, CredentialType)
		pairKey := credentialPairKey(cred.Issuer, cred.CredentialType)
		if seen[pairKey] {
			return tx.TefINTERNAL
		}
		seen[pairKey] = true

		pairs = append(pairs, credPair{issuer: cred.Issuer, credType: cred.CredentialType})
	}

	// Sort pairs by (issuer, credType) to match keylet computation
	sort.Slice(pairs, func(i, j int) bool {
		cmp := bytes.Compare(pairs[i].issuer[:], pairs[j].issuer[:])
		if cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(pairs[i].credType, pairs[j].credType) < 0
	})

	// Convert to keylet.CredentialPair
	keyletPairs := make([]keylet.CredentialPair, len(pairs))
	for i, cp := range pairs {
		keyletPairs[i] = keylet.CredentialPair{
			Issuer:         cp.issuer,
			CredentialType: cp.credType,
		}
	}

	// Check if credential-based DepositPreauth exists for destination
	preauthKey := keylet.DepositPreauthCredentials(dstAccountID, keyletPairs)
	if exists, _ := ctx.View.Exists(preauthKey); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}

// credentialPairKey returns a unique string key for deduplication of (issuer, credType) pairs.
func credentialPairKey(issuer [20]byte, credType []byte) string {
	return hex.EncodeToString(issuer[:]) + ":" + hex.EncodeToString(credType)
}

// verifyDepositPreauth checks deposit authorization for a payment.
// Reference: rippled verifyDepositPreauth() in Payment.cpp
func (p *Payment) verifyDepositPreauth(ctx *tx.ApplyContext, srcAccountID, dstAccountID [20]byte, dstAccount *state.AccountRoot) tx.Result {
	credentialsPresent := len(p.CredentialIDs) > 0

	// Remove expired credentials first
	if credentialsPresent {
		if p.removeExpiredCredentials(ctx) {
			return tx.TecEXPIRED
		}
	}

	// Check if destination requires deposit authorization
	if dstAccount != nil && (dstAccount.Flags&state.LsfDepositAuth) != 0 {
		// Self-payments always allowed
		if srcAccountID != dstAccountID {
			// Try account-based DepositPreauth first
			preauthKey := keylet.DepositPreauth(dstAccountID, srcAccountID)
			if exists, _ := ctx.View.Exists(preauthKey); !exists {
				// Account-based preauth not found — try credential-based
				if !credentialsPresent {
					return tx.TecNO_PERMISSION
				}
				return p.authorizedDepositPreauth(ctx, dstAccountID)
			}
		}
	}

	return tx.TesSUCCESS
}

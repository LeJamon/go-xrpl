package permissioneddomain

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/keylet"
)

// AccountInDomain checks if an account is a member of a permissioned domain.
// An account is in the domain if:
//   - It is the domain owner, OR
//   - It holds an accepted, non-expired credential matching one of the domain's
//     AcceptedCredentials.
//
// Reference: rippled app/misc/PermissionedDEXHelpers.cpp accountInDomain()
func AccountInDomain(view tx.LedgerView, accountID [20]byte, domainID [32]byte, parentCloseTime uint32) bool {
	domKey := keylet.PermissionedDomainByID(domainID)
	domData, err := view.Read(domKey)
	if err != nil || domData == nil {
		return false
	}
	pd, err := state.ParsePermissionedDomain(domData)
	if err != nil {
		return false
	}

	// Domain owner is always in the domain
	if pd.Owner == accountID {
		return true
	}

	// Check each accepted credential type
	for _, c := range pd.AcceptedCredentials {
		credKey := keylet.Credential(accountID, c.Issuer, c.CredentialType)
		credData, err := view.Read(credKey)
		if err != nil || credData == nil {
			continue
		}
		cred, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			continue
		}
		if !cred.IsAccepted() {
			continue
		}
		if credential.CheckCredentialExpired(cred, parentCloseTime) {
			continue
		}
		return true
	}

	return false
}

// OfferInDomain checks if an offer belongs to a permissioned domain and its
// owner is still in the domain (i.e., still holds the required credential).
// Reference: rippled app/misc/PermissionedDEXHelpers.cpp offerInDomain()
func OfferInDomain(view tx.LedgerView, offer *state.LedgerOffer, domainID [32]byte, parentCloseTime uint32) bool {
	var zeroDomain [32]byte
	if offer.DomainID == zeroDomain {
		return false
	}
	if offer.DomainID != domainID {
		return false
	}

	ownerID, err := state.DecodeAccountID(offer.Account)
	if err != nil {
		return false
	}
	return AccountInDomain(view, ownerID, domainID, parentCloseTime)
}

// ParseDomainID decodes a hex-encoded domain ID string to a [32]byte.
// Returns an error if the string is not a valid 64-character hex string.
// A zero domain ID is accepted: rippled treats sfDomainID as a plain Hash256
// and lets a zero value fail the on-ledger domain lookup naturally.
func ParseDomainID(hexStr string) ([32]byte, error) {
	var domainID [32]byte
	b, err := hex.DecodeString(hexStr)
	if err != nil || len(b) != 32 {
		return domainID, tx.Errorf(tx.TemMALFORMED, "DomainID must be a valid 256-bit hash")
	}
	copy(domainID[:], b)
	return domainID, nil
}

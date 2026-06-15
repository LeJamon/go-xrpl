package invariants

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// ValidPermissionedDomain
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPermissionedDomain (lines 1538-1635)
//
// Only checks for PermissionedDomainSet with tesSUCCESS.
// visitEntry: for PermissionedDomain entries with "after" data, validates:
//   - AcceptedCredentials array exists, is non-empty, has size <= 10
//   - All entries are unique
//   - Entries are sorted by (Issuer, CredentialType) lexicographically.

func checkValidPermissionedDomain(tx Transaction, result Result, entries []InvariantEntry) *InvariantViolation {
	if tx.TxType() != TypePermissionedDomainSet || result != TesSUCCESS {
		return nil
	}

	for _, e := range entries {
		// Only check PermissionedDomain entries that have an "after" state.
		if e.After == nil {
			continue
		}

		// Check both before and after: if before exists and is not PermissionedDomain, skip.
		// If after exists and is not PermissionedDomain, skip.
		// Reference: rippled lines 1544-1547
		if e.Before != nil {
			beforeType := state.EntryType(e.Before)
			if beforeType != "PermissionedDomain" {
				continue
			}
		}
		afterType := state.EntryType(e.After)
		if afterType != "PermissionedDomain" {
			continue
		}

		// Parse the PermissionedDomain from the "after" data.
		pd, err := state.ParsePermissionedDomain(e.After)
		if err != nil {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: fmt.Sprintf("could not parse PermissionedDomain SLE: %v", err),
			}
		}

		// Validate AcceptedCredentials.
		if v := validatePermissionedDomainCredentials(pd); v != nil {
			return v
		}
	}

	return nil
}

// credKey is a map key for checking credential uniqueness.
type credKey struct {
	issuer         [20]byte
	credentialType string // use string for map key
}

// validatePermissionedDomainCredentials checks that the AcceptedCredentials
// array is valid: non-empty, at most maxPermissionedDomainCredentials entries,
// unique, and sorted by (Issuer, CredentialType) lexicographically.
func validatePermissionedDomainCredentials(pd *state.PermissionedDomainData) *InvariantViolation {
	creds := pd.AcceptedCredentials

	// Check non-empty.
	if len(creds) == 0 {
		return &InvariantViolation{
			Name:    "ValidPermissionedDomain",
			Message: "permissioned domain with no rules",
		}
	}

	// Check max size.
	if len(creds) > maxPermissionedDomainCredentials {
		return &InvariantViolation{
			Name:    "ValidPermissionedDomain",
			Message: fmt.Sprintf("permissioned domain bad credentials size %d", len(creds)),
		}
	}

	// Check uniqueness and sorting.
	// Reference: rippled credentials::makeSorted() creates a
	// std::set<std::pair<AccountID, Slice>> — sorted by (Issuer, CredentialType)
	// lexicographically. If duplicates exist, the set is empty.
	// The invariant then checks that the stored array is in the same order as the sorted set.

	// Build sorted set and check for duplicates.
	seen := make(map[credKey]bool, len(creds))
	for _, c := range creds {
		k := credKey{issuer: c.Issuer, credentialType: string(c.CredentialType)}
		if seen[k] {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "permissioned domain credentials aren't unique",
			}
		}
		seen[k] = true
	}

	// Check that credentials are sorted by (Issuer, CredentialType) lexicographically.
	for i := 1; i < len(creds); i++ {
		cmp := bytes.Compare(creds[i-1].Issuer[:], creds[i].Issuer[:])
		if cmp > 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "permissioned domain credentials aren't sorted",
			}
		}
		if cmp == 0 {
			cmp = bytes.Compare(creds[i-1].CredentialType, creds[i].CredentialType)
			if cmp > 0 {
				return &InvariantViolation{
					Name:    "ValidPermissionedDomain",
					Message: "permissioned domain credentials aren't sorted",
				}
			}
			// cmp == 0 means duplicate, but that's already caught above
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// ValidPermissionedDEX
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPermissionedDEX (lines 1637-1718)
//
// visitEntry: For entries with "after" data:
//   - DirNode with DomainID: record the domain
//   - Offer with DomainID: record the domain; check hybrid offer structure
//   - Offer without DomainID: mark regularOffers
//
// finalize: Only for Payment/OfferCreate with tesSUCCESS:
//   - If tx has DomainID: verify domain exists, all touched domains match,
//     no regular offers affected
//   - Bad hybrids always fail for OfferCreate

// lsfHybridInvariant is the ledger flag for hybrid offers.
const lsfHybridInvariant = entry.LsfHybrid

func checkValidPermissionedDEX(tx Transaction, result Result, entries []InvariantEntry, view ReadView) *InvariantViolation {
	txType := tx.TxType()

	// Only check for Payment and OfferCreate with tesSUCCESS.
	// Reference: rippled lines 1674-1677
	if (txType != TypePayment && txType != TypeOfferCreate) || result != TesSUCCESS {
		return nil
	}

	var (
		regularOffers bool
		badHybrids    bool
		domains       = make(map[[32]byte]bool)
	)

	var zeroHash [32]byte

	for _, e := range entries {
		if e.After == nil {
			continue
		}

		afterType := state.EntryType(e.After)

		switch afterType {
		case "DirectoryNode":
			// Check if the DirNode has a DomainID field.
			// Reference: rippled lines 1643-1647
			if domainID, present := extractDomainIDFromBinary(e.After); present {
				domains[domainID] = true
			}

		case "Offer":
			offer, err := state.ParseLedgerOffer(e.After)
			if err != nil {
				return &InvariantViolation{
					Name:    "ValidPermissionedDEX",
					Message: fmt.Sprintf("could not parse Offer SLE: %v", err),
				}
			}

			if offer.DomainID != zeroHash {
				domains[offer.DomainID] = true
			} else {
				regularOffers = true
			}

			// A hybrid offer is malformed unless it carries both a present
			// DomainID and a present AdditionalBooks STArray of at most one
			// entry. Presence is keyed on the field being on the wire, not on
			// its value: a present all-zero DomainID and a present empty array
			// both satisfy presence (mirrors rippled isFieldPresent).
			if (offer.Flags & lsfHybridInvariant) != 0 {
				_, domainPresent := extractDomainIDFromBinary(e.After)
				abCount := countAdditionalBooksFromBinary(e.After)
				if !domainPresent || abCount < 0 || abCount > 1 {
					badHybrids = true
				}
			}
		}
	}

	// For OfferCreate, always check bad hybrids.
	// Reference: rippled lines 1681-1685
	if txType == TypeOfferCreate && badHybrids {
		return &InvariantViolation{
			Name:    "ValidPermissionedDEX",
			Message: "hybrid offer is malformed",
		}
	}

	// Check if the transaction has a DomainID.
	// Reference: rippled lines 1687-1688
	var txDomainID *[32]byte

	// Try the DomainIDProvider interface first
	if dp, ok := tx.(DomainIDProvider); ok {
		if did, hasDomain := dp.GetDomainID(); hasDomain {
			txDomainID = did
		}
	} else {
		// Fall back to TxHasField and Flatten
		if tx.TxHasField("DomainID") {
			flat, err := tx.Flatten()
			if err == nil {
				if domainStr, ok := flat["DomainID"].(string); ok {
					b, err := hex.DecodeString(domainStr)
					if err == nil && len(b) == 32 {
						var did [32]byte
						copy(did[:], b)
						txDomainID = &did
					}
				}
			}
		}
	}

	if txDomainID == nil {
		// Transaction doesn't have DomainID — no further checks needed.
		// Reference: rippled lines 1687-1688 — "return true" if no sfDomainID
		return nil
	}

	// Verify the domain exists in the view.
	// Reference: rippled lines 1690-1696
	if view != nil {
		pdKL := keylet.PermissionedDomainByID(*txDomainID)
		exists, err := view.Exists(pdKL)
		if err != nil || !exists {
			return &InvariantViolation{
				Name:    "ValidPermissionedDEX",
				Message: "domain doesn't exist",
			}
		}
	}

	// All domains touched by offers/dirs must match the tx's domain.
	// Reference: rippled lines 1700-1708
	for d := range domains {
		if d != *txDomainID {
			return &InvariantViolation{
				Name:    "ValidPermissionedDEX",
				Message: "transaction consumed wrong domains",
			}
		}
	}

	// No regular offers should be affected by domain transactions.
	// Reference: rippled lines 1710-1715
	if regularOffers {
		return &InvariantViolation{
			Name:    "ValidPermissionedDEX",
			Message: "domain transaction affected regular offers",
		}
	}

	return nil
}

// extractDomainIDFromBinary extracts the DomainID (Hash256, fieldCode=34) from
// binary SLE data. The bool reports whether the field is present, mirroring
// rippled's isFieldPresent(sfDomainID) so a present but all-zero DomainID is not
// collapsed into "absent".
func extractDomainIDFromBinary(data []byte) ([32]byte, bool) {
	var result [32]byte
	var present bool
	_ = state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == 5 && f.FieldCode == 34 { // Hash256 DomainID
			copy(result[:], f.Value)
			present = true
			return errStopWalk
		}
		return nil
	})
	return result, present
}

// countAdditionalBooksFromBinary counts the number of entries in the
// AdditionalBooks STArray (type=15, fieldCode=13) in binary SLE data.
// Returns -1 if the field is not present, or the count of objects inside.
func countAdditionalBooksFromBinary(data []byte) int {
	count := -1
	_ = state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == 15 && f.FieldCode == 13 { // AdditionalBooks STArray
			count = countArrayObjects(f.Value)
			return errStopWalk
		}
		return nil
	})
	return count
}

// countArrayObjects counts the inner objects of a serialized STArray value
// (the bytes between the array header and its 0xF1 end marker). Each inner
// object is delimited by its own 0xE1 marker.
func countArrayObjects(arrayValue []byte) int {
	count := 0
	for _, f := range topLevelFields(arrayValue) {
		if f.TypeCode == 14 { // STObject element
			count++
		}
	}
	return count
}

// topLevelFields walks a serialized STObject/STArray content slice and returns
// its top-level fields. Parse errors yield the fields decoded so far.
func topLevelFields(data []byte) []state.Field {
	var fields []state.Field
	_ = state.WalkFields(data, func(f state.Field) error {
		fields = append(fields, f)
		return nil
	})
	return fields
}

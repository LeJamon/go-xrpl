package invariants

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// ValidPermissionedDomain
// ---------------------------------------------------------------------------
//
// Reference: rippled InvariantCheck.cpp — ValidPermissionedDomain
//
// Pre-fixCleanup3_1_3: only PermissionedDomainSet with tesSUCCESS is checked;
// the single affected domain must have an AcceptedCredentials array that is
// non-empty, at most maxPermissionedDomainCredentials, unique, and sorted.
//
// Post-fixCleanup3_1_3: enforced for every transaction —
//   - a failed (non-tesSUCCESS) transaction must not touch any domain,
//   - at most one domain may be affected,
//   - PermissionedDomainSet must leave exactly one non-deleted valid domain,
//   - PermissionedDomainDelete must delete exactly one domain,
//   - any other transaction must touch no domain.

// pdSleStatus mirrors rippled's ValidPermissionedDomain::SleStatus: the parsed
// domain (from the entry's after-image) plus whether the entry was deleted.
type pdSleStatus struct {
	pd       *state.PermissionedDomainData
	isDelete bool
}

func checkValidPermissionedDomain(tx Transaction, result Result, entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	// Collect one status per touched PermissionedDomain entry. rippled's
	// visitEntry inspects the entry's after-image (for a delete, that is the
	// erased SLE), which maps to After when present, else Before.
	var statuses []pdSleStatus
	for _, e := range entries {
		afterImage := e.After
		if afterImage == nil {
			afterImage = e.Before
		}
		if afterImage == nil || state.EntryType(afterImage) != "PermissionedDomain" {
			continue
		}
		pd, err := state.ParsePermissionedDomain(afterImage)
		if err != nil {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: fmt.Sprintf("could not parse PermissionedDomain SLE: %v", err),
			}
		}
		statuses = append(statuses, pdSleStatus{pd: pd, isDelete: e.IsDelete})
	}

	if rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_1_3) {
		return checkValidPermissionedDomainNew(tx, result, statuses)
	}

	// Pre-fixCleanup3_1_3: only PermissionedDomainSet with tesSUCCESS.
	if tx.TxType() != TypePermissionedDomainSet || result != TesSUCCESS || len(statuses) == 0 {
		return nil
	}
	return validatePermissionedDomainCredentials(statuses[0].pd)
}

func checkValidPermissionedDomainNew(tx Transaction, result Result, statuses []pdSleStatus) *InvariantViolation {
	// A failed transaction must not touch any domain object.
	if result != TesSUCCESS {
		if len(statuses) != 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "failed transaction affected a permissioned domain",
			}
		}
		return nil
	}

	if len(statuses) > 1 {
		return &InvariantViolation{
			Name:    "ValidPermissionedDomain",
			Message: "transaction affected more than 1 permissioned domain entry",
		}
	}

	switch tx.TxType() {
	case TypePermissionedDomainSet:
		if len(statuses) == 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "no domain objects affected by PermissionedDomainSet",
			}
		}
		if statuses[0].isDelete {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "domain object deleted by PermissionedDomainSet",
			}
		}
		return validatePermissionedDomainCredentials(statuses[0].pd)

	case TypePermissionedDomainDelete:
		if len(statuses) == 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "no domain objects affected by PermissionedDomainDelete",
			}
		}
		if !statuses[0].isDelete {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "domain object modified, but not deleted by PermissionedDomainDelete",
			}
		}
		return nil

	default:
		if len(statuses) != 0 {
			return &InvariantViolation{
				Name:    "ValidPermissionedDomain",
				Message: "domain object(s) affected by an unauthorized transaction",
			}
		}
		return nil
	}
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

func checkValidPermissionedDEX(tx Transaction, result Result, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	// Post-fixCleanup3_1_3 a hybrid offer must carry exactly one AdditionalBooks
	// entry (size == 1); before the amendment, only a missing field or size > 1
	// was rejected (an empty array slipped through).
	hybridSizeStrict := rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_1_3)
	txType := tx.TxType()

	// Only check for Payment and OfferCreate with tesSUCCESS.
	// Reference: rippled lines 1674-1677
	if (txType != TypePayment && txType != TypeOfferCreate) || result != TesSUCCESS {
		return nil
	}

	var (
		regularOffers    bool // post-fixCleanup3_2_0: only non-deleted regular offers
		regularOffersOld bool // pre-fixCleanup3_2_0: any touched regular offer
		badHybrids       bool
		domains          = make(map[[32]byte]bool)
	)

	var zeroHash [32]byte

	for _, e := range entries {
		// rippled visitEntry inspects the entry's final state (its "after"); for a
		// delete that is the erased SLE, which goXRPL carries as Before.
		image := e.After
		if image == nil {
			image = e.Before
		}
		if image == nil {
			continue
		}

		switch state.EntryType(image) {
		case "DirectoryNode":
			// Check if the DirNode has a DomainID field.
			// Reference: rippled lines 1643-1647
			if domainID, present := extractDomainIDFromBinary(image); present {
				domains[domainID] = true
			}

		case "Offer":
			offer, err := state.ParseLedgerOffer(image)
			if err != nil {
				return &InvariantViolation{
					Name:    "ValidPermissionedDEX",
					Message: fmt.Sprintf("could not parse Offer SLE: %v", err),
				}
			}

			if offer.DomainID != zeroHash {
				domains[offer.DomainID] = true
			} else {
				// A deleted regular offer counts only for the pre-fixCleanup3_2_0
				// set: the amendment stops the invariant firing on a domain
				// transaction that legitimately deletes a regular offer.
				regularOffersOld = true
				if !e.IsDelete {
					regularOffers = true
				}
			}

			// A hybrid offer is malformed unless it carries both a present
			// DomainID and a present AdditionalBooks STArray. Presence is keyed
			// on the field being on the wire, not on its value: a present
			// all-zero DomainID and a present empty array both satisfy presence
			// (mirrors rippled isFieldPresent). Post-fixCleanup3_1_3 the array
			// must hold exactly one entry (size != 1 fails); before it, only a
			// missing field or size > 1 failed.
			if (offer.Flags & lsfHybridInvariant) != 0 {
				_, domainPresent := extractDomainIDFromBinary(image)
				abCount := countAdditionalBooksFromBinary(image)
				var abBad bool
				if hybridSizeStrict {
					abBad = abCount != 1
				} else {
					abBad = abCount < 0 || abCount > 1
				}
				if !domainPresent || abBad {
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

	// No regular offers should be affected by domain transactions. Post
	// fixCleanup3_2_0 a legitimately deleted regular offer no longer counts.
	// Reference: rippled lines 1710-1715 (#7118).
	hasRegularOffers := regularOffersOld
	if rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_2_0) {
		hasRegularOffers = regularOffers
	}
	if hasRegularOffers {
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

package invariants

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
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
			beforeType := getLedgerEntryType(e.Before)
			if beforeType != "PermissionedDomain" {
				continue
			}
		}
		afterType := getLedgerEntryType(e.After)
		if afterType != "PermissionedDomain" {
			continue
		}

		// Parse the PermissionedDomain from the "after" data.
		pd, err := state.ParsePermissionedDomain(e.After)
		if err != nil {
			continue
		}

		// Validate AcceptedCredentials.
		if v := validatePermissionedDomainCredentials(pd, e.Before != nil); v != nil {
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
// isModified indicates whether this is a modification (before != nil) — both
// before and after states are checked against the same criteria in rippled.
func validatePermissionedDomainCredentials(pd *state.PermissionedDomainData, _ bool) *InvariantViolation {
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
const lsfHybridInvariant uint32 = 0x00040000

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

		afterType := getLedgerEntryType(e.After)

		switch afterType {
		case "DirectoryNode":
			// Check if the DirNode has a DomainID field.
			// Reference: rippled lines 1643-1647
			domainID := extractDomainIDFromBinary(e.After)
			if domainID != zeroHash {
				domains[domainID] = true
			}

		case "Offer":
			offer, err := state.ParseLedgerOfferFromBytes(e.After)
			if err != nil {
				continue
			}

			if offer.DomainID != zeroHash {
				domains[offer.DomainID] = true
			} else {
				regularOffers = true
			}

			// Check hybrid offer structure.
			// Reference: rippled lines 1658-1663
			// rippled checks: lsfHybrid requires DomainID present AND
			// sfAdditionalBooks present with at most 1 entry.
			// In the Go codebase, AdditionalBooks is not serialized as an
			// STArray in binary but stored as separate struct fields
			// (AdditionalBookDirectory, AdditionalBookNode). We check:
			//   1. DomainID must be present for hybrid offers
			//   2. AdditionalBooks (if encoded as STArray) must have <= 1 entry
			if (offer.Flags & lsfHybridInvariant) != 0 {
				if offer.DomainID == zeroHash {
					badHybrids = true
				}
				// Check AdditionalBooks if present in binary
				abCount := countAdditionalBooksFromBinary(e.After)
				if abCount > 1 {
					badHybrids = true
				}
				// Note: abCount == -1 means AdditionalBooks not in binary,
				// which is valid in Go since it stores the data differently.
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
// binary SLE data. Returns a zero [32]byte if not found.
func extractDomainIDFromBinary(data []byte) [32]byte {
	var result [32]byte
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = int(data[offset])
			offset++
		}

		switch typeCode {
		case 5: // Hash256
			if offset+32 > len(data) {
				return result
			}
			if fieldCode == 34 { // DomainID
				copy(result[:], data[offset:offset+32])
				return result
			}
			offset += 32
		default:
			if typeCode == 14 || typeCode == 15 {
				// STObject/STArray structural markers — no payload
				continue
			}
			skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
			if !ok {
				return result
			}
			offset += skip
		}
	}
	return result
}

// countAdditionalBooksFromBinary counts the number of entries in the
// AdditionalBooks STArray (type=15, fieldCode=13) in binary SLE data.
// Returns -1 if the field is not present, or the count of objects inside.
func countAdditionalBooksFromBinary(data []byte) int {
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}
		header := data[offset]
		offset++

		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = int(data[offset])
			offset++
		}

		if typeCode == 15 && fieldCode == 13 {
			// Found AdditionalBooks array start.
			// Count objects inside until we hit the array end marker (0xF1).
			count := 0
			for offset < len(data) {
				if data[offset] == 0xF1 {
					// End of array
					return count
				}
				if data[offset] == 0xE1 {
					// End of object — count the completed object
					count++
					offset++
					continue
				}
				// Parse and skip inner field
				innerHeader := data[offset]
				offset++
				innerType := int((innerHeader >> 4) & 0x0F)
				innerField := int(innerHeader & 0x0F)

				if innerType == 0 {
					if offset >= len(data) {
						return count
					}
					innerType = int(data[offset])
					offset++
				}
				if innerField == 0 {
					if offset >= len(data) {
						return count
					}
					innerField = int(data[offset])
					offset++
				}

				if innerType == 14 || innerType == 15 {
					// Object/array structural marker — no payload
					continue
				}

				skip, ok := skipFieldBytes(innerType, innerField, data, offset)
				if !ok {
					return count
				}
				offset += skip
			}
			return count
		}

		// Skip this field
		if typeCode == 14 || typeCode == 15 {
			// Structural markers — no payload
			continue
		}

		skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
		if !ok {
			return -1
		}
		offset += skip
	}
	return -1 // Not found
}

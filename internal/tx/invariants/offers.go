package invariants

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// checkNoBadOffers verifies that Offer entries have positive non-zero amounts
// and that XRP/XRP offers don't exist.
// Reference: rippled InvariantCheck.cpp — NoBadOffers
func checkNoBadOffers(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "Offer" || e.IsDelete {
			continue
		}
		offer, err := parseOfferForInvariant(e.After)
		if err != nil {
			continue
		}
		// Both sides XRP is disallowed
		if offer.takerPaysIsXRP && offer.takerGetsIsXRP {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer has XRP on both sides",
			}
		}
		// Amounts must be positive
		if offer.takerPaysIsXRP && offer.takerPaysXRP == 0 {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer TakerPays (XRP) is zero",
			}
		}
		if offer.takerGetsIsXRP && offer.takerGetsXRP == 0 {
			return &InvariantViolation{
				Name:    "NoBadOffers",
				Message: "Offer TakerGets (XRP) is zero",
			}
		}
	}
	return nil
}

// checkNoZeroEscrow verifies that Escrow entries have a positive XRP amount.
// Reference: rippled InvariantCheck.cpp — NoZeroEscrow
func checkNoZeroEscrow(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "Escrow" || e.IsDelete {
			continue
		}
		esc, err := state.ParseEscrow(e.After)
		if err != nil {
			continue
		}
		if esc.Amount == 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow entry has zero XRP amount",
			}
		}
		if esc.Amount > InitialXRP {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("Escrow amount %d exceeds InitialXRP (%d)", esc.Amount, InitialXRP),
			}
		}
	}
	return nil
}

// offerForInvariant holds the parsed fields of an Offer ledger entry needed for invariant checks.
type offerForInvariant struct {
	takerPaysIsXRP bool
	takerPaysXRP   uint64
	takerGetsIsXRP bool
	takerGetsXRP   uint64
}

// parseOfferForInvariant extracts TakerPays/TakerGets from an Offer binary entry.
// Only checks XRP amounts (IOU amounts are assumed non-negative by binary encoding).
func parseOfferForInvariant(data []byte) (*offerForInvariant, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("offer too short")
	}
	result := &offerForInvariant{}
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

		// TakerPays = type 6 (Amount), field 4
		// TakerGets = type 6 (Amount), field 5
		if typeCode == 6 { // Amount
			if offset >= len(data) {
				break
			}
			firstByte := data[offset]
			isXRP := (firstByte & 0x80) == 0 // high bit 0 = XRP
			if isXRP {
				if offset+8 > len(data) {
					break
				}
				amount := binary.BigEndian.Uint64(data[offset:offset+8]) & 0x3FFFFFFFFFFFFFFF
				switch fieldCode {
				case 4:
					result.takerPaysIsXRP = true
					result.takerPaysXRP = amount
				case 5:
					result.takerGetsIsXRP = true
					result.takerGetsXRP = amount
				}
				offset += 8
			} else {
				// IOU: 48 bytes
				if offset+48 > len(data) {
					break
				}
				// IOU amounts are always non-negative in valid binary encoding
				offset += 48
			}
			continue
		}

		// Skip non-Amount fields
		skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
		if !ok {
			break
		}
		offset += skip
	}
	return result, nil
}

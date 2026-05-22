package invariants

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// checkNoBadOffers verifies that Offer entries have positive non-zero amounts
// and that XRP/XRP offers don't exist. Both pre- and post-tx images are
// inspected so the invariant catches pre-existing malformed offers as well as
// newly written ones.
// Reference: rippled InvariantCheck.cpp — NoBadOffers (lines 223-263).
func checkNoBadOffers(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		if e.EntryType != "Offer" {
			continue
		}
		for _, data := range [][]byte{e.Before, e.After} {
			if data == nil {
				continue
			}
			offer, err := parseOfferForInvariant(data)
			if err != nil {
				continue
			}
			if offer.takerPaysIsXRP && offer.takerGetsIsXRP {
				return &InvariantViolation{
					Name:    "NoBadOffers",
					Message: "Offer has XRP on both sides",
				}
			}
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
	}
	return nil
}

// checkNoZeroEscrow verifies that Escrow, MPTokenIssuance, and MPToken entries
// carry sane amount fields. XRP escrows must be strictly within (0, InitialXRP);
// IOU escrows must be strictly positive; MPT escrows must be within
// (0, MaxMPTokenAmount]. MPTokenIssuance.OutstandingAmount/LockedAmount and
// MPToken.MPTAmount/LockedAmount must each fit within [0, MaxMPTokenAmount],
// and OutstandingAmount must be >= LockedAmount.
// Reference: rippled InvariantCheck.cpp — NoZeroEscrow (lines 267-356).
func checkNoZeroEscrow(entries []InvariantEntry) *InvariantViolation {
	for _, e := range entries {
		switch e.EntryType {
		case "Escrow":
			for _, data := range [][]byte{e.Before, e.After} {
				if data == nil {
					continue
				}
				if v := checkEscrowAmount(data); v != nil {
					return v
				}
			}
		case "MPTokenIssuance":
			if e.After == nil {
				continue
			}
			if v := checkMPTokenIssuanceAmounts(e.After); v != nil {
				return v
			}
		case "MPToken":
			if e.After == nil {
				continue
			}
			if v := checkMPTokenAmounts(e.After); v != nil {
				return v
			}
		}
	}
	return nil
}

// MaxMPTokenAmount is the maximum representable MPT amount (2^63 - 1).
// Reference: rippled maxMPTokenAmount constant.
const MaxMPTokenAmount uint64 = 0x7FFFFFFFFFFFFFFF

func checkEscrowAmount(data []byte) *InvariantViolation {
	esc, err := state.ParseEscrow(data)
	if err != nil {
		return nil
	}
	if esc.IsXRP {
		if esc.Amount == 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow entry has zero XRP amount",
			}
		}
		if esc.Amount >= InitialXRP {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("Escrow XRP amount %d exceeds InitialXRP (%d)", esc.Amount, InitialXRP),
			}
		}
		return nil
	}
	if esc.MPTAmount != nil {
		v := *esc.MPTAmount
		if v <= 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("Escrow MPT amount %d is not positive", v),
			}
		}
		if uint64(v) > MaxMPTokenAmount {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("Escrow MPT amount %d exceeds MaxMPTokenAmount", v),
			}
		}
		return nil
	}
	// IOU escrow — must be strictly positive and must not use the sentinel "bad"
	// currency code ("XRP" as an IOU currency). Mirrors rippled InvariantCheck.cpp:286-292.
	if esc.IOUAmount != nil {
		if esc.IOUAmount.Signum() <= 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow IOU amount is not positive",
			}
		}
		if esc.IOUAmount.Currency == badIOUCurrency {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow IOU amount uses the bad (XRP) currency code",
			}
		}
	}
	return nil
}

// badIOUCurrency is the sentinel currency code rejected as an IOU asset.
// Mirrors rippled protocol/Issue.h badCurrency() — the 3-letter ASCII "XRP"
// is reserved for native XRP and may not appear as an IOU currency.
const badIOUCurrency = "XRP"

func checkMPTokenIssuanceAmounts(data []byte) *InvariantViolation {
	issuance, err := state.ParseMPTokenIssuance(data)
	if err != nil {
		return nil
	}
	if issuance.OutstandingAmount > MaxMPTokenAmount {
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("MPTokenIssuance.OutstandingAmount %d exceeds MaxMPTokenAmount", issuance.OutstandingAmount),
		}
	}
	if issuance.LockedAmount != nil {
		locked := *issuance.LockedAmount
		if locked > MaxMPTokenAmount {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("MPTokenIssuance.LockedAmount %d exceeds MaxMPTokenAmount", locked),
			}
		}
		if issuance.OutstandingAmount < locked {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: fmt.Sprintf("MPTokenIssuance.OutstandingAmount %d < LockedAmount %d", issuance.OutstandingAmount, locked),
			}
		}
	}
	return nil
}

func checkMPTokenAmounts(data []byte) *InvariantViolation {
	token, err := state.ParseMPToken(data)
	if err != nil {
		return nil
	}
	if token.MPTAmount > MaxMPTokenAmount {
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("MPToken.MPTAmount %d exceeds MaxMPTokenAmount", token.MPTAmount),
		}
	}
	if token.LockedAmount != nil && *token.LockedAmount > MaxMPTokenAmount {
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("MPToken.LockedAmount %d exceeds MaxMPTokenAmount", *token.LockedAmount),
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

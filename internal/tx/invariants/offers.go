package invariants

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// checkNoBadOffers verifies that an Offer never carries a negative TakerPays or
// TakerGets and that no XRP-for-XRP offer exists. Zero amounts are acceptable.
// Both pre- and post-tx images are inspected so the invariant catches
// pre-existing malformed offers as well as newly written ones.
// Reference: rippled InvariantCheck.cpp — NoBadOffers (lines 228-245).
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
				return &InvariantViolation{
					Name:    "NoBadOffers",
					Message: fmt.Sprintf("could not parse Offer SLE: %v", err),
				}
			}
			// isBad: pays < 0 || gets < 0 || (pays.native() && gets.native()).
			if offer.takerPaysNegative || offer.takerGetsNegative {
				return &InvariantViolation{
					Name:    "NoBadOffers",
					Message: "Offer has a negative amount",
				}
			}
			if offer.takerPaysIsXRP && offer.takerGetsIsXRP {
				return &InvariantViolation{
					Name:    "NoBadOffers",
					Message: "Offer has XRP on both sides",
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
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("could not parse Escrow SLE: %v", err),
		}
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
	// IOU escrow — must be strictly positive and must not use badCurrency
	// (rippled InvariantCheck.cpp:286-292). A zero-currency IOU is an illegal
	// encoding rippled rejects at deserialization (STAmount.cpp:183-184), so it
	// is flagged too.
	if esc.IOUAmount != nil {
		if esc.IOUAmount.Signum() <= 0 {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow IOU amount is not positive",
			}
		}
		if isBadCurrency(esc.IOUAmount.Currency) || isNativeXRPCurrency(esc.IOUAmount.Currency) {
			return &InvariantViolation{
				Name:    "NoZeroEscrow",
				Message: "Escrow IOU amount uses the bad (XRP) currency code",
			}
		}
	}
	return nil
}

func checkMPTokenIssuanceAmounts(data []byte) *InvariantViolation {
	issuance, err := state.ParseMPTokenIssuance(data)
	if err != nil {
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("could not parse MPTokenIssuance SLE: %v", err),
		}
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
		return &InvariantViolation{
			Name:    "NoZeroEscrow",
			Message: fmt.Sprintf("could not parse MPToken SLE: %v", err),
		}
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

// offerForInvariant preserves the sign of both XRP and IOU legs so NoBadOffers
// can flag a negative amount, mirroring rippled's STAmount comparison.
type offerForInvariant struct {
	takerPaysIsXRP    bool
	takerPaysNegative bool
	takerGetsIsXRP    bool
	takerGetsNegative bool
}

func parseOfferForInvariant(data []byte) (*offerForInvariant, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("offer too short")
	}
	result := &offerForInvariant{}
	walkErr := state.WalkFields(data, func(f state.Field) error {
		// TakerPays = Amount (type 6) field 4, TakerGets = field 5.
		if f.TypeCode != 6 {
			return nil
		}
		if len(f.Value) == 0 {
			return fmt.Errorf("offer truncated at Amount value")
		}
		// Bit 63 is the not-XRP flag (clear for XRP) and bit 62 is the sign
		// (set for positive). For IOU the not-XRP bit is set and the sign is
		// decoded from the 48-byte value.
		isXRP := (f.Value[0] & 0x80) == 0
		if isXRP {
			if len(f.Value) < 8 {
				return fmt.Errorf("offer truncated at XRP amount")
			}
			raw := binary.BigEndian.Uint64(f.Value[:8])
			magnitude := raw & 0x3FFFFFFFFFFFFFFF
			negative := raw&0x4000000000000000 == 0 && magnitude != 0
			switch f.FieldCode {
			case 4:
				result.takerPaysIsXRP = true
				result.takerPaysNegative = negative
			case 5:
				result.takerGetsIsXRP = true
				result.takerGetsNegative = negative
			}
			return nil
		}
		if len(f.Value) < 48 {
			return fmt.Errorf("offer truncated at IOU amount")
		}
		amt, err := state.ParseIOUAmountBinary(f.Value[:48])
		if err != nil {
			return fmt.Errorf("parse IOU amount: %w", err)
		}
		negative := amt.Signum() < 0
		switch f.FieldCode {
		case 4:
			result.takerPaysNegative = negative
		case 5:
			result.takerGetsNegative = negative
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return result, nil
}

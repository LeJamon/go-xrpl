package invariants

import (
	"encoding/binary"
	"math/big"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/protocol"
)

const (
	// maxNativeN is rippled STAmount::kMaxNativeN — the isLegalNet ceiling on a
	// native XRP amount's magnitude (100 billion XRP in drops).
	maxNativeN uint64 = 100_000_000_000_000_000
	// maxMPTAmount is rippled kMaxMpTokenAmount — the isLegalMPT ceiling on an
	// MPT amount's magnitude (2^63 - 1).
	maxMPTAmount uint64 = protocol.MaxMPTokenAmount
)

var (
	maxNativeBig = new(big.Int).SetUint64(maxNativeN)
	maxMPTBig    = new(big.Int).SetUint64(maxMPTAmount)
)

// HasInvalidAmount checks the flattened transaction before JSON rendering can
// discard a zero MPT's wire sign. Nested structures deeper than 10 are invalid.
func HasInvalidAmount(fields map[string]any) bool {
	return hasInvalidAmountMap(fields, 0)
}

func hasInvalidAmountMap(m map[string]any, depth int) bool {
	if depth > 10 {
		return true
	}
	defs := definitions.Get()
	for name, v := range m {
		fi, err := defs.FieldInstanceByName(name)
		if err != nil || fi == nil {
			continue
		}
		switch fi.Type {
		case "Amount":
			if amountJSONInvalid(v) {
				return true
			}
		case "STObject":
			if child, ok := v.(map[string]any); ok && hasInvalidAmountMap(child, depth+1) {
				return true
			}
		case "STArray":
			switch arr := v.(type) {
			case []map[string]any:
				for _, child := range arr {
					if hasInvalidAmountMap(child, depth+1) {
						return true
					}
				}
			case []any:
				for _, el := range arr {
					if child, ok := el.(map[string]any); ok && hasInvalidAmountMap(child, depth+1) {
						return true
					}
				}
			}
		}
	}
	return false
}

// amountJSONInvalid validates a single amount in canonical JSON form: a decimal
// drops string (native XRP), a {value, mpt_issuance_id} object (MPT), or a
// {value, currency, issuer} object (issued currency, always canonical).
func amountJSONInvalid(v any) bool {
	switch a := v.(type) {
	case binarytypes.RawXRPAmount:
		return a.Drops > maxNativeN
	case string:
		mag := strings.TrimPrefix(a, "-")
		n, ok := new(big.Int).SetString(mag, 10)
		if !ok {
			return false
		}
		return n.Cmp(maxNativeBig) > 0
	case map[string]any:
		if _, isMPT := a["mpt_issuance_id"]; !isMPT {
			return false // issued currency
		}
		if raw, ok := a["value"].(binarytypes.RawMPTValue); ok {
			return raw.Negative || raw.Magnitude > maxMPTAmount
		}
		val, _ := a["value"].(string)
		if strings.HasPrefix(val, "-") {
			return true // MPT must be non-negative
		}
		n, ok := new(big.Int).SetString(val, 10)
		if !ok {
			return false
		}
		return n.Cmp(maxMPTBig) > 0
	}
	return false
}

// hasInvalidAmountBinary reports whether a serialized ledger entry contains a
// non-canonical native or MPT amount, reading the raw amount bytes so a value
// that binarycodec.Decode would reject is still inspected. A serialized entry
// carrying a composite field the walker cannot delimit (Issue/PathSet/
// XChainBridge) yields a walk error, which is treated as "no invalid amount":
// the invariant never manufactures a failure it cannot substantiate.
func hasInvalidAmountBinary(data []byte) bool {
	found := false
	_ = state.WalkFieldsDeep(data, func(f state.Field) error {
		if f.TypeCode == 6 && amountBytesInvalid(f.Value) { // STI_AMOUNT
			found = true
			return errStopWalk
		}
		return nil
	})
	return found
}

// amountBytesInvalid validates a raw serialized STAmount value. The leading byte
// selects the shape: bit 0x80 → issued currency (always canonical), bit 0x20 →
// MPT (1 header byte + 8-byte magnitude + issuance ID), otherwise native XRP.
func amountBytesInvalid(v []byte) bool {
	if len(v) == 0 {
		return false
	}
	b := v[0]
	switch {
	case b&0x80 != 0:
		return false // issued currency
	case b&0x20 != 0:
		if len(v) < 9 {
			return false
		}
		positive := b&0x40 != 0
		mant := binary.BigEndian.Uint64(v[1:9])
		return !positive || mant > maxMPTAmount
	default:
		if len(v) < 8 {
			return false
		}
		drops := binary.BigEndian.Uint64(v[:8]) & 0x3FFF_FFFF_FFFF_FFFF
		return drops > maxNativeN
	}
}

// checkValidAmounts is the ValidAmounts invariant: after fixCleanup3_2_0 no
// ledger entry left by the transaction may contain a non-canonical MPT or XRP
// amount. Before the amendment the condition is only logged, never fatal, so the
// check is a no-op. Reference: rippled InvariantCheck.cpp ValidAmounts::finalize.
func checkValidAmounts(entries []InvariantEntry, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureFixCleanup3_2_0) {
		return nil
	}
	for _, e := range entries {
		if e.IsDelete || e.After == nil {
			continue
		}
		if hasInvalidAmountBinary(e.After) {
			return &InvariantViolation{
				Name:    "ValidAmounts",
				Message: "ledger entry contains non-canonical MPT or XRP amount",
			}
		}
	}
	return nil
}

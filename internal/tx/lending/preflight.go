package lending

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
)

// maxDataPayloadLength bounds the sfData blob on lending objects (rippled
// Protocol.h maxDataPayloadLength).
const maxDataPayloadLength = 256

// maxMPTokenAmount is the DebtMaximum upper bound (2^63-1).
const maxMPTokenAmount int64 = 0x7FFFFFFFFFFFFFFF

// LoanSet schedule bounds (rippled Protocol.h): the minimum payment interval and
// grace period are both 60 seconds.
const (
	minPaymentInterval uint32 = 60
	defaultGracePeriod uint32 = 60
)

// requiredLending is the amendment dependency chain for every LendingProtocol
// transaction: LendingProtocol, plus SingleAssetVault and MPTokensV1 which the
// lending objects are built on (rippled checkLendingProtocolDependencies).
func requiredLending() [][32]byte {
	return [][32]byte{
		amendment.FeatureLendingProtocol,
		amendment.FeatureSingleAssetVault,
		amendment.FeatureMPTokensV1,
	}
}

// validDataLength reports whether a hex Blob decodes to at most max bytes. An
// empty string is treated as absent (valid).
func validDataLength(data string, max int) bool {
	if data == "" {
		return true
	}
	b, err := hex.DecodeString(data)
	if err != nil {
		return false
	}
	return len(b) <= max
}

// validRangeU32 reports whether an optional uint32 is within [0, max].
func validRangeU32(v *uint32, max uint32) bool {
	return v == nil || *v <= max
}

// validRangeU16 reports whether an optional uint16 is within [0, max].
func validRangeU16(v *uint16, max uint16) bool {
	return v == nil || *v <= max
}

// validNumberRange reports whether an optional NUMBER string is within
// [min, max]. Absent is valid.
func validNumberRange(v *string, min, max int64) bool {
	if v == nil {
		return true
	}
	n := lendNum(*v)
	if n.Cmp(lmath.FromInt(min)) < 0 {
		return false
	}
	return n.Cmp(lmath.FromInt(max)) <= 0
}

// validNumberMinimum reports whether an optional NUMBER string is >= min.
func validNumberMinimum(v *string, min int64) bool {
	if v == nil {
		return true
	}
	return lendNum(*v).Cmp(lmath.FromInt(min)) >= 0
}

// isZeroAccount reports whether a base58 account address is the all-zero AccountID.
func isZeroAccount(addr string) bool {
	if addr == "" {
		return true
	}
	id, err := state.DecodeAccountID(addr)
	return err == nil && id == [20]byte{}
}

// isZeroHashStr reports whether a 64-char hex hash is all zero.
func isZeroHashStr(s string) bool {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// hashBytes decodes a 64-char hex hash into a [32]byte, reporting validity.
func hashBytes(s string) ([32]byte, bool) {
	var h [32]byte
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != 32 {
		return h, false
	}
	copy(h[:], b)
	return h, true
}

// checkUniversalFlags rejects any non-universal transaction flag (temINVALID_FLAG).
func checkUniversalFlags(t interface{ GetFlags() uint32 }) error {
	return tx.CheckFlags(t.GetFlags(), tx.TfUniversalMask)
}

package vault

import (
	"encoding/binary"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// This file is the local Number seam for the vault package. The vault ledger
// NUMBER fields are carried as their canonical decimal/scientific string; these
// helpers bridge that representation to state.XRPLNumber for the exact rippled
// share-conversion arithmetic. A shared state.Number helper (built in a sibling
// PR) can later replace the string carriage without touching call sites.

// vaultNumber parses a NUMBER field string ("" meaning zero) into an XRPLNumber.
// It round-trips through the binary codec so the parse is byte-identical to how
// the field decodes off the ledger.
func vaultNumber(s string) (state.XRPLNumber, error) {
	if s == "" || s == "0" {
		return state.NewXRPLNumber(0, 0), nil
	}
	num := &types.Number{}
	b, err := num.FromJSON(s)
	if err != nil {
		return state.NewXRPLNumber(0, 0), fmt.Errorf("parse number %q: %w", s, err)
	}
	mantissa := int64(binary.BigEndian.Uint64(b[:8]))
	exp := int32(binary.BigEndian.Uint32(b[8:12]))
	return state.NewXRPLNumber(mantissa, int(exp)), nil
}

// numberToString renders an XRPLNumber into the vault NUMBER-field convention:
// "" for zero, otherwise a scientific string the codec re-normalizes to the
// identical value.
func numberToString(n state.XRPLNumber) string {
	if n.IsZero() {
		return ""
	}
	return fmt.Sprintf("%de%d", n.Mantissa(), n.Exponent())
}

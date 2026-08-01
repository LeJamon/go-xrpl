package invariants

import (
	"encoding/binary"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// ---------------------------------------------------------------------------
// ValidBookDirectory
// ---------------------------------------------------------------------------
//
// Reference: rippled DirectoryInvariant.cpp — ValidBookDirectory
//
// A newly-created book-directory root must carry an sfExchangeRate that matches
// the quality encoded in the low 64 bits of its own key. A newly-created child
// page, or a page whose sfRootIndex changes, must point to a root that exists
// after the transaction applies. Ordinary modifications that do not change
// sfRootIndex are tolerated so legacy bad exchange-rate metadata survives normal
// operation (LedgerStateFix::BookExchangeRate repairs it). Gated on
// fixCleanup3_2_0: before the amendment the invariant never fails.

// Field identifiers (typeCode, fieldCode) from sfields.macro.
const (
	fcExchangeRate      = 6  // UInt64
	fcRootIndex         = 8  // Hash256
	fcDomainID          = 34 // Hash256
	fcTakerPaysCurrency = 1  // Hash160
	fcTakerPaysIssuer   = 2  // Hash160
	fcTakerGetsCurrency = 3  // Hash160
	fcTakerGetsIssuer   = 4  // Hash160

	tcUInt64  = 3
	tcHash256 = 5
	tcHash160 = 17
)

// bookDirRootPresence lists the fields whose presence marks a DirectoryNode as a
// book-directory root rather than an owner directory. The MPT book fields
// (TakerPaysMPT/TakerGetsMPT) do not exist in this build's protocol and so are
// omitted.
var bookDirRootPresence = []struct {
	typeCode  int
	fieldCode int
}{
	{tcUInt64, fcExchangeRate},
	{tcHash160, fcTakerPaysCurrency},
	{tcHash160, fcTakerPaysIssuer},
	{tcHash160, fcTakerGetsCurrency},
	{tcHash160, fcTakerGetsIssuer},
	{tcHash256, fcDomainID},
}

func checkValidBookDirectory(entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if rules == nil || !rules.Enabled(amendment.FeatureFixCleanup3_2_0) {
		return nil
	}

	rootIndexes := make(map[[32]byte]bool)

	for _, e := range entries {
		afterType, err := state.DecodeType(e.After)
		if e.IsDelete || e.After == nil || err != nil || afterType != entry.TypeDirectoryNode {
			continue
		}

		rootIndex, ok := dirRootIndex(e.After)
		if !ok {
			continue
		}

		// Ignore ordinary modifications that do not change which root this page
		// belongs to; only creations and sfRootIndex changes are validated.
		if e.Before != nil {
			if beforeRoot, ok := dirRootIndex(e.Before); ok && beforeRoot == rootIndex {
				continue
			}
		}

		if e.Key == rootIndex {
			if badExchangeRate(e.After, e.Key) {
				return &InvariantViolation{
					Name:    "ValidBookDirectory",
					Message: "book directory exchange rate does not match directory quality",
				}
			}
			continue
		}

		rootIndexes[rootIndex] = true
	}

	for rootIndex := range rootIndexes {
		kl := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: rootIndex}
		exists, err := view.Exists(kl)
		if err != nil || !exists {
			return &InvariantViolation{
				Name:    "ValidBookDirectory",
				Message: "book directory root missing",
			}
		}
	}

	return nil
}

// dirRootIndex reads the sfRootIndex of a serialized DirectoryNode.
func dirRootIndex(data []byte) ([32]byte, bool) {
	var root [32]byte
	var found bool
	_ = state.WalkFields(data, func(f state.Field) error {
		if f.TypeCode == tcHash256 && f.FieldCode == fcRootIndex && len(f.Value) == 32 {
			copy(root[:], f.Value)
			found = true
			return errStopWalk
		}
		return nil
	})
	return root, found
}

// badExchangeRate reports whether a serialized book-directory root carries an
// sfExchangeRate that is absent or disagrees with the quality in the low 64 bits
// of its key. Owner directories (no book fields present) are never bad.
func badExchangeRate(data []byte, key [32]byte) bool {
	var isRoot bool
	var exchangeRate uint64
	var hasExchangeRate bool
	_ = state.WalkFields(data, func(f state.Field) error {
		for _, p := range bookDirRootPresence {
			if f.TypeCode == p.typeCode && f.FieldCode == p.fieldCode {
				isRoot = true
			}
		}
		if f.TypeCode == tcUInt64 && f.FieldCode == fcExchangeRate && len(f.Value) == 8 {
			exchangeRate = binary.BigEndian.Uint64(f.Value)
			hasExchangeRate = true
		}
		return nil
	})
	if !isRoot {
		return false
	}
	return !hasExchangeRate || exchangeRate != binary.BigEndian.Uint64(key[24:])
}

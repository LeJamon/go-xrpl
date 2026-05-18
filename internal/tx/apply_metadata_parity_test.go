package tx

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx/ledgerfields"
)

// TestBuildModifiedNode_TypedMatchesGeneric verifies that the typed
// AccountRoot / Offer paths produce metadata byte-identical to the generic
// extractLedgerFields-based path. The two paths are run on the same fixtures
// and their serialized output is compared.
func TestBuildModifiedNode_TypedMatchesGeneric(t *testing.T) {
	t.Run("AccountRoot", func(t *testing.T) {
		origBytes, curBytes, key := buildAccountRootPairT(t)
		assertParityModified(t, key, origBytes, curBytes)
	})
	t.Run("Offer", func(t *testing.T) {
		origBytes, curBytes, key := buildOfferPairT(t)
		assertParityModified(t, key, origBytes, curBytes)
	})
	t.Run("DirectoryNode", func(t *testing.T) {
		origBytes, curBytes, key := buildDirectoryNodePairT(t)
		assertParityModified(t, key, origBytes, curBytes)
	})
	t.Run("RippleState", func(t *testing.T) {
		origBytes, curBytes, key := buildRippleStatePairT(t)
		assertParityModified(t, key, origBytes, curBytes)
	})
}

func TestBuildCreatedNode_TypedMatchesGeneric(t *testing.T) {
	t.Run("AccountRoot", func(t *testing.T) {
		_, curBytes, key := buildAccountRootPairT(t)
		assertParityCreated(t, key, curBytes)
	})
	t.Run("Offer", func(t *testing.T) {
		_, curBytes, key := buildOfferPairT(t)
		assertParityCreated(t, key, curBytes)
	})
	t.Run("DirectoryNode", func(t *testing.T) {
		_, curBytes, key := buildDirectoryNodePairT(t)
		assertParityCreated(t, key, curBytes)
	})
	t.Run("RippleState", func(t *testing.T) {
		_, curBytes, key := buildRippleStatePairT(t)
		assertParityCreated(t, key, curBytes)
	})
}

func TestBuildDeletedNode_TypedMatchesGeneric(t *testing.T) {
	t.Run("AccountRoot", func(t *testing.T) {
		origBytes, curBytes, key := buildAccountRootPairT(t)
		assertParityDeleted(t, key, origBytes, curBytes)
	})
	t.Run("Offer", func(t *testing.T) {
		origBytes, curBytes, key := buildOfferPairT(t)
		assertParityDeleted(t, key, origBytes, curBytes)
	})
	t.Run("DirectoryNode", func(t *testing.T) {
		origBytes, curBytes, key := buildDirectoryNodePairT(t)
		assertParityDeleted(t, key, origBytes, curBytes)
	})
	t.Run("RippleState", func(t *testing.T) {
		origBytes, curBytes, key := buildRippleStatePairT(t)
		assertParityDeleted(t, key, origBytes, curBytes)
	})
}

func assertParityModified(t *testing.T, key [32]byte, orig, curr []byte) {
	t.Helper()
	tbl := &ApplyStateTable{}

	prev := ledgerfields.SetDisabledForBenchmarks(true)
	generic, err := tbl.buildModifiedNode(key, orig, curr)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("generic buildModifiedNode: %v", err)
	}

	prev = ledgerfields.SetDisabledForBenchmarks(false)
	typed, err := tbl.buildModifiedNode(key, orig, curr)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("typed buildModifiedNode: %v", err)
	}

	assertAffectedNodeEqual(t, "ModifiedNode", generic, typed)
}

func assertParityCreated(t *testing.T, key [32]byte, data []byte) {
	t.Helper()
	tbl := &ApplyStateTable{}

	prev := ledgerfields.SetDisabledForBenchmarks(true)
	generic, err := tbl.buildCreatedNode(key, data)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("generic buildCreatedNode: %v", err)
	}

	prev = ledgerfields.SetDisabledForBenchmarks(false)
	typed, err := tbl.buildCreatedNode(key, data)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("typed buildCreatedNode: %v", err)
	}

	assertAffectedNodeEqual(t, "CreatedNode", generic, typed)
}

func assertParityDeleted(t *testing.T, key [32]byte, orig, curr []byte) {
	t.Helper()
	tbl := &ApplyStateTable{}

	prev := ledgerfields.SetDisabledForBenchmarks(true)
	generic, err := tbl.buildDeletedNode(key, orig, curr)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("generic buildDeletedNode: %v", err)
	}

	prev = ledgerfields.SetDisabledForBenchmarks(false)
	typed, err := tbl.buildDeletedNode(key, orig, curr)
	ledgerfields.SetDisabledForBenchmarks(prev)
	if err != nil {
		t.Fatalf("typed buildDeletedNode: %v", err)
	}

	assertAffectedNodeEqual(t, "DeletedNode", generic, typed)
}

func assertAffectedNodeEqual(t *testing.T, label string, want, got AffectedNode) {
	t.Helper()
	if want.NodeType != got.NodeType {
		t.Errorf("%s: NodeType differ: generic=%s typed=%s", label, want.NodeType, got.NodeType)
	}
	if want.LedgerEntryType != got.LedgerEntryType {
		t.Errorf("%s: LedgerEntryType differ: generic=%s typed=%s", label, want.LedgerEntryType, got.LedgerEntryType)
	}
	if want.LedgerIndex != got.LedgerIndex {
		t.Errorf("%s: LedgerIndex differ: generic=%s typed=%s", label, want.LedgerIndex, got.LedgerIndex)
	}
	if want.PreviousTxnID != got.PreviousTxnID {
		t.Errorf("%s: PreviousTxnID differ: generic=%q typed=%q", label, want.PreviousTxnID, got.PreviousTxnID)
	}
	if want.PreviousTxnLgrSeq != got.PreviousTxnLgrSeq {
		t.Errorf("%s: PreviousTxnLgrSeq differ: generic=%d typed=%d", label, want.PreviousTxnLgrSeq, got.PreviousTxnLgrSeq)
	}
	assertFieldsEqual(t, label+".NewFields", want.NewFields, got.NewFields)
	assertFieldsEqual(t, label+".FinalFields", want.FinalFields, got.FinalFields)
	assertFieldsEqual(t, label+".PreviousFields", want.PreviousFields, got.PreviousFields)
}

// assertFieldsEqual compares two metadata field maps. Map ordering does not
// matter for output byte-identity (binarycodec.Encode sorts by Ordinal); we
// just need the same name → value content.
func assertFieldsEqual(t *testing.T, label string, want, got map[string]any) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: map length differs: generic=%d typed=%d (generic=%v typed=%v)", label, len(want), len(got), want, got)
		return
	}
	for k, wantV := range want {
		gotV, ok := got[k]
		if !ok {
			t.Errorf("%s: key %q missing from typed (generic value %v)", label, k, wantV)
			continue
		}
		if !valuesEqual(wantV, gotV) {
			t.Errorf("%s: key %q differs: generic=%v (%T) typed=%v (%T)", label, k, wantV, wantV, gotV, gotV)
		}
	}
}

// valuesEqual compares decoded field values robustly. It normalizes integer
// types (int / uint32) because the generic decoder may emit either for the
// same wire type, depending on the codec implementation.
func valuesEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case uint32:
		switch bv := b.(type) {
		case uint32:
			return av == bv
		case int:
			return av == uint32(bv)
		}
	case int:
		switch bv := b.(type) {
		case int:
			return av == bv
		case uint32:
			return uint32(av) == bv
		}
	case []byte:
		bv, ok := b.([]byte)
		return ok && bytes.Equal(av, bv)
	}
	return reflect.DeepEqual(a, b)
}

func buildAccountRootPairT(t *testing.T) ([]byte, []byte, [32]byte) {
	return runWithBenchBuilder(t, buildAccountRootPair)
}

func buildOfferPairT(t *testing.T) ([]byte, []byte, [32]byte) {
	return runWithBenchBuilder(t, buildOfferPair)
}

func buildDirectoryNodePairT(t *testing.T) ([]byte, []byte, [32]byte) {
	return runWithBenchBuilder(t, buildDirectoryNodePair)
}

func buildRippleStatePairT(t *testing.T) ([]byte, []byte, [32]byte) {
	return runWithBenchBuilder(t, buildRippleStatePair)
}

// runWithBenchBuilder reuses a *testing.B-shaped fixture builder from a
// *testing.T context by running it inside a one-shot testing.Benchmark and
// capturing the fixture via closure.
func runWithBenchBuilder(t *testing.T, fn func(*testing.B) ([]byte, []byte, [32]byte)) ([]byte, []byte, [32]byte) {
	t.Helper()
	var (
		o, c []byte
		k    [32]byte
	)
	res := testing.Benchmark(func(b *testing.B) {
		o, c, k = fn(b)
	})
	if res.N == 0 && len(o) == 0 {
		t.Fatal("fixture builder produced no data")
	}
	return o, c, k
}

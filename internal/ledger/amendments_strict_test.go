package ledger

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestLoadAmendmentsFromLedgerEntryCanonical(t *testing.T) {
	data := encodeAmendmentsEntry(t, [][32]byte{
		amendment.FeatureAMM,
		amendment.FeatureFixAMMv1_1,
	})

	rules, err := LoadAmendmentsFromLedgerEntry(data)
	if err != nil {
		t.Fatalf("LoadAmendmentsFromLedgerEntry: %v", err)
	}
	if !rules.Enabled(amendment.FeatureAMM) {
		t.Error("AMM must be enabled")
	}
	if !rules.Enabled(amendment.FeatureFixAMMv1_1) {
		t.Error("fixAMMv1_1 must be enabled")
	}
}

func TestLoadAmendmentsMatchesTypedDecoder(t *testing.T) {
	for _, ids := range [][][32]byte{
		nil,
		{amendment.FeatureAMM},
		{amendment.FeatureAMM, amendment.FeatureFixAMMv1_1},
	} {
		data := encodeAmendmentsEntry(t, ids)
		var decoded ledgerentry.Amendments
		if err := decoded.Decode(data); err != nil {
			t.Fatalf("typed decode: %v", err)
		}
		decodedIDs, err := decodeAmendmentIDs(decoded.Amendments)
		if err != nil {
			t.Fatalf("decode typed amendment IDs: %v", err)
		}
		if len(decodedIDs) != len(ids) {
			t.Fatalf("typed amendment count = %d, want %d", len(decodedIDs), len(ids))
		}
		rules, err := LoadAmendmentsFromLedgerEntry(data)
		if err != nil {
			t.Fatalf("load rules: %v", err)
		}
		for i, id := range decodedIDs {
			if id != ids[i] {
				t.Fatalf("typed amendment %d = %x, want %x", i, id, ids[i])
			}
			if !rules.Enabled(id) {
				t.Fatalf("loaded rules did not enable typed amendment %x", id)
			}
		}
	}
}

func TestLoadAmendmentsFromLedgerEntryRejectsMalformedData(t *testing.T) {
	canonical := encodeAmendmentsEntry(t, [][32]byte{amendment.FeatureAMM})
	wrongType := append([]byte(nil), canonical...)
	wrongType[2]++
	missingType := append([]byte(nil), canonical[3:]...)
	duplicateType := append(append([]byte(nil), canonical[:3]...), canonical...)
	truncated := append([]byte(nil), canonical[:len(canonical)-1]...)
	unknownField := append(append([]byte(nil), canonical...), 0x71)
	trailingData := append(append([]byte(nil), canonical...), 0)
	duplicateFlags := append(append([]byte(nil), canonical...), 0x22)
	flagsField := []byte{0x22, 0, 0, 0, 0}
	flagsOffset := bytes.Index(canonical, flagsField)
	if flagsOffset < 0 {
		t.Fatal("canonical entry does not contain the expected Flags field")
	}
	missingFlags := append([]byte(nil), canonical[:flagsOffset]...)
	missingFlags = append(missingFlags, canonical[flagsOffset+len(flagsField):]...)

	vectorHeader := []byte{0x03, 0x13, 0x20}
	vectorOffset := bytes.Index(canonical, vectorHeader)
	if vectorOffset < 0 {
		t.Fatal("canonical entry does not contain the expected Vector256 header")
	}
	badVectorLength := append([]byte(nil), canonical...)
	badVectorLength[vectorOffset+2]--
	excessiveNesting := append(append([]byte(nil), canonical...), 0xF0, 0x10, 0xE0, 0x12)
	excessiveNesting = append(excessiveNesting, bytes.Repeat([]byte{0xEA}, 50)...)

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "wrong ledger entry type", data: wrongType},
		{name: "missing ledger entry type", data: missingType},
		{name: "duplicate ledger entry type", data: duplicateType},
		{name: "truncated vector", data: truncated},
		{name: "unknown field", data: unknownField},
		{name: "trailing data", data: trailingData},
		{name: "duplicate flags", data: duplicateFlags},
		{name: "missing flags", data: missingFlags},
		{name: "non-multiple Vector256 length", data: badVectorLength},
		{name: "excessive nesting", data: excessiveNesting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadAmendmentsFromLedgerEntry(tc.data); err == nil {
				t.Fatal("LoadAmendmentsFromLedgerEntry succeeded for malformed data")
			}
		})
	}
}

func TestDecodeAmendmentIDsStrict(t *testing.T) {
	canonical := strings.Repeat("AB", 32)
	ids, err := decodeAmendmentIDs([]string{canonical})
	if err != nil {
		t.Fatalf("decodeAmendmentIDs: %v", err)
	}
	if len(ids) != 1 || !bytes.Equal(ids[0][:], bytes.Repeat([]byte{0xAB}, 32)) {
		t.Fatalf("decodeAmendmentIDs returned %X", ids)
	}

	for _, value := range []string{
		strings.Repeat("AB", 31),
		strings.Repeat("AB", 33),
		strings.Repeat("AB", 31) + "AZ",
	} {
		if _, err := decodeAmendmentIDs([]string{value}); err == nil {
			t.Fatalf("decodeAmendmentIDs accepted %q", value)
		}
	}
}

func TestLoadAmendmentsFromSHAMapPreservesBackendError(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(keylet.Amendments().Key, encodeAmendmentsEntry(t, [][32]byte{amendment.FeatureAMM})); err != nil {
		t.Fatalf("seed Amendments entry: %v", err)
	}
	family := newLifecycleMemoryFamily()
	lazyState := lifecycleLazyMap(t, stateMap, family)
	wantErr := errors.New("injected Amendments fetch failure")
	family.setFetchError(wantErr)

	if _, err := LoadAmendmentsFromSHAMapContext(context.Background(), lazyState); !errors.Is(err, wantErr) {
		t.Fatalf("LoadAmendmentsFromSHAMapContext error = %v, want %v", err, wantErr)
	}
}

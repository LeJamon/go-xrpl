package replaytool

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/shamap"
)

const testAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func encodeSLE(t *testing.T, m map[string]any) []byte {
	t.Helper()
	h, err := binarycodec.Encode(m)
	if err != nil {
		t.Fatalf("encode %v: %v", m, err)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}

func encodeMeta(t *testing.T, affected ...map[string]any) []byte {
	t.Helper()
	nodes := make([]any, len(affected))
	for i, a := range affected {
		nodes[i] = a
	}
	return encodeSLE(t, map[string]any{"AffectedNodes": nodes})
}

func mustIndex(t *testing.T, s string) [32]byte {
	t.Helper()
	idx, err := decodeIndex(s)
	if err != nil {
		t.Fatalf("decodeIndex %s: %v", s, err)
	}
	return idx
}

func stateRoot(t *testing.T, entries map[[32]byte][]byte) [32]byte {
	t.Helper()
	m, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("shamap.New: %v", err)
	}
	for k, v := range entries {
		if err := m.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	root, err := m.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return root
}

func putAll(t *testing.T, entries map[[32]byte][]byte) *shamap.SHAMap {
	t.Helper()
	m, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("shamap.New: %v", err)
	}
	for k, v := range entries {
		if err := m.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	return m
}

// TestReconstructFromMeta_ModifyWithFieldRemoval covers the hardest path: a
// ModifiedNode whose FinalFields is a partial delta (Balance changed) and whose
// PreviousFields names a field removed by the transaction (Domain). The
// reconstruction must overlay the delta onto the pre-object and drop the
// removed field, byte-for-byte.
func TestReconstructFromMeta_ModifyWithFieldRemoval(t *testing.T) {
	idxHex := "00000000000000000000000000000000000000000000000000000000000000AA"
	idx := mustIndex(t, idxHex)

	pre := map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         testAccount,
		"Balance":         "1000000000",
		"Flags":           0,
		"OwnerCount":      0,
		"Sequence":        1,
		"Domain":          "6578616D706C65",
	}
	post := map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Account":         testAccount,
		"Balance":         "2000000000",
		"Flags":           0,
		"OwnerCount":      0,
		"Sequence":        1,
	}

	preState := putAll(t, map[[32]byte][]byte{idx: encodeSLE(t, pre)})
	wantRoot := stateRoot(t, map[[32]byte][]byte{idx: encodeSLE(t, post)})

	meta := encodeMeta(t, map[string]any{
		"ModifiedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     idxHex,
			"FinalFields":     map[string]any{"Balance": "2000000000"},
			"PreviousFields":  map[string]any{"Balance": "1000000000", "Domain": "6578616D706C65"},
		},
	})

	corrected, err := reconstructFromMeta(preState, [][]byte{meta})
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, err := corrected.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("reconstructed root %x != expected %x", gotRoot[:8], wantRoot[:8])
	}
}

// TestReconstructFromMeta_CreateAndDelete covers CreatedNode (NewFields + the
// node-level LedgerEntryType) and DeletedNode, while an untouched object must
// be preserved verbatim.
func TestReconstructFromMeta_CreateAndDelete(t *testing.T) {
	idxKeep := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000001")
	idxNew := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000002")
	idxDel := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000003")

	keep := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "10", "Flags": 0, "OwnerCount": 0, "Sequence": 1,
	})
	del := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "20", "Flags": 0, "OwnerCount": 0, "Sequence": 2,
	})
	newFields := map[string]any{
		"Account": testAccount, "Balance": "30", "Flags": 0, "OwnerCount": 0, "Sequence": 3,
	}
	created := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "30", "Flags": 0, "OwnerCount": 0, "Sequence": 3,
	})

	preState := putAll(t, map[[32]byte][]byte{idxKeep: keep, idxDel: del})
	wantRoot := stateRoot(t, map[[32]byte][]byte{idxKeep: keep, idxNew: created})

	meta := encodeMeta(t,
		map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     hex.EncodeToString(idxNew[:]),
			"NewFields":       newFields,
		}},
		map[string]any{"DeletedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"LedgerIndex":     hex.EncodeToString(idxDel[:]),
			"FinalFields":     map[string]any{"Account": testAccount, "Balance": "20"},
		}},
	)

	corrected, err := reconstructFromMeta(preState, [][]byte{meta})
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, err := corrected.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("reconstructed root %x != expected %x", gotRoot[:8], wantRoot[:8])
	}

	if _, found, _ := corrected.Get(idxDel); found {
		t.Fatal("deleted node still present")
	}
	if _, found, _ := corrected.Get(idxNew); !found {
		t.Fatal("created node missing")
	}
}

func TestReconstructFromMeta_EmptyMetaLeavesStateUnchanged(t *testing.T) {
	idx := mustIndex(t, "00000000000000000000000000000000000000000000000000000000000000FF")
	obj := encodeSLE(t, map[string]any{
		"LedgerEntryType": "AccountRoot", "Account": testAccount,
		"Balance": "7", "Flags": 0, "OwnerCount": 0, "Sequence": 1,
	})
	preState := putAll(t, map[[32]byte][]byte{idx: obj})
	preRoot, _ := preState.Hash()

	corrected, err := reconstructFromMeta(preState, [][]byte{nil, {}})
	if err != nil {
		t.Fatalf("reconstructFromMeta: %v", err)
	}
	gotRoot, _ := corrected.Hash()
	if gotRoot != preRoot {
		t.Fatalf("empty meta changed root: %x != %x", gotRoot[:8], preRoot[:8])
	}
}

func TestDivergingObjects(t *testing.T) {
	idxSame := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000011")
	idxDiff := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000012")
	idxOnlyGo := mustIndex(t, "0000000000000000000000000000000000000000000000000000000000000013")

	same := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "1", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	goDiff := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "2", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	mainDiff := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "3", "Flags": 0, "OwnerCount": 0, "Sequence": 1})
	goOnly := encodeSLE(t, map[string]any{"LedgerEntryType": "AccountRoot", "Account": testAccount, "Balance": "4", "Flags": 0, "OwnerCount": 0, "Sequence": 1})

	goxrpl := putAll(t, map[[32]byte][]byte{idxSame: same, idxDiff: goDiff, idxOnlyGo: goOnly})
	mainnet := putAll(t, map[[32]byte][]byte{idxSame: same, idxDiff: mainDiff})

	diverging, err := divergingObjects(goxrpl, mainnet)
	if err != nil {
		t.Fatalf("divergingObjects: %v", err)
	}

	byIndex := map[string]divergingObject{}
	for _, d := range diverging {
		byIndex[d.Index] = d
	}
	if _, ok := byIndex[hex.EncodeToString(idxSame[:])]; ok {
		t.Fatal("identical object reported as diverging")
	}

	d, ok := byIndex[hex.EncodeToString(idxDiff[:])]
	if !ok || d.GoXRPL == "" || d.Mainnet == "" || d.GoXRPL == d.Mainnet {
		t.Fatalf("modified object not reported correctly: %+v", d)
	}

	d, ok = byIndex[hex.EncodeToString(idxOnlyGo[:])]
	if !ok || d.GoXRPL == "" || d.Mainnet != "" {
		t.Fatalf("go-only object should have empty mainnet side: %+v", d)
	}
}

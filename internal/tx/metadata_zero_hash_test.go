package tx

import "testing"

// TestIsZeroHashHex pins the predicate that drives the metadata
// "omit defaulted optional Hash256" path. The function is the only
// thing standing between goxrpl and an extra 33-byte field
// (PreviousTxnID = uint256{0}) showing up in every meta blob touching
// a never-before-modified account — most importantly the genesis
// master account, which broke SHAMap tx-tree-root parity with rippled
// in the issue #401 soak network.
func TestIsZeroHashHex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0", false},
		{"0000000000000000000000000000000000000000000000000000000000000000", true},
		// length 63 / 65 must NOT be treated as zero.
		{"000000000000000000000000000000000000000000000000000000000000000", false},
		{"00000000000000000000000000000000000000000000000000000000000000000", false},
		// any non-zero nibble disqualifies.
		{"0000000000000000000000000000000000000000000000000000000000000001", false},
		{"1000000000000000000000000000000000000000000000000000000000000000", false},
		// case sensitivity: rippled emits uppercase but our predicate
		// must not mistake a stray uppercase 'O' for a zero.
		{"00000000000000000000000000000000O000000000000000000000000000000", false},
	}
	for _, c := range cases {
		got := isZeroHashHex(c.in)
		if got != c.want {
			t.Errorf("isZeroHashHex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestAffectedNodeOmitsZeroPreviousTxnID guards
// affectedNodeToRippledFormat against re-emitting PreviousTxnID when
// it's all-zero. Without this gate the genesis master account picks
// up a defaulted PreviousTxnID at every tx that touches it, the
// SHAMap tx-tree leaf grows by 33 bytes, and the tree root drifts
// away from rippled's at the first round with master-account
// activity (issue #401).
func TestAffectedNodeOmitsZeroPreviousTxnID(t *testing.T) {
	zero := "0000000000000000000000000000000000000000000000000000000000000000"

	mod := AffectedNode{
		NodeType:        "ModifiedNode",
		LedgerEntryType: "AccountRoot",
		LedgerIndex:     "2B6AC232AA4C4BE41BF49D2459FA4A0347E1B543A4C92FCEE0821C0201E2E9A8",
		PreviousTxnID:   zero,
	}
	out, err := affectedNodeToRippledFormat(mod)
	if err != nil {
		t.Fatalf("affectedNodeToRippledFormat: %v", err)
	}
	inner, ok := out["ModifiedNode"].(map[string]any)
	if !ok {
		t.Fatalf("missing ModifiedNode wrapper: %v", out)
	}
	if _, present := inner["PreviousTxnID"]; present {
		t.Errorf("PreviousTxnID=zero must be omitted, got: %v", inner["PreviousTxnID"])
	}

	// Sanity: a real PreviousTxnID DOES survive.
	realTxnID := "1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF"
	mod.PreviousTxnID = realTxnID
	out, err = affectedNodeToRippledFormat(mod)
	if err != nil {
		t.Fatalf("affectedNodeToRippledFormat: %v", err)
	}
	inner = out["ModifiedNode"].(map[string]any)
	got, present := inner["PreviousTxnID"]
	if !present {
		t.Errorf("real PreviousTxnID was incorrectly omitted")
	} else if got != realTxnID {
		t.Errorf("PreviousTxnID round-trip failed: got %v want %v", got, realTxnID)
	}
}

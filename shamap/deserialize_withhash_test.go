package shamap

import (
	"bytes"
	"testing"
)

// serializedNode is a prefix-format node blob paired with its true SHAMap hash,
// as it would be stored in (and fetched from) the content-addressed nodestore.
type serializedNode struct {
	name string
	data []byte
	hash [32]byte
}

// buildSerializedNodes produces one prefix-format blob per node type, mirroring
// what a lazy descent fetches from the store.
func buildSerializedNodes(t testing.TB) []serializedNode {
	t.Helper()

	mkData := func(seed byte, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i)
		}
		return b
	}

	// Inner node with a few populated branches.
	inner := newInnerNode()
	for i := 0; i < BranchFactor; i += 5 {
		var h [32]byte
		for j := range h {
			h[j] = byte(i*7 + j + 1)
		}
		inner.hashes[i] = h
		inner.isBranch |= 1 << uint(i)
	}
	if err := inner.UpdateHash(); err != nil {
		t.Fatalf("inner UpdateHash: %v", err)
	}

	stateLeaf, err := newAccountStateLeafNode(NewItem([32]byte{0xAB, 0x01, 0x02}, mkData(0x10, 96)))
	if err != nil {
		t.Fatalf("account state leaf: %v", err)
	}
	txLeaf, err := newTransactionLeafNode(NewItem([32]byte{0xCD}, mkData(0x20, 80)))
	if err != nil {
		t.Fatalf("tx leaf: %v", err)
	}
	txMetaLeaf, err := newTransactionWithMetaLeafNode(NewItem([32]byte{0xEF, 0x09}, mkData(0x30, 160)))
	if err != nil {
		t.Fatalf("tx+meta leaf: %v", err)
	}

	var out []serializedNode
	for _, c := range []struct {
		name string
		node Node
	}{
		{"inner", inner},
		{"account_state_leaf", stateLeaf},
		{"transaction_leaf", txLeaf},
		{"transaction_with_meta_leaf", txMetaLeaf},
	} {
		data, err := c.node.SerializeWithPrefix()
		if err != nil {
			t.Fatalf("%s SerializeWithPrefix: %v", c.name, err)
		}
		out = append(out, serializedNode{name: c.name, data: data, hash: c.node.Hash()})
	}
	return out
}

// TestDeserializeFromPrefixWithHashMatchesRecompute proves the fast path is
// byte-for-byte equivalent to the recomputing path: installing the known
// content-addressed hash yields the same node hash and the same re-serialized
// bytes as recomputing it. This is the safety contract behind skipping the
// per-descent re-hash (issue #1084).
func TestDeserializeFromPrefixWithHashMatchesRecompute(t *testing.T) {
	for _, sn := range buildSerializedNodes(t) {
		t.Run(sn.name, func(t *testing.T) {
			recomputed, err := DeserializeFromPrefix(sn.data)
			if err != nil {
				t.Fatalf("DeserializeFromPrefix: %v", err)
			}
			if recomputed.Hash() != sn.hash {
				t.Fatalf("recompute hash %x != true hash %x", recomputed.Hash(), sn.hash)
			}

			withHash, err := DeserializeFromPrefixWithHash(sn.data, sn.hash)
			if err != nil {
				t.Fatalf("DeserializeFromPrefixWithHash: %v", err)
			}
			if withHash.Hash() != recomputed.Hash() {
				t.Fatalf("withHash hash %x != recompute hash %x", withHash.Hash(), recomputed.Hash())
			}

			// A loaded node must be clean, and must re-serialize to the exact
			// bytes it was decoded from (the content is untouched by the hash
			// shortcut).
			if withHash.IsDirty() {
				t.Fatal("withHash node marked dirty")
			}
			reser, err := withHash.SerializeWithPrefix()
			if err != nil {
				t.Fatalf("re-SerializeWithPrefix: %v", err)
			}
			if !bytes.Equal(reser, sn.data) {
				t.Fatalf("re-serialized bytes differ from original")
			}
		})
	}
}

func benchmarkDeserialize(b *testing.B, withHash bool) {
	nodes := buildSerializedNodes(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sn := nodes[i%len(nodes)]
		var err error
		if withHash {
			_, err = DeserializeFromPrefixWithHash(sn.data, sn.hash)
		} else {
			_, err = DeserializeFromPrefix(sn.data)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeserializeFromPrefix is the recomputing baseline.
func BenchmarkDeserializeFromPrefix(b *testing.B) { benchmarkDeserialize(b, false) }

// BenchmarkDeserializeFromPrefixWithHash is the descent fast path; compare its
// allocs/op and B/op against the baseline to see the per-fetch saving.
func BenchmarkDeserializeFromPrefixWithHash(b *testing.B) { benchmarkDeserialize(b, true) }

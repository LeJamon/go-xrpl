package shamap

import (
	"crypto/sha512"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

func prefixedTestNode(prefix [4]byte, payloadLen int, keyed bool) []byte {
	size := 4 + payloadLen
	if keyed {
		size += 32
	}
	data := make([]byte, size)
	copy(data, prefix[:])
	for i := 4; i < 4+payloadLen; i++ {
		data[i] = byte(i)
	}
	if keyed {
		data[len(data)-1] = 1
	}
	return data
}

func TestDecodePrefixBody(t *testing.T) {
	inner := prefixedTestNode(protocol.HashPrefixInnerNode(), BranchFactor*32, false)
	inner[4+3*32] = 1
	tests := []struct {
		name    string
		data    []byte
		kind    storedNodeKind
		payload int
		keyed   bool
	}{
		{"inner", inner, storedInner, BranchFactor * 32, false},
		{"account state", prefixedTestNode(protocol.HashPrefixLeafNode(), 12, true), storedAccountState, 12, true},
		{"transaction", prefixedTestNode(protocol.HashPrefixTransactionID(), 12, false), storedTransaction, 12, false},
		{"transaction with metadata", prefixedTestNode(protocol.HashPrefixTxNode(), 12, true), storedTransactionWithMeta, 12, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := decodePrefixBody(tt.data)
			if err != nil {
				t.Fatalf("decodePrefixBody: %v", err)
			}
			if body.kind != tt.kind || len(body.payload) != tt.payload {
				t.Fatalf("body = {kind:%d payload:%d}, want {kind:%d payload:%d}", body.kind, len(body.payload), tt.kind, tt.payload)
			}
			if tt.keyed && isZeroHash(body.key) {
				t.Fatal("keyed body has zero key")
			}
		})
	}
}

func TestDecodePrefixBodyRejectsMalformedData(t *testing.T) {
	unknown := make([]byte, 4)
	copy(unknown, "nope")
	zeroAccountKey := prefixedTestNode(protocol.HashPrefixLeafNode(), 12, true)
	for i := len(zeroAccountKey) - 32; i < len(zeroAccountKey); i++ {
		zeroAccountKey[i] = 0
	}
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"short prefix", []byte{1, 2, 3}, "data too short"},
		{"unknown prefix", unknown, "unknown hash prefix"},
		{"short inner", prefixedTestNode(protocol.HashPrefixInnerNode(), BranchFactor*32-1, false), "invalid inner node"},
		{"long inner", prefixedTestNode(protocol.HashPrefixInnerNode(), BranchFactor*32+1, false), "invalid inner node"},
		{"account missing key", prefixedTestNode(protocol.HashPrefixLeafNode(), 31, false), "account state"},
		{"zero account key", zeroAccountKey, "zero key"},
		{"empty transaction", prefixedTestNode(protocol.HashPrefixTransactionID(), 0, false), "transaction prefix"},
		{"transaction metadata missing key", prefixedTestNode(protocol.HashPrefixTxNode(), 31, false), "transaction+meta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodePrefixBody(tt.data); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decodePrefixBody error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPrefixDecodersPreservePayloadValidationLayers(t *testing.T) {
	tests := [][]byte{
		prefixedTestNode(protocol.HashPrefixLeafNode(), 11, true),
		prefixedTestNode(protocol.HashPrefixTransactionID(), 11, false),
		prefixedTestNode(protocol.HashPrefixTxNode(), 11, true),
	}
	for _, data := range tests {
		body, err := decodePrefixBody(data)
		if err != nil {
			t.Fatalf("decodePrefixBody rejected valid framing: %v", err)
		}
		if len(body.payload) != 11 {
			t.Fatalf("payload length = %d, want 11", len(body.payload))
		}
		if _, err := materializePrefixNode(body); err == nil {
			t.Fatal("materialization accepted a short item payload")
		}
		digest := sha512.Sum512(data)
		var expected [32]byte
		copy(expected[:], digest[:32])
		if _, err := decodeTraversalNode(data, expected); err == nil {
			t.Fatal("traversal decoder accepted a short item payload")
		}
	}
}

func TestPrefixMaterializationAndTraversalParity(t *testing.T) {
	inner := prefixedTestNode(protocol.HashPrefixInnerNode(), BranchFactor*32, false)
	inner[4+7*32] = 0xA5
	tests := [][]byte{
		inner,
		prefixedTestNode(protocol.HashPrefixLeafNode(), 12, true),
		prefixedTestNode(protocol.HashPrefixTransactionID(), 12, false),
		prefixedTestNode(protocol.HashPrefixTxNode(), 12, true),
	}
	for _, data := range tests {
		digest := sha512.Sum512(data)
		var expected [32]byte
		copy(expected[:], digest[:32])

		node, err := deserializeFromPrefix(data)
		if err != nil {
			t.Fatalf("deserializeFromPrefix: %v", err)
		}
		if got := node.Hash(); got != expected {
			t.Fatalf("materialized hash = %x, want %x", got[:8], expected[:8])
		}
		view, err := decodeTraversalNode(data, expected)
		if err != nil {
			t.Fatalf("decodeTraversalNode: %v", err)
		}
		innerNode, isInner := node.(*innerNode)
		if view.inner != isInner {
			t.Fatalf("traversal inner = %v, materialized inner = %v", view.inner, isInner)
		}
		if isInner {
			if view.branches != innerNode.isBranch || view.hashes != innerNode.hashes {
				t.Fatal("traversal and materialized inner branches differ")
			}
		}
	}
}

package shamap

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestCanonicalLeafPrefixEncodingMatchesLeafNode(t *testing.T) {
	key := commitmentTestKey(0x29)
	data := commitmentTestData(0x41)
	tests := []struct {
		name      string
		nodeType  NodeType
		kind      leafKind
		keyOnWire bool
	}{
		{name: "account state", nodeType: NodeTypeAccountState, kind: leafAccountState, keyOnWire: true},
		{name: "transaction", nodeType: NodeTypeTransactionNoMeta, kind: leafTransaction},
		{name: "transaction with metadata", nodeType: NodeTypeTransactionWithMeta, kind: leafTransactionWithMeta, keyOnWire: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node, err := createLeafNode(test.nodeType, NewItem(key, data))
			if err != nil {
				t.Fatalf("createLeafNode: %v", err)
			}
			got, err := node.SerializeWithPrefix()
			if err != nil {
				t.Fatalf("SerializeWithPrefix: %v", err)
			}
			want := append([]byte{}, leafKinds[test.kind].hashPrefix...)
			want = append(want, data...)
			if test.keyOnWire {
				want = append(want, key[:]...)
			}
			encoded := serializeLeafWithPrefix(test.kind, key, data)
			if !bytes.Equal(encoded, want) {
				t.Fatalf("encoded prefix bytes = %x, want %x", encoded, want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("node prefix bytes = %x, want %x", got, want)
			}
			if gotHash, wantHash := node.Hash(), sha512half.Sum(want); gotHash != wantHash {
				t.Fatalf("hash = %x, want %x", gotHash, wantHash)
			}
			wantLength := len(data) + len(leafKinds[test.kind].hashPrefix)
			if test.keyOnWire {
				wantLength += len(key)
			}
			if len(got) != wantLength {
				t.Fatalf("prefix length = %d, want %d", len(got), wantLength)
			}
		})
	}
}

func TestCanonicalInnerPrefixEncodingMatchesInnerNode(t *testing.T) {
	var hashes [BranchFactor][32]byte
	for i := range hashes {
		for j := range hashes[i] {
			hashes[i][j] = byte(i*BranchFactor + j)
		}
	}

	for _, branches := range []uint16{1, (1 << 3) | (1 << 11), ^uint16(0)} {
		t.Run(string(rune('a'+branches%26)), func(t *testing.T) {
			node := &innerNode{hashes: hashes, isBranch: branches}
			if err := node.UpdateHash(); err != nil {
				t.Fatalf("UpdateHash: %v", err)
			}
			got, err := node.SerializeWithPrefix()
			if err != nil {
				t.Fatalf("SerializeWithPrefix: %v", err)
			}
			want := make([]byte, fullInnerSerializedSize)
			copy(want[:4], protocol.HashPrefixInnerNode().Bytes())
			for i := range BranchFactor {
				if branches&(1<<i) != 0 {
					offset := 4 + i*32
					copy(want[offset:offset+32], hashes[i][:])
				}
			}
			encoded, err := serializeInnerWithPrefix(hashes, branches)
			if err != nil {
				t.Fatalf("serializeInnerWithPrefix: %v", err)
			}
			if !bytes.Equal(encoded, want) {
				t.Fatalf("encoded prefix bytes differ: got %x want %x", encoded, want)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("node prefix bytes differ: got %x want %x", got, want)
			}
			if gotHash, wantHash := node.Hash(), sha512half.Sum(want); gotHash != wantHash {
				t.Fatalf("hash = %x, want %x", gotHash, wantHash)
			}
		})
	}

	if _, err := serializeInnerWithPrefix(hashes, 0); !errors.Is(err, errEmptyNonRoot) {
		t.Fatalf("empty inner error = %v, want %v", err, errEmptyNonRoot)
	}
}

func TestCommitmentEmitLeafPreservesAdmissionErrorsAndSinkLifetime(t *testing.T) {
	key := commitmentTestKey(0x56)
	data := commitmentTestData(0x67)
	charge := commitmentNodeCharge(data)

	tooSmallBudget := &testCommitmentBudget{capacity: charge - 1}
	tooSmallBuilder := &commitmentBuilder{
		ctx:     context.Background(),
		mapType: TypeState,
		budget:  tooSmallBudget,
		emit:    func(FlushEntry) error { t.Fatal("sink called after admission failure"); return nil },
	}
	if _, err := tooSmallBuilder.emitLeaf(Change{Key: key, Data: data, Type: NodeTypeInner}); !errors.Is(err, ErrCommitmentBudget) {
		t.Fatalf("admission error = %v, want %v", err, ErrCommitmentBudget)
	}
	if used := tooSmallBudget.used.Load(); used != 0 {
		t.Fatalf("budget retained %d bytes after admission failure", used)
	}

	invalidBudget := &testCommitmentBudget{capacity: charge}
	invalidBuilder := &commitmentBuilder{
		ctx:     context.Background(),
		mapType: TypeState,
		budget:  invalidBudget,
		emit:    func(FlushEntry) error { t.Fatal("sink called for invalid leaf"); return nil },
	}
	if _, err := invalidBuilder.emitLeaf(Change{Key: key, Data: data, Type: NodeTypeInner}); err == nil || !strings.Contains(err.Error(), "invalid node type for leaf") {
		t.Fatalf("invalid leaf error = %v", err)
	}
	if used := invalidBudget.used.Load(); used != 0 {
		t.Fatalf("budget retained %d bytes after invalid leaf", used)
	}

	shortData := make([]byte, 11)
	shortBudget := &testCommitmentBudget{capacity: commitmentNodeCharge(shortData)}
	shortBuilder := &commitmentBuilder{
		ctx:     context.Background(),
		mapType: TypeState,
		budget:  shortBudget,
		emit:    func(FlushEntry) error { t.Fatal("sink called for short leaf"); return nil },
	}
	if _, err := shortBuilder.emitLeaf(Change{Key: key, Data: shortData, Type: NodeTypeAccountState}); !errors.Is(err, errItemTooSmall) {
		t.Fatalf("short leaf error = %v, want %v", err, errItemTooSmall)
	}
	if used := shortBudget.used.Load(); used != 0 {
		t.Fatalf("budget retained %d bytes after short leaf", used)
	}

	var observed []byte
	emitBudget := &testCommitmentBudget{capacity: charge}
	emitBuilder := &commitmentBuilder{
		ctx:     context.Background(),
		mapType: TypeState,
		budget:  emitBudget,
		emit: func(entry FlushEntry) error {
			if len(entry.Data) == 0 || &entry.Data[0] == &data[0] {
				t.Fatal("sink received aliased or empty leaf data")
			}
			observed = bytes.Clone(entry.Data)
			return nil
		},
	}
	ref, err := emitBuilder.emitLeaf(Change{Key: key, Data: data, Type: NodeTypeAccountState})
	if err != nil {
		t.Fatalf("emitLeaf: %v", err)
	}
	want := serializeLeafWithPrefix(leafAccountState, key, data)
	if !bytes.Equal(observed, want) {
		t.Fatalf("sink data = %x, want %x", observed, want)
	}
	if ref.hash != sha512half.Sum(want) {
		t.Fatalf("ref hash = %x, want %x", ref.hash, sha512half.Sum(want))
	}
	if used := emitBudget.used.Load(); used != 0 {
		t.Fatalf("budget retained %d bytes after successful emit", used)
	}
}

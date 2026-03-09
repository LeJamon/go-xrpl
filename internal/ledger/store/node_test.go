package store

import (
	"bytes"
	"testing"
)

func TestCreateObject(t *testing.T) {
	nodeType := TypeLedger
	data := []byte("test data")
	hash := [32]byte{1, 2, 3}

	obj := New(nodeType, data, hash)

	if obj == nil {
		t.Fatal("CreateObject returned nil")
	}

	if obj.Type() != nodeType {
		t.Errorf("Expected type %v, got %v", nodeType, obj.Type())
	}

	if obj.Hash() != hash {
		t.Errorf("Expected hash %v, got %v", hash, obj.Hash())
	}

	if !bytes.Equal(obj.Data(), data) {
		t.Errorf("Expected data %v, got %v", data, obj.Data())
	}
}

func TestNodeObjectTypes(t *testing.T) {
	tests := []struct {
		name     string
		nodeType ObjectType
		want     ObjectType
	}{
		{"Unknown", TypeUnknown, 0},
		{"Ledger", TypeLedger, 1},
		{"AccountNode", TypeAccount, 3},
		{"TransactionNode", TypeTransaction, 4},
		{"Dummy", TypeDummy, 512},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.nodeType != tt.want {
				t.Errorf("NodeObjectType %s = %d; want %d", tt.name, tt.nodeType, tt.want)
			}
		})
	}
}

func TestGetters(t *testing.T) {
	nodeType := TypeAccount
	data := []byte("test data for getters")
	hash := [32]byte{1, 2, 3, 4}

	obj := &Node{
		typ:  nodeType,
		hash: hash,
		data: data,
	}

	if got := obj.Type(); got != nodeType {
		t.Errorf("GetType() = %v; want %v", got, nodeType)
	}

	if got := obj.Hash(); got != hash {
		t.Errorf("GetHash() = %v; want %v", got, hash)
	}

	if got := obj.Data(); !bytes.Equal(got, data) {
		t.Errorf("GetData() = %v; want %v", got, data)
	}
}

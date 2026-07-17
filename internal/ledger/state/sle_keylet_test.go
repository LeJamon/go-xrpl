package state

import (
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestMatchesKeyletType(t *testing.T) {
	key := [32]byte{1}
	child := keylet.Child(key)
	if child.Type != entry.TypeChild || child.Key != key {
		t.Fatalf("Child() = %#v, want type %v and key %x", child, entry.TypeChild, key)
	}
	data := func(typ entry.Type) []byte {
		return []byte{0x11, byte(typ >> 8), byte(typ), 0}
	}

	tests := []struct {
		name string
		k    keylet.Keylet
		data []byte
		want bool
	}{
		{name: "any", k: keylet.Keylet{Type: entry.TypeAny, Key: key}, data: data(entry.TypeAccountRoot), want: true},
		{name: "any malformed", k: keylet.Keylet{Type: entry.TypeAny, Key: key}, data: []byte{0x11, 0x00}, want: true},
		{name: "exact", k: keylet.Keylet{Type: entry.TypeAccountRoot, Key: key}, data: data(entry.TypeAccountRoot), want: true},
		{name: "wrong exact", k: keylet.Keylet{Type: entry.TypeOffer, Key: key}, data: data(entry.TypeAccountRoot)},
		{name: "child", k: child, data: data(entry.TypeAccountRoot), want: true},
		{name: "directory is not a child", k: child, data: data(entry.TypeDirectoryNode)},
		{name: "child pseudo-type is not a child", k: child, data: data(entry.TypeChild)},
		{name: "malformed child", k: child, data: []byte{0x11, 0x00}},
		{name: "malformed exact", k: keylet.Keylet{Type: entry.TypeAccountRoot, Key: key}, data: []byte{0x11, 0x00}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchesKeyletType(test.k, test.data); got != test.want {
				t.Fatalf("MatchesKeyletType() = %v, want %v", got, test.want)
			}
		})
	}
}

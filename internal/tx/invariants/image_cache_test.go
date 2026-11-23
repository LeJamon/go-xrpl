package invariants

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestImageCacheDecodesEachImageOnce(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{Account: addrHolderA, Balance: 10, Sequence: 1})
	after := mustSerializeAccount(t, &state.AccountRoot{Account: addrHolderA, Balance: 9, Sequence: 2})
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}

	e := withImageCaches(entries)[0]
	if entries[0].images != nil {
		t.Fatal("withImageCaches modified the caller's entries")
	}

	first, err := e.accountRoot(e.Before)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := e.accountRoot(e.Before)
	if again != first || first.Balance != 10 {
		t.Fatalf("Before image not shared: %p vs %p, balance %d", first, again, first.Balance)
	}
	if afterAcct, _ := e.accountRoot(e.After); afterAcct == first || afterAcct.Balance != 9 {
		t.Fatalf("After image resolved to the wrong slot: balance %d", afterAcct.Balance)
	}
	if foreign, _ := e.accountRoot(bytes.Clone(e.Before)); foreign == first {
		t.Fatal("a slice that is not one of the entry's images was served from the cache")
	}
	if uncached, _ := entries[0].accountRoot(before); uncached == nil || uncached.Balance != 10 {
		t.Fatal("an entry without a cache must decode directly")
	}
}

func TestImageCacheMemoisesDecodeErrors(t *testing.T) {
	bad := []byte{0x11, 0x00, 0x61, 0xFF}
	_, wantErr := state.ParseAccountRoot(bad)
	if wantErr == nil {
		t.Fatal("expected the malformed image to fail to decode")
	}
	e := withImageCaches([]InvariantEntry{{EntryType: entry.TypeAccountRoot, After: bad}})[0]
	for range 2 {
		if _, err := e.accountRoot(e.After); err == nil || err.Error() != wantErr.Error() {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	}
}

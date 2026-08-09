package invariants

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// bookMapView is a ReadView backed by an in-memory key→bytes map, for exercising the
// directory-root existence check in finalize.
type bookMapView struct {
	stubView
	entries map[[32]byte][]byte
}

func (v bookMapView) Read(k keylet.Keylet) ([]byte, error) { return v.entries[k.Key], nil }
func (v bookMapView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}

// bookRoot builds a serialized book-directory root whose key encodes quality in
// its low 64 bits. exchangeRate is stored verbatim (pass a mismatching value to
// simulate legacy corruption).
func bookRoot(quality, exchangeRate uint64) (key [32]byte, data []byte) {
	key[0] = 0xB0 // arbitrary non-zero high bytes so key != rootIndex only when intended
	binary.BigEndian.PutUint64(key[24:], quality)
	dir := &state.DirectoryNode{
		RootIndex:    key,
		ExchangeRate: exchangeRate,
	}
	b, err := state.SerializeDirectoryNode(dir, true)
	if err != nil {
		panic(err)
	}
	return key, b
}

func TestCheckValidBookDirectory_RootExchangeRate(t *testing.T) {
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_2_0})

	// Correct root: exchangeRate == quality.
	key, data := bookRoot(0x1234_5678, 0x1234_5678)
	entries := []InvariantEntry{{Key: key, EntryType: entry.TypeDirectoryNode, After: data}}
	if v := checkValidBookDirectory(entries, stubView{}, on); v != nil {
		t.Fatalf("matching exchange rate must pass, got %v", v)
	}

	// Corrupt root: exchangeRate != quality.
	badKey, badData := bookRoot(0x1234_5678, 0x9999_9999)
	badEntries := []InvariantEntry{{Key: badKey, EntryType: entry.TypeDirectoryNode, After: badData}}
	if v := checkValidBookDirectory(badEntries, stubView{}, on); v == nil {
		t.Fatalf("mismatched exchange rate must fail")
	}

	// Pre-amendment: never fatal.
	if v := checkValidBookDirectory(badEntries, stubView{}, amendment.EmptyRules()); v != nil {
		t.Fatalf("pre-amendment must not fail, got %v", v)
	}
}

func TestCheckValidBookDirectory_ChildRootMustExist(t *testing.T) {
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_2_0})

	rootKey, rootData := bookRoot(0xABCD, 0xABCD)

	// A newly-created child page pointing at rootKey (child.key != rootIndex).
	var childKey [32]byte
	childKey[0] = 0xC0
	binary.BigEndian.PutUint64(childKey[24:], 1) // page number, differs from root
	child := &state.DirectoryNode{RootIndex: rootKey}
	childData, err := state.SerializeDirectoryNode(child, true)
	if err != nil {
		t.Fatal(err)
	}
	childEntry := []InvariantEntry{{Key: childKey, EntryType: entry.TypeDirectoryNode, After: childData}}

	// Root missing from the view → violation.
	if v := checkValidBookDirectory(childEntry, bookMapView{entries: map[[32]byte][]byte{}}, on); v == nil {
		t.Fatalf("missing root must fail")
	}

	// Root present → passes.
	view := bookMapView{entries: map[[32]byte][]byte{rootKey: rootData}}
	if v := checkValidBookDirectory(childEntry, view, on); v != nil {
		t.Fatalf("present root must pass, got %v", v)
	}
}

func TestBadExchangeRate_OwnerDirIgnored(t *testing.T) {
	// An owner directory (no book fields) is never a book root.
	var key [32]byte
	binary.BigEndian.PutUint64(key[24:], 42)
	dir := &state.DirectoryNode{RootIndex: key, Owner: [20]byte{1, 2, 3}}
	data, err := state.SerializeDirectoryNode(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if badExchangeRate(data, key) {
		t.Fatalf("owner directory must not be treated as a bad book root")
	}
}

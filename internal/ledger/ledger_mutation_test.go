package ledger

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// mutAcct builds a deterministic account keylet from a single seed byte by
// filling all 20 bytes of the account id with it. Distinct seeds yield
// distinct, collision-free keys for state-map fixtures.
func mutAcct(seed byte) keylet.Keylet {
	var id [20]byte
	for i := range id {
		id[i] = seed
	}
	return keylet.Account(id)
}

// mutData returns a deterministic 16-byte payload tagged by `tag`. The
// SHAMap rejects leaf data under 12 bytes, so fixtures must clear that
// floor; distinct tags yield distinct, comparable payloads.
func mutData(tag byte) []byte {
	d := make([]byte, 16)
	for i := range d {
		d[i] = tag
	}
	d[0] = 0x11
	binary.BigEndian.PutUint16(d[1:3], uint16(entry.TypeAccountRoot))
	return d
}

func TestLedger_ReadChecksKeyletType(t *testing.T) {
	l := newOpenChild(t)
	accountKey := mutAcct(0x01)
	data := mutData(0x11)
	if err := l.Insert(accountKey, data); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	wrongType := keylet.Keylet{Type: entry.TypePermissionedDomain, Key: accountKey.Key}
	got, err := l.Read(wrongType)
	if err != nil {
		t.Fatalf("Read wrong type: %v", err)
	}
	if got != nil {
		t.Fatalf("Read wrong type returned %x", got)
	}
	exists, err := l.Exists(wrongType)
	if err != nil {
		t.Fatalf("Exists wrong type: %v", err)
	}
	if !exists {
		t.Fatal("Exists did not report the occupied base key")
	}

	anyType := keylet.Keylet{Key: accountKey.Key}
	got, err = l.Read(anyType)
	if err != nil {
		t.Fatalf("Read any type: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Read any type: got %x want %x", got, data)
	}
	exists, err = l.Exists(anyType)
	if err != nil {
		t.Fatalf("Exists any type: %v", err)
	}
	if !exists {
		t.Fatal("Exists did not report an ltANY entry")
	}
}

// TestLedger_MutateHappyAndErrorPaths exercises Insert/Update/Erase on an
// open, mutable ledger: the success path for each, plus the three error
// paths the mutators guard. The happy paths make the error assertions
// meaningful — e.g. a duplicate Insert must be rejected AND must not
// clobber the original value.
func TestLedger_MutateHappyAndErrorPaths(t *testing.T) {
	l := newOpenChild(t)

	k := mutAcct(0x01)
	data := mutData(0x11)

	// Insert (happy): the entry becomes readable.
	if err := l.Insert(k, data); err != nil {
		t.Fatalf("Insert (happy): unexpected error: %v", err)
	}
	got, err := l.Read(k)
	if err != nil {
		t.Fatalf("Read after Insert: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Read after Insert: got %x want %x", got, data)
	}

	// Insert of an existing key fails with the "entry already exists" error.
	// That error is an ad-hoc errors.New (not a sentinel), so match on text.
	err = l.Insert(k, mutData(0x99))
	if err == nil {
		t.Fatalf("Insert (duplicate): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Insert (duplicate): error = %q, want it to mention \"already exists\"", err)
	}
	// The rejected duplicate must leave the original value intact.
	if got, _ = l.Read(k); !bytes.Equal(got, data) {
		t.Errorf("duplicate Insert overwrote entry: got %x want %x", got, data)
	}

	// Update (happy): the value is replaced.
	updated := mutData(0x44)
	if err := l.Update(k, updated); err != nil {
		t.Fatalf("Update (happy): unexpected error: %v", err)
	}
	if got, _ = l.Read(k); !bytes.Equal(got, updated) {
		t.Errorf("Read after Update: got %x want %x", got, updated)
	}

	// Update of a missing key fails with ErrEntryNotFound.
	missing := mutAcct(0x02)
	if err := l.Update(missing, mutData(0x00)); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("Update (missing): error = %v, want ErrEntryNotFound", err)
	}

	// Erase of a missing key fails with ErrEntryNotFound.
	if err := l.Erase(missing); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("Erase (missing): error = %v, want ErrEntryNotFound", err)
	}

	// Erase (happy): the existing entry is removed.
	if err := l.Erase(k); err != nil {
		t.Fatalf("Erase (happy): unexpected error: %v", err)
	}
	exists, err := l.Exists(k)
	if err != nil {
		t.Fatalf("Exists after Erase: %v", err)
	}
	if exists {
		t.Errorf("entry still present after Erase")
	}
}

func TestLedger_ApplyAtomically(t *testing.T) {
	l := newOpenChild(t)
	existing := mutAcct(0x03)
	original := mutData(0x11)
	if err := l.Insert(existing, original); err != nil {
		t.Fatalf("seed existing entry: %v", err)
	}
	l.AdjustDropsDestroyed(drops.XRPAmount(10))

	inserted := mutAcct(0x04)
	injected := errors.New("injected apply failure")
	err := l.ApplyAtomically(func(view Writer) error {
		if err := view.Update(existing, mutData(0x22)); err != nil {
			return err
		}
		if err := view.Insert(inserted, mutData(0x33)); err != nil {
			return err
		}
		view.AdjustDropsDestroyed(drops.XRPAmount(5))
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("ApplyAtomically error = %v, want %v", err, injected)
	}
	if got, _ := l.Read(existing); !bytes.Equal(got, original) {
		t.Fatalf("failed apply changed existing entry: got %x want %x", got, original)
	}
	if exists, _ := l.Exists(inserted); exists {
		t.Fatal("failed apply committed inserted entry")
	}
	if l.dropsDestroyed != drops.XRPAmount(10) {
		t.Fatalf("failed apply destroyed %d drops, want 10", l.dropsDestroyed)
	}

	err = l.ApplyAtomically(func(view Writer) error {
		if err := view.Update(existing, mutData(0x22)); err != nil {
			return err
		}
		if err := view.Insert(inserted, mutData(0x33)); err != nil {
			return err
		}
		view.AdjustDropsDestroyed(drops.XRPAmount(5))
		return nil
	})
	if err != nil {
		t.Fatalf("ApplyAtomically success: %v", err)
	}
	if got, _ := l.Read(existing); !bytes.Equal(got, mutData(0x22)) {
		t.Fatalf("successful apply existing entry = %x, want %x", got, mutData(0x22))
	}
	if got, _ := l.Read(inserted); !bytes.Equal(got, mutData(0x33)) {
		t.Fatalf("successful apply inserted entry = %x, want %x", got, mutData(0x33))
	}
	if l.dropsDestroyed != drops.XRPAmount(15) {
		t.Fatalf("successful apply destroyed %d drops, want 15", l.dropsDestroyed)
	}
}

// TestLedger_MutatorsRejectedWhenImmutable verifies that once a ledger is
// closed (state != StateOpen), all three mutators are rejected with
// ErrLedgerImmutable. The targeted entry is seeded while the ledger is
// still open so that Update/Erase aim at an EXISTING key — proving the
// rejection comes from the immutability guard, not the not-found guard.
func TestLedger_MutatorsRejectedWhenImmutable(t *testing.T) {
	l := newOpenChild(t)

	k := mutAcct(0x07)
	seed := mutData(0xAB)
	if err := l.Insert(k, seed); err != nil {
		t.Fatalf("seed Insert: %v", err)
	}

	if err := l.Close(l.CloseTime(), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if l.State() == StateOpen {
		t.Fatalf("ledger still StateOpen after Close")
	}

	newKey := mutAcct(0x08)
	if err := l.Insert(newKey, mutData(0x01)); !errors.Is(err, ErrLedgerImmutable) {
		t.Errorf("Insert after Close: error = %v, want ErrLedgerImmutable", err)
	}
	if err := l.Update(k, mutData(0x02)); !errors.Is(err, ErrLedgerImmutable) {
		t.Errorf("Update after Close: error = %v, want ErrLedgerImmutable", err)
	}
	if err := l.Erase(k); !errors.Is(err, ErrLedgerImmutable) {
		t.Errorf("Erase after Close: error = %v, want ErrLedgerImmutable", err)
	}

	// The seeded entry must survive the rejected mutations unchanged.
	got, err := l.Read(k)
	if err != nil {
		t.Fatalf("Read after rejected mutations: %v", err)
	}
	if !bytes.Equal(got, seed) {
		t.Errorf("entry mutated despite immutable rejection: got %x want %x", got, seed)
	}
}

// TestLedger_MutableSnapshot_Isolation proves a MutableSnapshot is an
// independent deep copy: mutating the snapshot (Insert/Update + the public
// AdjustDropsDestroyed) leaves the parent untouched, and mutating the
// parent afterwards (Insert/Erase) does not leak into the snapshot. These
// assertions would fail if the state map were shallow-aliased rather than
// copy-on-write.
func TestLedger_MutableSnapshot_Isolation(t *testing.T) {
	parent := newOpenChild(t)

	shared := mutAcct(0x10)
	sharedData := mutData(0x01)
	if err := parent.Insert(shared, sharedData); err != nil {
		t.Fatalf("seed shared: %v", err)
	}
	parent.AdjustDropsDestroyed(drops.XRPAmount(100))

	snap, err := parent.MutableSnapshot()
	if err != nil {
		t.Fatalf("MutableSnapshot: %v", err)
	}

	// The snapshot begins as an exact copy of parent state and tally.
	if got, _ := snap.Read(shared); !bytes.Equal(got, sharedData) {
		t.Fatalf("snapshot missing shared entry at creation: got %x", got)
	}
	if snap.dropsDestroyed != parent.dropsDestroyed {
		t.Fatalf("snapshot dropsDestroyed = %d, want %d (copy at creation)",
			int64(snap.dropsDestroyed), int64(parent.dropsDestroyed))
	}

	// Mutate the SNAPSHOT in every way.
	snapOnly := mutAcct(0x11)
	snapShared := mutData(0xDE)
	if err := snap.Insert(snapOnly, mutData(0xFF)); err != nil {
		t.Fatalf("snap Insert: %v", err)
	}
	if err := snap.Update(shared, snapShared); err != nil {
		t.Fatalf("snap Update: %v", err)
	}
	snap.AdjustDropsDestroyed(drops.XRPAmount(50))

	// Parent must not observe ANY of the snapshot's mutations.
	if ex, _ := parent.Exists(snapOnly); ex {
		t.Errorf("parent saw snapshot-only insert (state map aliased)")
	}
	if got, _ := parent.Read(shared); !bytes.Equal(got, sharedData) {
		t.Errorf("parent shared entry changed by snapshot update: got %x want %x", got, sharedData)
	}
	if int64(parent.dropsDestroyed) != 100 {
		t.Errorf("parent dropsDestroyed changed by snapshot: got %d want 100", int64(parent.dropsDestroyed))
	}

	// The snapshot does observe its own mutations.
	if got, _ := snap.Read(shared); !bytes.Equal(got, snapShared) {
		t.Errorf("snapshot update not visible to itself: got %x want %x", got, snapShared)
	}
	if int64(snap.dropsDestroyed) != 150 {
		t.Errorf("snapshot dropsDestroyed: got %d want 150", int64(snap.dropsDestroyed))
	}

	// Now mutate the PARENT; the snapshot must not observe it either.
	parentOnly := mutAcct(0x12)
	if err := parent.Insert(parentOnly, mutData(0xAA)); err != nil {
		t.Fatalf("parent Insert: %v", err)
	}
	if err := parent.Erase(shared); err != nil {
		t.Fatalf("parent Erase shared: %v", err)
	}

	if ex, _ := snap.Exists(parentOnly); ex {
		t.Errorf("snapshot saw parent-only insert (state map aliased)")
	}
	// The parent erased `shared`, but the snapshot keeps its own version.
	if got, _ := snap.Read(shared); !bytes.Equal(got, snapShared) {
		t.Errorf("parent Erase leaked into snapshot: got %x want %x", got, snapShared)
	}
}

// TestLedger_TwoMutableSnapshots_Independent proves two mutable snapshots
// taken from the same parent are independent of one another: mutations to
// snapshot A are invisible to snapshot B (and to the parent).
func TestLedger_TwoMutableSnapshots_Independent(t *testing.T) {
	parent := newOpenChild(t)

	base := mutAcct(0x20)
	baseData := mutData(0x00)
	if err := parent.Insert(base, baseData); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	a, err := parent.MutableSnapshot()
	if err != nil {
		t.Fatalf("MutableSnapshot a: %v", err)
	}
	b, err := parent.MutableSnapshot()
	if err != nil {
		t.Fatalf("MutableSnapshot b: %v", err)
	}

	// Mutate only A.
	onlyA := mutAcct(0x21)
	if err := a.Insert(onlyA, mutData(0x01)); err != nil {
		t.Fatalf("a Insert: %v", err)
	}
	if err := a.Update(base, mutData(0xA0)); err != nil {
		t.Fatalf("a Update base: %v", err)
	}
	a.AdjustDropsDestroyed(drops.XRPAmount(7))

	// B must be entirely unaffected by A.
	if ex, _ := b.Exists(onlyA); ex {
		t.Errorf("snapshot B saw snapshot A's insert")
	}
	if got, _ := b.Read(base); !bytes.Equal(got, baseData) {
		t.Errorf("snapshot B base changed by A: got %x want %x", got, baseData)
	}
	if int64(b.dropsDestroyed) != 0 {
		t.Errorf("snapshot B dropsDestroyed changed by A: got %d want 0", int64(b.dropsDestroyed))
	}

	// And so must the parent.
	if ex, _ := parent.Exists(onlyA); ex {
		t.Errorf("parent saw snapshot A's insert")
	}
	if got, _ := parent.Read(base); !bytes.Equal(got, baseData) {
		t.Errorf("parent base changed by A: got %x want %x", got, baseData)
	}
}

// TestLedger_Snapshot_ReadIsolation proves the immutable Snapshot() captures
// parent state at snapshot time and is insulated from subsequent parent
// mutations — the read-isolation half of copy-on-write.
func TestLedger_Snapshot_ReadIsolation(t *testing.T) {
	parent := newOpenChild(t)

	k := mutAcct(0x30)
	orig := mutData(0x01)
	if err := parent.Insert(k, orig); err != nil {
		t.Fatalf("seed: %v", err)
	}

	snap, err := parent.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got, _ := snap.Read(k); !bytes.Equal(got, orig) {
		t.Fatalf("snapshot missing entry at creation: got %x", got)
	}

	// Parent mutates AFTER the snapshot was taken.
	if err := parent.Update(k, mutData(0xFF)); err != nil {
		t.Fatalf("parent Update: %v", err)
	}
	added := mutAcct(0x31)
	if err := parent.Insert(added, mutData(0x09)); err != nil {
		t.Fatalf("parent Insert: %v", err)
	}

	// The immutable snapshot still shows the state as of its creation.
	if got, _ := snap.Read(k); !bytes.Equal(got, orig) {
		t.Errorf("parent update leaked into immutable snapshot: got %x want %x", got, orig)
	}
	if ex, _ := snap.Exists(added); ex {
		t.Errorf("parent insert leaked into immutable snapshot")
	}
}

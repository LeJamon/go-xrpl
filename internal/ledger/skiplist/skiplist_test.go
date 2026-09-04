package skiplist

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

// synthHash returns a deterministic, distinct stand-in for the hash of ledger
// seq. The skip-list logic is agnostic to the actual hash values — only
// LastLedgerSequence and the Hashes length are load-bearing — so synthetic
// hashes keep these unit tests decoupled from full ledger construction (and
// from the internal/ledger package, avoiding an import cycle).
func synthHash(seq uint32) [32]byte {
	var h [32]byte
	binary.BigEndian.PutUint32(h[0:4], seq)
	h[4] = 0xAB
	return h
}

// seedToSeq builds a state map whose rolling LedgerHashes SLE describes
// ledgers 1..target-1 — exactly the state a parent ledger at sequence `target`
// carries — by walking the production UpdateOnMap append path. It returns the
// state map and the synthetic per-ledger hashes indexed by sequence.
func seedToSeq(t *testing.T, target uint32) (*shamap.SHAMap, [][32]byte) {
	t.Helper()
	sm := shamap.New(shamap.TypeState)
	hashes := make([][32]byte, target+1)
	for s := uint32(1); s <= target; s++ {
		hashes[s] = synthHash(s)
	}
	for seq := uint32(2); seq <= target; seq++ {
		if err := UpdateOnMap(sm, seq, hashes[seq-1]); err != nil {
			t.Fatalf("seed UpdateOnMap at seq %d: %v", seq, err)
		}
	}
	return sm, hashes
}

// TestUpdateOnMap_RejectsCorruptedExistingSLE pins the defensive invariant
// check for issue #470: if the existing rolling LedgerHashes SLE encodes a
// state that isn't "parent describes ledgers 1..parent.seq-1", the next
// append must fail loudly rather than append on top of corrupted state and
// emit a divergent ledger.
func TestUpdateOnMap_RejectsCorruptedExistingSLE(t *testing.T) {
	// Seed to seq 5 so the rolling SLE describes ledgers 1..4.
	sm, hashes := seedToSeq(t, 5)

	// Corrupt the rolling LedgerHashes SLE: bump LastLedgerSequence by 1 and
	// append an extra (ghost) hash, simulating the leakage pattern from issue
	// #470 where a speculative consensus attempt polluted persisted state.
	rollingKey := keylet.LedgerHashes()
	existingFields, existingHashes, existingLast, err := ReadLedgerHashesSLE(sm, rollingKey.Key)
	if err != nil {
		t.Fatalf("ReadLedgerHashesSLE: %v", err)
	}
	corruptedHashes := append([][32]byte(nil), existingHashes...)
	var ghost [32]byte
	ghost[0] = 0xDE
	ghost[1] = 0xAD
	corruptedHashes = append(corruptedHashes, ghost)
	if err := Write(sm, rollingKey.Key, existingFields, corruptedHashes, existingLast+1); err != nil {
		t.Fatalf("Write (corrupting): %v", err)
	}

	// Now attempt the chain-advance to seq 6. The assertion must fire.
	if err := UpdateOnMap(sm, 6, hashes[5]); err == nil {
		t.Fatalf("UpdateOnMap on corrupted SLE: want error, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestUpdateOnMapIgnoresSequenceZero(t *testing.T) {
	t.Parallel()
	if err := UpdateOnMap(shamap.New(shamap.TypeState), 0, [32]byte{1}); err != nil {
		t.Fatalf("UpdateOnMap sequence zero: %v", err)
	}
}

func TestReadLedgerHashesRejectsMissingLastSequence(t *testing.T) {
	t.Parallel()
	sm := shamap.New(shamap.TypeState)
	entry := &ledgerfields.LedgerHashes{}
	entry.SetFlags(0)
	entry.SetHashes([]string{fmt.Sprintf("%064X", [32]byte{1})})
	data, err := entry.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := sm.Put(keylet.LedgerHashes().Key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, _, _, err := ReadLedgerHashesSLE(sm, keylet.LedgerHashes().Key); err == nil || err.Error() != "LedgerHashes missing LastLedgerSequence" {
		t.Fatalf("error = %v, want missing LastLedgerSequence", err)
	}
}

func TestReadLedgerHashesValidatesTypedFields(t *testing.T) {
	tests := []struct {
		name      string
		hashCount int
		first     *uint32
		last      uint32
		wantErr   string
	}{
		{name: "empty hashes", last: 1, wantErr: "LedgerHashes has invalid Hashes cardinality 0"},
		{name: "too many hashes", hashCount: 257, last: 257, wantErr: "LedgerHashes has invalid Hashes cardinality 257"},
		{name: "zero first sequence", hashCount: 1, first: uint32Pointer(0), last: 1, wantErr: "LedgerHashes has invalid FirstLedgerSequence"},
		{name: "first after last", hashCount: 1, first: uint32Pointer(2), last: 1, wantErr: "LedgerHashes has invalid FirstLedgerSequence"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := &ledgerfields.LedgerHashes{}
			entry.SetFlags(0)
			entry.SetLastLedgerSequence(test.last)
			if test.first != nil {
				entry.SetFirstLedgerSequence(*test.first)
			}
			hashes := make([]string, test.hashCount)
			for i := range hashes {
				hashes[i] = fmt.Sprintf("%064X", synthHash(uint32(i+1)))
			}
			entry.SetHashes(hashes)
			data, err := entry.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			sm := shamap.New(shamap.TypeState)
			if err := sm.Put(keylet.LedgerHashes().Key, data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if _, _, _, err := ReadLedgerHashesSLE(sm, keylet.LedgerHashes().Key); err == nil || err.Error() != test.wantErr {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

// TestUpdateOnMap_PreservesFirstLedgerSequence pins issue #1008: the rolling
// LedgerHashes SLE is rewritten on every close, and that rewrite must keep the
// optional FirstLedgerSequence the existing object carries. Mainnet's rolling
// skip list has FirstLedgerSequence=2; dropping it on rewrite shortens the SLE
// by 6 bytes, shifting its state-tree leaf and diverging account_hash. rippled
// mutates the SLE in place (Ledger.cpp:878-943), preserving the field.
func TestUpdateOnMap_PreservesFirstLedgerSequence(t *testing.T) {
	// Seed to seq 5 → rolling SLE describes ledgers 1..4.
	sm, hashes := seedToSeq(t, 5)
	rollingKey := keylet.LedgerHashes()

	// Stamp FirstLedgerSequence=2 onto the existing rolling SLE, as a
	// mainnet-seeded state map would carry it. Keep Hashes/LastLedgerSequence
	// untouched so the next chain-advance assertion still holds.
	const firstSeq = uint32(2)
	entry, h, lastSeq, err := ReadLedgerHashesSLE(sm, rollingKey.Key)
	if err != nil {
		t.Fatalf("ReadLedgerHashesSLE: %v", err)
	}
	if entry == nil {
		t.Fatalf("rolling LedgerHashes SLE absent after seeding to seq 5")
	}
	entry.SetFirstLedgerSequence(firstSeq)
	if err := Write(sm, rollingKey.Key, entry, h, lastSeq); err != nil {
		t.Fatalf("Write (seeding FirstLedgerSequence): %v", err)
	}

	// Advance one ledger — the rewrite under test.
	if err := UpdateOnMap(sm, 6, hashes[5]); err != nil {
		t.Fatalf("UpdateOnMap: %v", err)
	}

	// FirstLedgerSequence must survive, and the list must have advanced.
	gotEntry, gotHashes, gotLast, err := ReadLedgerHashesSLE(sm, rollingKey.Key)
	if err != nil {
		t.Fatalf("ReadLedgerHashesSLE after advance: %v", err)
	}
	if !gotEntry.HasFirstLedgerSequence() {
		t.Fatalf("FirstLedgerSequence dropped on rewrite (issue #1008)")
	}
	gotFirst := gotEntry.FirstLedgerSequence
	if gotFirst != firstSeq {
		t.Errorf("FirstLedgerSequence = %d, want %d", gotFirst, firstSeq)
	}
	if want := uint32(5); gotLast != want {
		t.Errorf("LastLedgerSequence = %d, want %d", gotLast, want)
	}
	if want := len(h) + 1; len(gotHashes) != want {
		t.Errorf("len(Hashes) = %d, want %d", len(gotHashes), want)
	}
	if gotHashes[len(gotHashes)-1] != hashes[5] {
		t.Errorf("last Hashes entry = %x, want parent hash %x", gotHashes[len(gotHashes)-1], hashes[5])
	}
}

// TestUpdateOnMap_CreatedSkipListHasNoFirstLedgerSequence pins the other half
// of rippled parity: when the SLE is created from scratch only Hashes and
// LastLedgerSequence are set, so a node built from a fresh genesis must not
// synthesize a FirstLedgerSequence the reference never writes.
func TestUpdateOnMap_CreatedSkipListHasNoFirstLedgerSequence(t *testing.T) {
	sm, _ := seedToSeq(t, 5)
	entry, _, _, err := ReadLedgerHashesSLE(sm, keylet.LedgerHashes().Key)
	if err != nil {
		t.Fatalf("ReadLedgerHashesSLE: %v", err)
	}
	if entry == nil {
		t.Fatalf("rolling LedgerHashes SLE absent after seeding to seq 5")
	}
	if entry.HasFirstLedgerSequence() {
		t.Errorf("freshly created skip list has FirstLedgerSequence; rippled does not set it on creation")
	}
}

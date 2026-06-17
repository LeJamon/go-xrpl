package ledger

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
)

// decodeRollingLedgerHashesForTest reads the rolling-256 LedgerHashes SLE from `l`
// and returns its (LastLedgerSequence, Hashes) tuple. (0, nil, nil) when
// the SLE is absent — early ledgers before the entry is first written.
func decodeRollingLedgerHashesForTest(t *testing.T, l *Ledger) (uint32, []string) {
	t.Helper()
	raw, err := l.Read(keylet.LedgerHashes())
	if err != nil {
		t.Fatalf("read LedgerHashes SLE: %v", err)
	}
	if raw == nil {
		return 0, nil
	}
	decoded, err := binarycodec.DecodeBytes(raw)
	if err != nil {
		t.Fatalf("decode LedgerHashes SLE: %v", err)
	}
	var lastSeq uint32
	switch v := decoded["LastLedgerSequence"].(type) {
	case uint32:
		lastSeq = v
	case int:
		lastSeq = uint32(v)
	case int64:
		lastSeq = uint32(v)
	case uint64:
		lastSeq = uint32(v)
	default:
		t.Fatalf("LastLedgerSequence type: %T", decoded["LastLedgerSequence"])
	}
	var hashes []string
	switch v := decoded["Hashes"].(type) {
	case []string:
		hashes = append([]string(nil), v...)
	case []any:
		for _, h := range v {
			hashes = append(hashes, h.(string))
		}
	default:
		t.Fatalf("Hashes type: %T", decoded["Hashes"])
	}
	return lastSeq, hashes
}

// TestLedger_Close_LedgerHashes_NoSelfInclusion verifies the
// invariant from issue #470: the LedgerHashes object inside a freshly
// closed ledger N must record hashes for ledgers 1..N-1 — never N
// itself. Self-inclusion is structurally impossible (the hash is not
// known until the state, which contains LedgerHashes, is finalized)
// and produces an immediate fork against rippled.
//
// Reference: rippled Ledger::updateSkipList (Ledger.cpp:878-943) —
// the skip-list update appends info_.parentHash and stamps
// LastLedgerSequence to prevIndex = info_.seq - 1.
func TestLedger_Close_LedgerHashes_NoSelfInclusion(t *testing.T) {
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})

	// Walk 1 -> 17 (the exact failure point from issue #470).
	hashes := map[uint32][32]byte{parent.Sequence(): parent.Hash()}
	const target = uint32(17)
	for parent.Sequence() < target {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		child, err := NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen at seq %d: %v", parent.Sequence()+1, err)
		}
		if err := child.Close(closeTime, 0); err != nil {
			t.Fatalf("Close at seq %d: %v", child.Sequence(), err)
		}

		seq := child.Sequence()
		lastSeq, hashStrs := decodeRollingLedgerHashesForTest(t, child)

		// Self-inclusion check: the child's own hash must NOT appear.
		selfHashHex := fmt.Sprintf("%064X", child.Hash())
		for i, h := range hashStrs {
			if h == selfHashHex {
				t.Fatalf("ledger %d: LedgerHashes contains own hash at index %d (%s) — self-inclusion bug from issue #470", seq, i, h)
			}
		}

		// LastLedgerSequence must equal parent's seq, not self.
		if got, want := lastSeq, seq-1; got != want {
			t.Errorf("ledger %d: LastLedgerSequence = %d, want %d (parent seq)", seq, got, want)
		}

		// Length must equal parent seq (cap 256 for the rolling list).
		wantLen := min(int(seq-1), 256)
		if got := len(hashStrs); got != wantLen {
			t.Errorf("ledger %d: len(Hashes) = %d, want %d", seq, got, wantLen)
		}

		// Last entry must equal parent.Hash().
		if len(hashStrs) > 0 {
			gotLast, _ := hex.DecodeString(hashStrs[len(hashStrs)-1])
			parentHash := parent.Hash()
			if fmt.Sprintf("%x", gotLast) != fmt.Sprintf("%x", parentHash[:]) {
				t.Errorf("ledger %d: last Hashes entry = %s, want parent hash %x", seq, hashStrs[len(hashStrs)-1], parentHash)
			}
		}

		// Every entry in Hashes must correspond to a real ancestor.
		for i, h := range hashStrs {
			gotBytes, _ := hex.DecodeString(h)
			var got [32]byte
			copy(got[:], gotBytes)
			// hashes index in the array corresponds to ledger seq (i+1)
			// since rolling list runs 1..seq-1 once we're past genesis
			// and below the 256 cap.
			ancestorSeq := uint32(i + 1)
			if wantLen == 256 {
				// In the saturated case, slot i maps to seq (seq-256+i).
				ancestorSeq = seq - 256 + uint32(i)
			}
			want, ok := hashes[ancestorSeq]
			if !ok {
				continue
			}
			if got != want {
				t.Errorf("ledger %d: Hashes[%d] = %x, want %x (ancestor seq %d)", seq, i, got, want, ancestorSeq)
			}
		}

		hashes[seq] = child.Hash()
		parent = child
	}
}

// TestUpdateSkipListOnMap_RejectsCorruptedExistingSLE pins the defensive
// invariant check added for issue #470: if the existing rolling
// LedgerHashes SLE encodes a state that isn't "parent describes ledgers
// 1..parent.seq-1", the next close must fail loudly rather than append
// on top of corrupted state and emit a divergent ledger.
func TestUpdateSkipListOnMap_RejectsCorruptedExistingSLE(t *testing.T) {
	// Walk to a parent at seq 5 so the existing SLE has 4 entries.
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
	for parent.Sequence() < 5 {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		child, err := NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen: %v", err)
		}
		if err := child.Close(closeTime, 0); err != nil {
			t.Fatalf("Close: %v", err)
		}
		parent = child
	}

	// Build a snapshot of parent's state, then directly corrupt the
	// rolling LedgerHashes SLE: bump LastLedgerSequence by 1 and append
	// an extra (ghost) hash. This simulates the leakage pattern from
	// issue #470 where a speculative consensus attempt polluted the
	// parent's persisted state.
	stateMap, err := parent.stateMap.Snapshot(true)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	rollingKey := keylet.LedgerHashes()
	existingFields, existingHashes, existingLast, err := readLedgerHashesSLE(stateMap, rollingKey.Key)
	if err != nil {
		t.Fatalf("readLedgerHashesSLE: %v", err)
	}
	corruptedHashes := append([][32]byte(nil), existingHashes...)
	var ghost [32]byte
	ghost[0] = 0xDE
	ghost[1] = 0xAD
	corruptedHashes = append(corruptedHashes, ghost)
	if err := writeSkipList(stateMap, rollingKey.Key, existingFields, corruptedHashes, existingLast+1); err != nil {
		t.Fatalf("writeSkipList (corrupting): %v", err)
	}

	// Now attempt the chain-advance close. The assertion must fire.
	err = UpdateSkipListOnMap(stateMap, parent.Sequence()+1, parent.Hash())
	if err == nil {
		t.Fatalf("UpdateSkipListOnMap on corrupted SLE: want error, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// walkToSeq advances a freshly created genesis ledger up to (and including)
// the target sequence, returning the ledger at that seq.
func walkToSeq(t *testing.T, target uint32) *Ledger {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
	for parent.Sequence() < target {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		child, err := NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen at seq %d: %v", parent.Sequence()+1, err)
		}
		if err := child.Close(closeTime, 0); err != nil {
			t.Fatalf("Close at seq %d: %v", child.Sequence(), err)
		}
		parent = child
	}
	return parent
}

// TestUpdateSkipListOnMap_PreservesFirstLedgerSequence pins issue #1008: the
// rolling LedgerHashes SLE is rewritten on every close, and that rewrite must
// keep the optional FirstLedgerSequence the existing object carries. Mainnet's
// rolling skip list has FirstLedgerSequence=2; dropping it on rewrite shortens
// the SLE by 6 bytes, shifting its state-tree leaf and diverging account_hash.
// rippled mutates the SLE in place (Ledger.cpp:878-943), preserving the field.
func TestUpdateSkipListOnMap_PreservesFirstLedgerSequence(t *testing.T) {
	// Parent at seq 5 → rolling SLE describes ledgers 1..4.
	parent := walkToSeq(t, 5)

	stateMap, err := parent.stateMap.Snapshot(true)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	rollingKey := keylet.LedgerHashes()

	// Stamp FirstLedgerSequence=2 onto the existing rolling SLE, as a
	// mainnet-seeded state map would carry it. Keep Hashes/LastLedgerSequence
	// untouched so the next chain-advance assertion still holds.
	const firstSeq = uint32(2)
	fields, hashes, lastSeq, err := readLedgerHashesSLE(stateMap, rollingKey.Key)
	if err != nil {
		t.Fatalf("readLedgerHashesSLE: %v", err)
	}
	if fields == nil {
		t.Fatalf("rolling LedgerHashes SLE absent at parent seq 5")
	}
	fields["FirstLedgerSequence"] = firstSeq
	if err := writeSkipList(stateMap, rollingKey.Key, fields, hashes, lastSeq); err != nil {
		t.Fatalf("writeSkipList (seeding FirstLedgerSequence): %v", err)
	}

	// Advance one ledger — the rewrite under test.
	if err := UpdateSkipListOnMap(stateMap, parent.Sequence()+1, parent.Hash()); err != nil {
		t.Fatalf("UpdateSkipListOnMap: %v", err)
	}

	// FirstLedgerSequence must survive, and the list must have advanced.
	gotFields, gotHashes, gotLast, err := readLedgerHashesSLE(stateMap, rollingKey.Key)
	if err != nil {
		t.Fatalf("readLedgerHashesSLE after advance: %v", err)
	}
	gotFirst, err := decodeUint32Field(gotFields, "FirstLedgerSequence")
	if err != nil {
		t.Fatalf("decode FirstLedgerSequence: %v", err)
	}
	if _, ok := gotFields["FirstLedgerSequence"]; !ok {
		t.Fatalf("FirstLedgerSequence dropped on rewrite (issue #1008)")
	}
	if gotFirst != firstSeq {
		t.Errorf("FirstLedgerSequence = %d, want %d", gotFirst, firstSeq)
	}
	if want := parent.Sequence(); gotLast != want {
		t.Errorf("LastLedgerSequence = %d, want %d", gotLast, want)
	}
	if want := len(hashes) + 1; len(gotHashes) != want {
		t.Errorf("len(Hashes) = %d, want %d", len(gotHashes), want)
	}
	if gotHashes[len(gotHashes)-1] != parent.Hash() {
		t.Errorf("last Hashes entry = %x, want parent hash %x", gotHashes[len(gotHashes)-1], parent.Hash())
	}
}

// TestUpdateSkipListOnMap_CreatedSkipListHasNoFirstLedgerSequence pins the
// other half of rippled parity: when updateSkipList creates the SLE from
// scratch it sets only Hashes and LastLedgerSequence, so a node built from a
// fresh genesis must not synthesize a FirstLedgerSequence the reference never
// writes.
func TestUpdateSkipListOnMap_CreatedSkipListHasNoFirstLedgerSequence(t *testing.T) {
	child := walkToSeq(t, 5)
	fields, _, _, err := readLedgerHashesSLE(child.stateMap, keylet.LedgerHashes().Key)
	if err != nil {
		t.Fatalf("readLedgerHashesSLE: %v", err)
	}
	if fields == nil {
		t.Fatalf("rolling LedgerHashes SLE absent at seq 5")
	}
	if _, ok := fields["FirstLedgerSequence"]; ok {
		t.Errorf("freshly created skip list has FirstLedgerSequence; rippled does not set it on creation")
	}
}

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

package replaytool

import (
	"encoding/hex"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// TestReplayClose_CheckpointSeedBoundary closes the first block of a replay
// range whose state is seeded from a checkpoint — a parent ledger's fully
// closed state, loaded straight into a fresh open ledger via NewOpenWithHeader,
// exactly as the replay tooling does. Close() owns the LedgerHashes skip-list
// update, so closing this block must advance the rolling list by exactly one
// entry (the parent hash), not abort.
//
// Regression for issue #997: replay used to run its own updateSkipList pass
// before Close(), double-appending the parent hash. The second append (inside
// Close) then saw LastLedgerSequence already at prevIndex and tripped the
// issue #470 consistency assertion, wedging the range at its first block.
func TestReplayClose_CheckpointSeedBoundary(t *testing.T) {
	parent := walkGenesisTo(t, 6)

	// Seed the replay from the parent's closed state, mirroring the checkpoint
	// loader: a fresh open ledger built from the snapshot via NewOpenWithHeader
	// (not NewOpen), then closed.
	seed, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatalf("StateMapSnapshot: %v", err)
	}
	block := parent.Sequence() + 1
	hdr := seedBlockHeader(parent)
	open := ledger.NewOpenWithHeader(hdr, seed, shamap.New(shamap.TypeTransaction), drops.Fees{})

	if err := open.Close(hdr.CloseTime, 0); err != nil {
		t.Fatalf("closing first replayed block %d after checkpoint seed: %v", block, err)
	}

	// The rolling LedgerHashes must have advanced by exactly one append: the
	// parent hash, stamped at LastLedgerSequence = parent.seq (= block-1).
	lastSeq, hashes := decodeRollingLedgerHashes(t, open)
	if want := block - 1; lastSeq != want {
		t.Errorf("LastLedgerSequence = %d, want %d", lastSeq, want)
	}
	if want := int(block - 1); len(hashes) != want {
		t.Errorf("len(Hashes) = %d, want %d (single append, no duplicate)", len(hashes), want)
	}

	parentHash := parent.Hash()
	if got := hashes[len(hashes)-1]; got != parentHash {
		t.Errorf("last Hashes entry = %x, want parent hash %x", got, parentHash)
	}
	// Direct guard against the double-append: the parent hash appears once.
	count := 0
	for _, h := range hashes {
		if h == parentHash {
			count++
		}
	}
	if count != 1 {
		t.Errorf("parent hash appears %d times in rolling skip list, want 1", count)
	}
}

// TestReplayClose_DoubleSkipListUpdateRejected reproduces the exact failure
// from issue #997: running a skip-list update before Close() (as the replay
// tooling used to) double-appends the parent hash, so Close()'s own update
// finds LastLedgerSequence already at prevIndex and aborts. This pins why the
// pre-Close pass had to be removed — Close() must be the sole updater.
func TestReplayClose_DoubleSkipListUpdateRejected(t *testing.T) {
	parent := walkGenesisTo(t, 6)

	seed, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatalf("StateMapSnapshot: %v", err)
	}
	hdr := seedBlockHeader(parent)

	// Simulate the removed pre-Close skip-list pass: one update advances the
	// seed's rolling list to LastLedgerSequence = prevIndex.
	if err := ledger.UpdateSkipListOnMap(seed, hdr.LedgerIndex, hdr.ParentHash); err != nil {
		t.Fatalf("first (pre-Close) skip-list update: %v", err)
	}

	// Close() runs the update a second time and must reject the now-ahead state.
	open := ledger.NewOpenWithHeader(hdr, seed, shamap.New(shamap.TypeTransaction), drops.Fees{})
	if err := open.Close(hdr.CloseTime, 0); err == nil {
		t.Fatal("Close after a double skip-list update: want error, got nil")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

// walkGenesisTo builds a genesis ledger and advances it to sequence target via
// the consensus construction (NewOpen + Close), returning the closed parent.
// Its rolling LedgerHashes SLE is then populated (LastLedgerSequence =
// target-1, target-1 hashes), as a real checkpoint seed would be.
func walkGenesisTo(t *testing.T, target uint32) *ledger.Ledger {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := ledger.FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
	for parent.Sequence() < target {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		child, err := ledger.NewOpen(parent, closeTime)
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

// seedBlockHeader builds the header for the first replayed block on top of
// parent, mirroring replay_range's NewOpenWithHeader construction.
func seedBlockHeader(parent *ledger.Ledger) header.LedgerHeader {
	return header.LedgerHeader{
		LedgerIndex:         parent.Sequence() + 1,
		ParentHash:          parent.Hash(),
		ParentCloseTime:     parent.CloseTime(),
		CloseTime:           parent.CloseTime().Add(10 * time.Second),
		CloseTimeResolution: parent.CloseTimeResolution(),
		Drops:               parent.TotalDrops(),
	}
}

// decodeRollingLedgerHashes reads the rolling-256 LedgerHashes SLE from l and
// returns its (LastLedgerSequence, Hashes). Returns (0, nil) when absent.
func decodeRollingLedgerHashes(t *testing.T, l *ledger.Ledger) (uint32, [][32]byte) {
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
		t.Fatalf("LastLedgerSequence type %T", decoded["LastLedgerSequence"])
	}

	var rawHashes []string
	switch v := decoded["Hashes"].(type) {
	case []string:
		rawHashes = v
	case []any:
		for _, h := range v {
			rawHashes = append(rawHashes, h.(string))
		}
	default:
		t.Fatalf("Hashes type %T", decoded["Hashes"])
	}

	hashes := make([][32]byte, 0, len(rawHashes))
	for _, h := range rawHashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatalf("decode hash %q: %v", h, err)
		}
		var arr [32]byte
		copy(arr[:], b)
		hashes = append(hashes, arr)
	}
	return lastSeq, hashes
}

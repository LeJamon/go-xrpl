package service

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStubLedger constructs a Ledger with an explicit seq/hash/parentHash
// triple, backed by an empty state and tx map. The hash does not need to
// match the canonical computed hash for this test — we exercise the
// parent-hash chain check, which consumes the *stored* header values.
func makeStubLedger(t *testing.T, seq uint32, hash, parentHash [32]byte) *ledger.Ledger {
	t.Helper()
	stateMap, stateRoot := makeStubStateMap(t, seq)
	txMap := shamap.New(shamap.TypeTransaction)
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	hdr := header.LedgerHeader{
		LedgerIndex: seq,
		Hash:        hash,
		ParentHash:  parentHash,
		AccountHash: stateRoot,
		TxHash:      txRoot,
		Validated:   true,
	}
	l, err := ledger.NewFromHeader(hdr, stateMap, txMap, drops.Fees{})
	if err != nil {
		t.Fatalf("NewFromHeader: %v", err)
	}
	return l
}

func makeStubStateMap(t *testing.T, seq uint32) (*shamap.SHAMap, [32]byte) {
	t.Helper()
	stateMap := shamap.New(shamap.TypeState)
	key := [32]byte{0x53, byte(seq), byte(seq >> 8), byte(seq >> 16), byte(seq >> 24)}
	require.NoError(t, stateMap.Put(key, []byte("stub-state-data")))
	root, err := stateMap.Hash()
	require.NoError(t, err)
	return stateMap, root
}

func TestSwitchToPreferredLedger_FixMismatchInvalidatesDivergedTail(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB, hashC [32]byte
	hashA[0] = 0xAA
	hashB[0] = 0xBB
	hashC[0] = 0xCC

	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA)
	ledC := makeStubLedger(t, baseSeq+2, hashC, hashB)

	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.ledgerHistory[ledC.Sequence()] = ledC
	svc.closedLedger = ledC
	svc.mu.Unlock()
	seedCompleteLedgers(t, svc, baseSeq, baseSeq+2)

	var hashD [32]byte
	hashD[0] = 0xDD
	var divergentParent [32]byte
	divergentParent[0] = 0xFF
	preferred := makeStubLedger(t, baseSeq+1, hashD, divergentParent)
	require.NoError(t, svc.SwitchToPreferredLedger(preferred))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	gotD, okD := svc.ledgerHistory[baseSeq+1]
	require.True(t, okD, "preferred ledger D must be installed at its seq")
	assert.Equal(t, hashD, gotD.Hash(), "seq S+1 must now hold D, not the purged B")

	_, okC := svc.ledgerHistory[baseSeq+2]
	assert.False(t, okC, "orphaned forward ledger C must be purged by fixMismatch")

	require.NotNil(t, svc.closedLedger)
	assert.Equal(t, hashD, svc.closedLedger.Hash(),
		"closedLedger must track the preferred ledger after a fork switch")
	assert.Equal(t, strconv.FormatUint(uint64(baseSeq+1), 10), svc.completeLedgersString(),
		"fork invalidation must retain only the preferred ledger in complete_ledgers")
}

func TestSwitchToPreferredLedger_NoMismatchNoOp(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB [32]byte
	hashA[0] = 0xA1
	hashB[0] = 0xB1
	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA)

	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.closedLedger = ledB
	svc.mu.Unlock()

	var hashD [32]byte
	hashD[0] = 0xD1

	preferred := makeStubLedger(t, baseSeq+2, hashD, hashB)
	require.NoError(t, svc.SwitchToPreferredLedger(preferred))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	gotA, okA := svc.ledgerHistory[baseSeq]
	require.True(t, okA, "A must remain when the preferred ledger does not invalidate history")
	assert.Equal(t, hashA, gotA.Hash())

	gotB, okB := svc.ledgerHistory[baseSeq+1]
	require.True(t, okB, "B must remain: its hash matches D.parentHash")
	assert.Equal(t, hashB, gotB.Hash())

	gotD, okD := svc.ledgerHistory[baseSeq+2]
	require.True(t, okD)
	assert.Equal(t, hashD, gotD.Hash())
}

func TestSwitchToPreferredLedger_FixMismatchValidatedLedgerInvalidationLogsError(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashB [32]byte
	hashB[0] = 0x42
	var zero [32]byte
	ledB := makeStubLedger(t, baseSeq, hashB, zero)
	require.True(t, ledB.IsValidated(),
		"stub ledger via NewFromHeader must be in validated state")

	svc.mu.Lock()
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.closedLedger = ledB
	prevValidated := svc.validatedLedger
	svc.mu.Unlock()

	var divergentParent [32]byte
	divergentParent[0] = 0xAB
	var hashD [32]byte
	hashD[0] = 0xDD
	preferred := makeStubLedger(t, baseSeq+1, hashD, divergentParent)
	require.NoError(t, svc.SwitchToPreferredLedger(preferred))

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Equal(t, prevValidated, svc.validatedLedger,
		"validatedLedger must NOT be silently reset by fixMismatch — "+
			"a validated-ledger invalidation is an operational alert and "+
			"requires operator action, not silent rewrite")
}

func TestSwitchToPreferredLedger_FixMismatchPurgesTxIndex(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	baseSeq := svc.GetClosedLedgerIndex() + 1

	var hashA, hashB, hashC [32]byte
	hashA[0] = 0x1A
	hashB[0] = 0x1B
	hashC[0] = 0x1C
	var zero [32]byte
	ledA := makeStubLedger(t, baseSeq, hashA, zero)
	ledB := makeStubLedger(t, baseSeq+1, hashB, hashA)
	ledC := makeStubLedger(t, baseSeq+2, hashC, hashB)

	var txInB, txInC [32]byte
	txInB[0] = 0x0B
	txInC[0] = 0x0C

	svc.mu.Lock()
	svc.ledgerHistory[ledA.Sequence()] = ledA
	svc.ledgerHistory[ledB.Sequence()] = ledB
	svc.ledgerHistory[ledC.Sequence()] = ledC
	svc.closedLedger = ledC
	svc.txIndex[txInB] = ledB.Sequence()
	svc.txPositionIndex[txInB] = 0
	svc.txIndex[txInC] = ledC.Sequence()
	svc.txPositionIndex[txInC] = 0
	svc.mu.Unlock()

	var divergentParent [32]byte
	divergentParent[0] = 0xEE
	var hashD [32]byte
	hashD[0] = 0xDD
	preferred := makeStubLedger(t, baseSeq+1, hashD, divergentParent)
	require.NoError(t, svc.SwitchToPreferredLedger(preferred))

	svc.mu.RLock()
	defer svc.mu.RUnlock()

	_, okC := svc.txIndex[txInC]
	assert.False(t, okC, "tx-index must drop entries for orphaned forward ledgers")
	_, okCPos := svc.txPositionIndex[txInC]
	assert.False(t, okCPos, "tx-position-index must drop entries for orphaned forward ledgers")

	_, okB := svc.txIndex[txInB]
	assert.False(t, okB, "tx-index must drop entries for a prev-seq ledger that mismatched the preferred parent")
}

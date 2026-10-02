package adaptor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const localReplayBudgetStateEntries = 5000

func makeBudgetReplayService(t *testing.T) (*service.Service, *backend.NodeStore) {
	t.Helper()
	db, err := nodestore.NewKVDatabase(memorydb.New(), nodestore.DatabaseConfig{})
	require.NoError(t, err)
	rm, err := sqlitedb.NewRepositoryManager(t.Context(), t.TempDir(), sqlitedb.Settings{})
	require.NoError(t, err)
	family := backend.New(db)
	svc, err := service.New(service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, rm.Close())
		require.NoError(t, db.Close())
	})
	return svc, family
}

func buildBackedLargeReplayTarget(t *testing.T, parent *ledger.Ledger) standardReplayTestLink {
	t.Helper()
	response, child, _, seq := buildSuccessorAgainstParent(t, parent)
	stateMap, err := child.StateMapSnapshot()
	require.NoError(t, err)
	for i := 1; i <= localReplayBudgetStateEntries; i++ {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i))
		value := make([]byte, 32)
		binary.BigEndian.PutUint32(value, uint32(i))
		require.NoError(t, stateMap.Put(key, value))
	}
	wireNodes, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	require.Greater(t, len(wireNodes), 4096)
	txMap, err := child.TxMapSnapshot()
	require.NoError(t, err)
	h := child.Header()
	h.AccountHash, err = stateMap.Hash()
	require.NoError(t, err)
	h.Hash = header.CalculateHash(h)
	child, err = ledger.NewFromHeader(h, stateMap, txMap, parent.Fees())
	require.NoError(t, err)
	response.LedgerHash = h.Hash[:]
	response.LedgerHeader = header.AddRaw(h, false)
	return standardReplayTestLink{response: response, ledger: child, hash: h.Hash, seq: seq}
}

func clearReplayPeers(c *catchupReplayCoordinator) {
	c.peersMu.Lock()
	clear(c.peerStates)
	c.peersMu.Unlock()
}

func TestLocalReplayReplacementProgressesAcrossVerificationBudgets(t *testing.T) {
	for _, withPeer := range []bool{false, true} {
		name := "without peers"
		if withPeer {
			name = "with peer"
		}
		t.Run(name, func(t *testing.T) {
			svc, family := makeBudgetReplayService(t)
			_, err := svc.AcceptLedger(t.Context())
			require.NoError(t, err)
			closed := svc.GetClosedLedger()
			require.NotNil(t, closed)

			target := buildBackedLargeReplayTarget(t, closed)
			stateMap, err := target.ledger.StateMapSnapshot()
			require.NoError(t, err)
			txMap, err := target.ledger.TxMapSnapshot()
			require.NoError(t, err)
			h := target.ledger.Header()
			require.NoError(t, svc.StoreLedgerWithState(t.Context(), &h, stateMap, txMap))
			svc.FlushPersists()
			family.FullBelowCache().Bump()

			a, sender := newRecordingAdaptor(t, svc)
			r := newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 8))
			c := r.catchupReplay
			c.standardReplay = standardReplayPipeline{
				generation: 1,
				active:     true,
				pivotReady: true,
				pivotSeq:   closed.Sequence(),
				pivotHash:  closed.Hash(),
				anchorSeq:  closed.Sequence(),
				anchorHash: closed.Hash(),
				targetSeq:  target.seq,
				targetHash: target.hash,
				entries:    make(map[uint32]*standardReplayEntry),
			}
			clearReplayPeers(c)
			if withPeer {
				trackCatchupPeer(r, 7, target.seq, target.hash)
			}

			require.True(t, c.reserveStandardReplayReplacement(1, target.seq, target.hash, 0, time.Now()))
			require.NotNil(t, c.standardReplay.replacement,
				"the first 4096-node verification slice must leave the replacement pending")
			assert.Empty(t, sender.legacyCalls())

			for attempt := 0; c.standardReplay.replacement != nil && attempt < 10; attempt++ {
				c.retryStandardReplayReplacement(time.Now())
			}
			require.Nil(t, c.standardReplay.replacement)
			assert.Equal(t, target.hash, c.standardReplay.anchorHash)
			assert.False(t, c.standardReplay.active)
			assert.Empty(t, sender.legacyCalls())
		})
	}
}

func TestLocalReplayPendingPivotProgressesAcrossVerificationBudgets(t *testing.T) {
	svc, family := makeBudgetReplayService(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	target := buildBackedLargeReplayTarget(t, svc.GetClosedLedger())
	storeRecoveryLedger(t, svc, target.ledger)
	svc.FlushPersists()
	family.FullBelowCache().Bump()

	a, sender := newRecordingAdaptor(t, svc)
	r := newTestRouter(nil, a, make(chan *peermanagement.InboundMessage, 8))
	c := r.catchupReplay
	require.False(t, c.locallySatisfiesLedger(target.seq, target.hash))
	complete := false
	for attempt := 0; !complete && attempt < 10; attempt++ {
		complete = c.locallySatisfiesLedger(target.seq, target.hash)
	}
	require.True(t, complete)
	assert.Empty(t, sender.legacyCalls())
}

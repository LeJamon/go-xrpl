package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type issue1676BlockingFamily struct {
	base    shamap.Family
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *issue1676BlockingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return f.base.Fetch(ctx, hash)
	}
}

func (f *issue1676BlockingFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func TestBlockedInboundTraversalDoesNotStallValidationOrConsensusTick(t *testing.T) {
	source := newWideWorkSource(t, 16)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)

	baseFamily := backend.NewMemory()
	pack, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(pack))
	for _, node := range pack {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}
	require.NoError(t, baseFamily.StoreBatch(t.Context(), entries))

	family := &issue1676BlockingFamily{
		base:    baseFamily,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(family.release) }) })

	const seq = uint32(100)
	hdr := header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash}
	headerData := header.AddRaw(hdr, false)
	candidateHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	candidate := inbound.New(
		candidateHash,
		seq,
		7,
		serveTestLogger(),
		inbound.WithFamily(family),
	)

	adaptor := newTestAdaptor(t)
	engineConfig := rcl.DefaultConfig()
	engineConfig.ManualTick = true
	engine := rcl.NewEngine(adaptor, engineConfig)
	router := newTestRouter(engine, adaptor, nil)
	router.fetchTracker.Track(candidate)
	baseReleased := make(chan struct{})
	router.standardReplay = standardReplayPipeline{
		active:      true,
		pivotSeq:    seq,
		pivotHash:   candidateHash,
		anchorSeq:   seq,
		anchorHash:  candidateHash,
		targetSeq:   seq,
		targetHash:  candidateHash,
		entries:     make(map[uint32]*standardReplayEntry),
		baseLedger:  candidate,
		baseRelease: func() { close(baseReleased) },
	}
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })

	baseDone := make(chan error, 1)
	go func() {
		baseDone <- candidate.GotBaseContext(t.Context(), []message.LedgerNode{
			{NodeData: headerData},
			{NodeData: rootData},
		})
	}()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("inbound completeness walk did not reach the blocking store")
	}

	nodeID, err := adaptor.GetValidatorKey()
	require.NoError(t, err)
	now := adaptor.Now()
	validationDone := make(chan error, 1)
	go func() {
		_, processErr := engine.ProcessVerifiedValidation(&consensus.Validation{
			LedgerSeq: seq,
			LedgerID:  consensus.LedgerID{0xA7},
			NodeID:    nodeID,
			SignTime:  now,
			SeenTime:  now,
			Full:      true,
		}, consensus.ValidationOrigin{PeerID: 8})
		validationDone <- processErr
	}()

	select {
	case processErr := <-validationDone:
		require.NoError(t, processErr)
	case <-time.After(time.Second):
		t.Fatal("trusted validation waited for the inbound completeness walk")
	}
	require.Nil(t, router.fetchTracker.Find(candidateHash))
	select {
	case <-baseReleased:
		t.Fatal("checkpoint base released while discovery still used it")
	default:
	}

	tickDone := make(chan struct{})
	go func() {
		engine.TimerEntry()
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("consensus tick waited for the inbound completeness walk")
	}

	releaseOnce.Do(func() { close(family.release) })
	require.NoError(t, <-baseDone)
	select {
	case <-baseReleased:
	case <-time.After(time.Second):
		t.Fatal("checkpoint base was not released after discovery stopped")
	}
}

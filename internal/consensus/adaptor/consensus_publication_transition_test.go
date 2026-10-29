package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/require"
)

func TestProductionAdaptorRetainsQuorumConflictDuringTrustTransition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		depth int
	}{
		{name: "same sequence"},
		{name: "child", depth: 1},
		{name: "grandchild", depth: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testProductionAdaptorRetainsQuorumConflict(t, tc.depth)
		})
	}
}

func testProductionAdaptorRetainsQuorumConflict(t *testing.T, depth int) {
	t.Helper()
	a := newTestAdaptor(t)
	t.Cleanup(a.ledgerService.Stop)
	a.SetOnLedgerRequested(func(consensus.LedgerID) error { return nil })
	sender := &acceptValidationSender{emitted: make(chan struct{})}
	a.sender = sender

	nodes := make([]consensus.NodeID, 1, 5)
	nodes[0] = a.identity.NodeID
	keys := make([][33]byte, 1, 5)
	keys[0] = a.identity.MasterKey
	remoteIdentities := make([]*ValidatorIdentity, 0, 4)
	for i := range 4 {
		entropy := make([]byte, 16)
		entropy[0] = byte(i + 1)
		seed, err := addresscodec.EncodeSeed(entropy, secp256k1.Algorithm{})
		require.NoError(t, err)
		identity, err := NewValidatorIdentity(seed)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, identity.Close()) })
		remoteIdentities = append(remoteIdentities, identity)
		nodes = append(nodes, identity.NodeID)
		keys = append(keys, identity.MasterKey)
	}
	a.SetTrustedValidators(nodes, keys)
	require.Equal(t, 4, a.GetQuorum())

	config := rcl.DefaultConfig()
	config.ManualTick = true
	engine := rcl.NewEngine(a, config)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })
	parent, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	validated := a.ledgerService.GetValidatedLedger()
	require.NotNil(t, validated)
	txSet, err := a.BuildTxSet(nil)
	require.NoError(t, err)
	seq := parent.Seq() + 1
	for range depth {
		parent, err = a.BuildLedger(t.Context(), parent, txSet, a.Now(), true, nil)
		require.NoError(t, err)
	}
	require.Equal(t, validated.Hash(), a.ledgerService.GetValidatedLedger().Hash())

	blocked := &blockedBuildAdaptor{
		Adaptor: a, entered: make(chan struct{}), release: make(chan struct{}), completed: make(chan struct{}),
	}
	t.Cleanup(func() {
		blocked.releaseBuild()
		select {
		case <-blocked.completed:
		case <-time.After(5 * time.Second):
			t.Error("ledger build did not finish during cleanup")
		}
	})
	buildResult := make(chan error, 1)
	go func() {
		_, err := blocked.BuildLedger(t.Context(), parent, txSet, a.Now(), true, nil)
		buildResult <- err
	}()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ledger build did not start")
	}

	// The quorum ledger is authenticated but its body has not been acquired.
	canonical := consensus.LedgerID{0xc1}
	for _, identity := range remoteIdentities {
		validation := &consensus.Validation{
			LedgerID: canonical, LedgerSeq: seq, SignTime: a.Now(), SeenTime: a.Now(), Full: true,
		}
		require.NoError(t, identity.SignValidation(validation))
		require.NoError(t, engine.OnValidation(validation, 0))
	}
	_, held := a.ledgerService.GetCachedLedgerByHash([32]byte(canonical))
	require.False(t, held)
	require.Equal(t, validated.Hash(), a.ledgerService.GetValidatedLedger().Hash())

	transitionEntered := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan struct{})
	var releaseOnce sync.Once
	a.mu.Lock()
	onTrustChanged := a.onTrustChanged
	a.onTrustChanged = func(trusted []consensus.NodeID, quorum int) {
		onTrustChanged(trusted, quorum)
		close(transitionEntered)
		<-releaseTransition
	}
	a.mu.Unlock()
	require.NotNil(t, onTrustChanged)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseTransition) })
		select {
		case <-transitionDone:
		case <-time.After(5 * time.Second):
			t.Error("trust transition did not finish during cleanup")
		}
	})
	go func() {
		a.SetTrustedValidators(nodes, keys)
		close(transitionDone)
	}()
	select {
	case <-transitionEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("trust transition did not reach the refreshed tracker")
	}
	require.True(t, a.IsQuorumUnavailable())

	blocked.releaseBuild()
	select {
	case err := <-buildResult:
		require.ErrorIs(t, err, service.ErrConsensusPublicationStale)
	case <-time.After(5 * time.Second):
		t.Fatal("ledger publication waited for the trust transition")
	}
	require.Equal(t, [32]byte(parent.ID()), a.ledgerService.GetClosedLedger().Hash())
	require.Equal(t, validated.Hash(), a.ledgerService.GetValidatedLedger().Hash())
	validation := &consensus.Validation{
		LedgerID: consensus.LedgerID{0xc2}, LedgerSeq: seq, Full: true,
	}
	if depth > 0 {
		validation.LedgerID, validation.LedgerSeq = parent.ID(), parent.Seq()
	}
	err = a.BroadcastValidation(validation)
	require.ErrorIs(t, err, service.ErrConsensusPublicationStale)
	require.Zero(t, a.lastIssuedValidationSeq.Load())
	require.Empty(t, sender.snapshot())
}

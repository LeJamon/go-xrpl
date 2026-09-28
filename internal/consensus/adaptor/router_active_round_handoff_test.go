package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

func TestRouter_ActiveRoundRecoveryDefersPromotionAndHandoff(t *testing.T) {
	svc := newTestLedgerService(t)
	a, _ := newRecordingAdaptor(t, svc)
	a.SetOperatingMode(consensus.OpModeFull)
	now := a.Now()
	cfg := rcl.DefaultConfig()
	cfg.ManualTick = true
	cfg.Clock = func() time.Time { return now }
	e := rcl.NewEngine(a, cfg)
	require.NoError(t, e.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, e.Stop()) })
	r := newTestRouter(e, a, nil)
	parent := svc.GetClosedLedger()
	require.NoError(t, e.StartRound(consensus.RoundID{
		Seq: parent.Sequence() + 1, ParentHash: consensus.LedgerID(parent.Hash()),
	}, true))
	require.Equal(t, consensus.ModeProposing, e.Mode())
	validatedBefore := svc.GetValidatedLedger().Hash()

	child, err := ledger.NewOpen(parent, a.Now())
	require.NoError(t, err)
	require.NoError(t, child.Close(a.Now(), 0))
	nodeID, err := a.GetValidatorKey()
	require.NoError(t, err)
	// Quorum arrives before the locally verified replay result, as it did in
	// the live incident. Use the real tracker and its production callback.
	_, err = e.ProcessVerifiedValidation(&consensus.Validation{
		LedgerSeq: child.Sequence(), LedgerID: consensus.LedgerID(child.Hash()),
		NodeID: nodeID, SignTime: a.Now(), SeenTime: a.Now(), Full: true,
	}, consensus.ValidationOrigin{PeerID: 7})
	require.NoError(t, err)
	_, result := a.recheckFullyValidated(child.Sequence(), child.Hash())
	require.Equal(t, validationRecheckAccepted, result)
	hdr, initial, err := r.storeVerifiedLedger(child)
	require.NoError(t, err)
	require.False(t, initial)

	// Both pipeline and classic replay enter this common completion method.
	require.False(t, r.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, false))
	require.Equal(t, parent.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, validatedBefore, svc.GetValidatedLedger().Hash(), "do not promote the service frontier before the handoff policy permits it")
	require.Equal(t, consensus.ModeProposing, e.Mode())
	require.Equal(t, child.Hash(), r.consensusRecovery.targetHash, "keep the verified result available for retry")
	held, err := svc.GetLedgerByHash(child.Hash())
	require.NoError(t, err)
	require.NotNil(t, held)

	// A round that fails to finish still has a bounded recovery path.
	now = now.Add(cfg.Timing.LedgerMaxConsensus + time.Second)
	require.True(t, r.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, false))
	require.Equal(t, child.Hash(), svc.GetClosedLedger().Hash())
	require.Equal(t, child.Hash(), svc.GetValidatedLedger().Hash())
	require.Equal(t, consensus.ModeSwitchedLedger, e.Mode())
}

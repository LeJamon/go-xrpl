package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/require"
)

type diagnosticPinFamily struct {
	*replayAdmissionFamily
	pins atomic.Int32
}

func (f *diagnosticPinFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	fingerprint, release, err := f.replayAdmissionFamily.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fingerprint, release, err
	}
	f.pins.Add(1)
	return fingerprint, func() { f.pins.Add(-1); release() }, nil
}

func TestReplayDiagnosticParentReusesExactProofAndPinsRetry(t *testing.T) {
	family := &diagnosticPinFamily{replayAdmissionFamily: newReplayAdmissionFamily(t)}
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	source := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 4096)
	root, err := source.Hash()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 100, AccountHash: root, ParentHash: [32]byte{1}}
	h.Hash = header.CalculateHash(h)
	svc.rememberReplayProof(h, family.fingerprint, true)
	family.resetReads()
	parent, release, handled, err := svc.prepareReplayDiagnosticParent(t.Context(), h, drops.Fees{}, true)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotNil(t, parent)
	require.LessOrEqual(t, family.readCount(), 16, "only root/amendment point reads, not a copy of all 4096 leaves")
	require.Equal(t, int32(1), family.pins.Load(), "pin must cover the caller's replay")
	require.Equal(t, h.Hash, parent.Hash())
	release()
	release()
	require.Zero(t, family.pins.Load())

	// A changed durable generation cannot reuse the old certificate.
	family.bumpFingerprint()
	family.resetReads()
	_, release, handled, err = svc.prepareReplayDiagnosticParent(t.Context(), h, drops.Fees{}, true)
	require.NoError(t, err)
	require.True(t, handled)
	release()
	require.Greater(t, family.readCount(), 4000)
	require.Zero(t, family.pins.Load())

	// Corruption/missing-state diagnostics may not reuse completeness proofs.
	svc.rememberReplayProof(h, family.fingerprint, true)
	child := replayAdmissionFirstChildHash(t, family.replayAdmissionFamily, root)
	family.hideWithoutGeneration(child)
	_, release, handled, err = svc.prepareReplayDiagnosticParent(t.Context(), h, drops.Fees{}, false)
	require.Error(t, err)
	require.True(t, handled)
	require.Nil(t, release)
	require.Zero(t, family.pins.Load())

	// Even a proof hit still cold-loads and checks the root.
	family.hideWithoutGeneration(root)
	_, release, _, err = svc.prepareReplayDiagnosticParent(t.Context(), h, drops.Fees{}, true)
	require.Error(t, err)
	require.Nil(t, release)
	require.Zero(t, family.pins.Load())
}

func TestReplayDiagnosticParentRejectsHeaderAndCancellation(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	_, release, handled, err := svc.prepareReplayDiagnosticParent(t.Context(), header.LedgerHeader{Hash: [32]byte{1}}, drops.Fees{}, true)
	require.ErrorIs(t, err, errReplayParentCorrupt)
	require.True(t, handled)
	require.Nil(t, release)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _, err = svc.prepareReplayDiagnosticParent(ctx, header.LedgerHeader{}, drops.Fees{}, true)
	require.ErrorIs(t, err, context.Canceled)
}

package adaptor

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/stretchr/testify/require"
)

func newLivePivotRecoveryFixture(
	t *testing.T,
	validationCount int,
	signTime, trackerTime time.Time,
) (*pivotRecoveryBackend, header.LedgerHeader, *testPeerSessions, *rcl.ValidationTracker, time.Time) {
	t.Helper()
	r, a, _, svc := makeRouter(t)
	a.SetOperatingMode(consensus.OpModeTracking)
	sessions := &testPeerSessions{connected: map[peermanagement.PeerID]bool{7: true}}
	r.setPeerSessionView(sessions)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	h := closed.Header()
	r.catchupReplay.recordValidationCatchupTarget(h.LedgerIndex, h.Hash, 7, catchupSourceQuorum)
	trackCatchupPeer(r, 7, h.LedgerIndex, h.Hash)

	second, err := NewValidatorIdentity("snMKnVku798EnBwUfxeSD8953sLYA")
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	identities := []*ValidatorIdentity{a.identity, second}
	trusted := []consensus.NodeID{identities[0].NodeID, identities[1].NodeID}
	masters := [][33]byte{identities[0].MasterKey, identities[1].MasterKey}
	a.SetTrustedValidators(trusted, masters)

	tracker := rcl.NewValidationTracker(2)
	tracker.SetTrustedAndQuorum(trusted, 2)
	tracker.SetNow(func() time.Time { return trackerTime })
	a.SetValidationHistorian(tracker)
	for i := 0; i < validationCount; i++ {
		validation := &consensus.Validation{
			LedgerID:  consensus.LedgerID(h.Hash),
			LedgerSeq: h.LedgerIndex,
			SignTime:  signTime,
			SeenTime:  signTime,
			Full:      true,
		}
		require.NoError(t, identities[i].SignValidation(validation))
		raw, err := serializeSTValidationChecked(validation)
		require.NoError(t, err)
		parsed, err := parseSTValidation(raw)
		require.NoError(t, err)
		parsed.NodeID = validation.NodeID
		parsed.Raw = append([]byte(nil), raw...)
		parsed.SeenTime = validation.SeenTime
		require.True(t, tracker.Add(parsed))
	}

	now := a.Now()
	return &pivotRecoveryBackend{coordinator: r.catchupReplay}, h, sessions, tracker, now
}

func TestRecoveryCompletionCannotCompleteReplacementOrPublishHistory(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	c := r.catchupReplay
	before := svc.GetClosedLedger().Hash()
	hash := [32]byte{0x71}
	old := inbound.NewRecovery(hash, 100, 7, c.logger)
	replacement, created := c.fetchTracker.GetOrCreateWithSequence(hash, 100, func() *inbound.Ledger {
		return inbound.NewRecovery(hash, 100, 7, c.logger)
	})
	require.True(t, created)
	job := &pivotRecoveryAcquisition{ledger: replacement, done: make(chan recoveryAcquisitionResult, 1)}
	c.pivotRecovery = job
	c.completeInboundLedgerReady(old)
	require.Empty(t, job.done)
	require.Same(t, replacement, c.fetchTracker.Find(hash))
	require.Equal(t, before, svc.GetClosedLedger().Hash())
	cause := errors.New("unavailable recovery state")
	c.failPivotRecoveryAcquisition(replacement, cause)
	result := <-job.done
	require.ErrorIs(t, result.err, cause)
	require.Nil(t, c.fetchTracker.Find(hash))
	require.Equal(t, before, svc.GetClosedLedger().Hash())
}

func TestRecoveryAuthenticationRejectsBrokenAncestryBeforeQuorum(t *testing.T) {
	r, _, _, _ := makeRouter(t)
	backend := &pivotRecoveryBackend{coordinator: r.catchupReplay}
	parent := header.LedgerHeader{LedgerIndex: 10}
	parent.Hash = header.CalculateHash(parent)
	next := header.LedgerHeader{LedgerIndex: 11, ParentHash: parent.Hash}
	next.Hash = header.CalculateHash(next)
	for _, test := range []struct {
		name     string
		ancestry []header.LedgerHeader
	}{
		{"empty", nil},
		{"wrong target", []header.LedgerHeader{next}},
		{"duplicate sequence", []header.LedgerHeader{parent, parent}},
		{"invalid hash", []header.LedgerHeader{parent, {LedgerIndex: 11, ParentHash: parent.Hash}}},
		{"reversed", []header.LedgerHeader{parent, next, parent}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, err := recoveryTarget(parent, []byte(`{}`), test.ancestry)
			require.NoError(t, err)
			require.Error(t, backend.Authenticate(context.Background(), target))
		})
	}
}

func TestRecoveryAcquisitionDoesNotReuseCachedDurabilityOrCompleteness(t *testing.T) {
	base := newAcquisitionStoreTestFamily()
	hash := [32]byte{42}
	base.cached[hash] = []byte("warm cache only")
	lane := newAcquisitionStoreLane(base, slog.Default(), 1)
	family := &recoveryAcquisitionFamily{lane.scope().(*acquisitionStoreScope)}
	require.Nil(t, family.FullBelowCache())
	data, err := family.Fetch(t.Context(), hash)
	require.NoError(t, err)
	require.Nil(t, data)
	data, err = family.FetchDurable(t.Context(), hash)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Zero(t, base.cacheCalls)
	base.data[hash] = []byte("durable bytes")
	data, err = family.Fetch(t.Context(), hash)
	require.NoError(t, err)
	require.Equal(t, base.data[hash], data)
}

func TestPivotRecoveryCaughtUpRequiresCurrentConnectedQuorum(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name            string
		validationCount int
		signTime        time.Time
		trackerTime     time.Time
		disconnect      bool
		want            bool
	}{
		{
			name:            "current connected quorum",
			validationCount: 2,
			signTime:        now,
			trackerTime:     now,
			want:            true,
		},
		{
			name:            "below quorum",
			validationCount: 1,
			signTime:        now,
			trackerTime:     now,
			want:            false,
		},
		{
			name:            "stale signed quorum",
			validationCount: 2,
			signTime:        now.Add(-4 * time.Minute),
			trackerTime:     now.Add(-4 * time.Minute),
			want:            false,
		},
		{
			name:            "disconnected source peer",
			validationCount: 2,
			signTime:        now,
			trackerTime:     now,
			disconnect:      true,
			want:            false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend, target, sessions, tracker, current := newLivePivotRecoveryFixture(
				t, test.validationCount, test.signTime, test.trackerTime,
			)
			if test.name == "stale signed quorum" {
				tracker.SetNow(func() time.Time { return current })
			}
			if test.disconnect {
				sessions.set(7, false)
			}
			require.Equal(t, test.want, backend.CaughtUp(context.Background(), target))
		})
	}
}

func TestPivotRecoveryHandoffRechecksLiveEligibilityAfterSwitch(t *testing.T) {
	now := time.Now().UTC()
	backend, target, sessions, _, _ := newLivePivotRecoveryFixture(t, 2, now, now)
	engine := &mockEngine{
		switchResult: consensus.LedgerSwitchAccepted,
		switchHook: func(consensus.LedgerID) {
			sessions.set(7, false)
		},
	}
	backend.coordinator.engine = engine

	err := backend.Handoff(context.Background(), target)
	require.Error(t, err)
	require.Len(t, engine.getLedgers(), 1)
}

func TestPivotRecoveryRestoresMonotonicProgress(t *testing.T) {
	backend := &pivotRecoveryBackend{}
	backend.RestoreRecoveryProgress(9, 5, 4)
	backend.RestoreRecoveryProgress(3, 8, 2)
	require.Equal(t, uint64(9), backend.reused)
	require.Equal(t, uint64(8), backend.fetched)
	require.Equal(t, uint64(4), backend.missing)
}

func TestCaptureReplayEvidenceAuthenticatesDirectQuorumProof(t *testing.T) {
	now := time.Now().UTC()
	backend, target, _, _, _ := newLivePivotRecoveryFixture(t, 2, now, now)
	raw, err := backend.CaptureReplayEvidence(target)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.NoError(t, backend.Authenticate(context.Background(), service.RecoveryTarget{Header: target, Evidence: raw}))
}

func TestRecoveryAuthenticationRetainsRouteDuringTrustUnavailability(t *testing.T) {
	now := time.Now().UTC()
	backend, target, _, _, _ := newLivePivotRecoveryFixture(t, 2, now, now)
	raw, err := backend.CaptureReplayEvidence(target)
	require.NoError(t, err)
	backend.coordinator.adaptor.SetQuorumUnavailableFunc(func() bool { return true })
	err = backend.Authenticate(context.Background(), service.RecoveryTarget{Header: target, Evidence: raw})
	require.ErrorIs(t, err, service.ErrRecoveryUnavailable)
	require.ErrorIs(t, err, ErrRecoveryProofUnavailable)
	backend.coordinator.adaptor.SetQuorumUnavailableFunc(func() bool { return false })
	require.NoError(t, backend.Authenticate(context.Background(), service.RecoveryTarget{Header: target, Evidence: raw}))
}

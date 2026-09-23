package adaptor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/stretchr/testify/require"
)

func TestHeaderDiscoveryPreservesVerifiedReplayPipeline(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	require.True(t, r.standardReplay.active)
	require.True(t, r.standardReplay.pivotReady)
	generation := r.standardReplay.generation

	startTestHeaderDiscovery(t, r, base.Sequence(), links[len(links)-1], 7, catchupSourceQuorum)
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)
	require.NotNil(t, r.fetchTracker.Find(links[0].hash))
	require.NotNil(t, r.fetchTracker.Find(links[1].hash))

	for i := len(links) - 1; i >= 0; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
	}
	require.Nil(t, r.headerDiscovery)
	require.True(t, r.standardReplay.active)
	require.Equal(t, generation, r.standardReplay.generation)
	require.NotNil(t, r.fetchTracker.Find(links[0].hash))
	require.NotNil(t, r.fetchTracker.Find(links[1].hash))
}

func TestHeaderDiscoveryRetiresContradictoryVerifiedReplayPipeline(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	require.True(t, r.standardReplay.active)

	contradictory := buildAlternativeReplaySuccessor(t, base, 2*time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), contradictory, 7, catchupSourceQuorum)
	require.False(t, r.standardReplay.active)
	for _, link := range links {
		require.Nil(t, r.fetchTracker.Find(link.hash))
	}
}

func TestStandardReplayBaseConsumesStoredPrefixAndKeepsPreparedSuffix(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	completeStandardReplayTestLink(t, r, links[2])
	completeStandardReplayTestLink(t, r, links[1])
	storeRecoveryLedger(t, svc, links[0].ledger)

	_, identity, current := r.standardReplayBase(svc, base, links[2].seq, links[2].hash)
	require.True(t, current)
	require.True(t, identity.active)
	require.Equal(t, links[0].seq, identity.anchorSeq)
	require.Equal(t, links[0].hash, identity.anchorHash)
	require.Nil(t, r.standardReplay.entries[links[0].seq])
	require.NotNil(t, r.standardReplay.entries[links[1].seq])
	require.NotNil(t, r.standardReplay.entries[links[2].seq])
	require.Nil(t, r.fetchTracker.Find(links[0].hash))
	require.True(t, r.standardReplay.applying)
	require.Len(t, r.standardReplayDrainWake, 1)
}

func TestHeaderDiscoveryKeepsPreparedTailWhenTrustedTargetFallsBehind(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 5)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	oldTarget := links[len(links)-1]

	startTestHeaderDiscovery(t, r, base.Sequence(), links[2], 7, catchupSourceQuorum)
	for i := 2; i >= 0; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
	}

	require.True(t, r.standardReplay.active)
	require.Equal(t, oldTarget.seq, r.standardReplay.targetSeq)
	require.Equal(t, oldTarget.hash, r.standardReplay.targetHash)
	require.NotNil(t, r.standardReplay.entries[links[3].seq])
	require.NotNil(t, r.standardReplay.entries[links[4].seq])
}

func TestHeaderDiscoveryKeepsTailBeyondPreparedWindowWhenTargetFallsBehind(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, standardReplayPipelineWindow)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	// The active target is beyond the resident window. Header discovery proves
	// only a lower target, so entries after that target must remain the next
	// verified frontier rather than being cleared when the lower target drains.
	r.acquisitionMu.Lock()
	r.standardReplay.targetSeq = base.Sequence() + 100
	r.standardReplay.targetHash = [32]byte{0xa1}
	r.acquisitionMu.Unlock()

	startTestHeaderDiscovery(t, r, base.Sequence(), links[4], 7, catchupSourceQuorum)
	for i := 4; i >= 0; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
	}

	require.True(t, r.standardReplay.active)
	require.Equal(t, links[standardReplayPipelineWindow-1].seq, r.standardReplay.targetSeq)
	require.Equal(t, links[standardReplayPipelineWindow-1].hash, r.standardReplay.targetHash)
	for i := 5; i < len(links); i++ {
		require.NotNil(t, r.standardReplay.entries[links[i].seq])
	}
}

func TestHeaderDiscoverySuffixRetirementLogsIdentity(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	var logs bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&logs, nil))
	r.acquisitionMu.Lock()
	r.standardReplay.entries[links[1].seq].parentHash = [32]byte{0xff}
	generation := r.standardReplay.generation
	r.acquisitionMu.Unlock()

	r.reconcileStandardReplayAfterHeaderDiscovery(
		base.Sequence(),
		base.Hash(),
		links[0].seq,
		links[0].hash,
		[]header.LedgerHeader{links[0].ledger.Header()},
	)

	require.True(t, r.standardReplay.active)
	require.Equal(t, links[0].seq, r.standardReplay.targetSeq)
	require.Nil(t, r.standardReplay.entries[links[1].seq])
	require.Nil(t, r.standardReplay.entries[links[2].seq])
	require.Contains(t, logs.String(), "reason=header_discovery_conflict")
	require.Contains(t, logs.String(), "generation="+fmt.Sprint(generation))
	require.Contains(t, logs.String(), "discarded_entries=2")
}

func TestContinueFrozenPivotRecoveryKeepsPreparedTailForLowerTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	r.catchupMu.Lock()
	r.catchup = catchupTarget{
		seq:    links[0].seq,
		hash:   links[0].hash,
		peerID: 7,
		source: catchupSourceQuorum,
	}
	r.catchupMu.Unlock()
	require.True(t, r.continueFrozenPivotRecovery(links[0].seq, links[0].hash, 7))
	require.True(t, r.standardReplay.active)
	require.NotNil(t, r.standardReplay.entries[links[1].seq])
	require.NotNil(t, r.standardReplay.entries[links[2].seq])
}

func TestStandardReplayCancellationLogsIdentity(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	links := buildStandardReplayTestChain(t, r, svc.GetClosedLedger(), 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)

	var logs bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&logs, nil))
	identity := r.standardReplayIdentityLocked()
	_, current := r.cancelStandardReplayPipelineIdentity(identity, "test_conflict")
	require.True(t, current)
	require.Contains(t, logs.String(), "reason=test_conflict")
	require.Contains(t, logs.String(), "generation=1")
	require.Contains(t, logs.String(), "anchor_seq=")
	require.Contains(t, logs.String(), "target_seq=")
	require.Contains(t, logs.String(), "discarded_entries=3")
}

func TestStandardReplayAnchorAdvanceRejectsLateConflictingLinkAtomically(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 3)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	identity := r.standardReplayIdentityLocked()
	firstAcquisition := r.fetchTracker.Find(links[0].hash)
	require.NotNil(t, firstAcquisition)
	contradictory := buildAlternativeReplaySuccessor(t, links[0].ledger, 2*time.Second)

	updated, retirement, current := r.advanceStandardReplayAnchor(identity, []standardReplayLink{
		{seq: links[0].seq, hash: links[0].hash, parentHash: base.Hash()},
		{seq: contradictory.seq, hash: contradictory.hash, parentHash: links[0].hash},
	}, contradictory.ledger)
	require.False(t, current)
	require.Equal(t, identity, updated)
	require.Empty(t, retirement.ledgers)
	require.Equal(t, base.Sequence(), r.standardReplay.anchorSeq)
	require.Equal(t, base.Hash(), r.standardReplay.anchorHash)
	require.Same(t, firstAcquisition, r.fetchTracker.Find(links[0].hash))
	require.NotNil(t, r.standardReplay.entries[links[0].seq])
}

func TestHeaderDiscoveryPreservesPipelineAlreadyPastFrozenTarget(t *testing.T) {
	r, a, sender, svc := makeRouter(t)
	_, err := svc.AcceptLedger(t.Context())
	require.NoError(t, err)
	base := svc.GetClosedLedger()
	links := buildStandardReplayTestChain(t, r, base, 5)
	armStandardReplayTestPipeline(t, r, a, sender, links)
	startTestHeaderDiscovery(t, r, base.Sequence(), links[2], 7, catchupSourceQuorum)

	for i := 0; i < 4; i++ {
		storeRecoveryLedger(t, svc, links[i].ledger)
	}
	_, identity, current := r.standardReplayBase(svc, base, links[4].seq, links[4].hash)
	require.True(t, current)
	require.True(t, identity.active)
	require.Equal(t, links[3].seq, identity.anchorSeq)

	for i := 2; i >= 0; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
	}
	require.True(t, r.standardReplay.active)
	require.Equal(t, links[3].seq, r.standardReplay.anchorSeq)
	require.NotNil(t, r.standardReplay.entries[links[4].seq])
}

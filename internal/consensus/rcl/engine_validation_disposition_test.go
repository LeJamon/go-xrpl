package rcl

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

type blockingTestSubscriber struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTestSubscriber) OnEvent(event consensus.Event) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
}

func TestProcessVerifiedValidationDrainsFinalityOutsideEngineLock(t *testing.T) {
	adaptor := newMockAdaptor()
	trusted := consensus.NodeID{0x45}
	adaptor.quorum = 1
	adaptor.setTrusted([]consensus.NodeID{trusted})
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	adaptor.onFullyValidated = func(consensus.LedgerID, uint32) {
		close(entered)
		<-release
	}
	engine := startedEngine(t, adaptor)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	now := adaptor.Now()

	validationDone := make(chan error, 1)
	go func() {
		_, err := engine.ProcessVerifiedValidation(&consensus.Validation{
			LedgerSeq: 101,
			LedgerID:  consensus.LedgerID{0xA1},
			NodeID:    trusted,
			SignTime:  now,
			SeenTime:  now,
			Full:      true,
		}, consensus.ValidationOrigin{PeerID: 7})
		validationDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fully validated callback did not start")
	}

	tickDone := make(chan struct{})
	go func() {
		engine.timerEntry()
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("consensus tick waited for the fully validated callback")
	}

	processingDone := make(chan struct{})
	go func() {
		_, _ = engine.ProcessVerifiedValidation(&consensus.Validation{
			LedgerSeq: 101,
			LedgerID:  consensus.LedgerID{0xA1},
			NodeID:    consensus.NodeID{0x99},
			SignTime:  now,
			SeenTime:  now,
			Full:      true,
		}, consensus.ValidationOrigin{PeerID: 8})
		close(processingDone)
	}()
	select {
	case <-processingDone:
	case <-time.After(time.Second):
		t.Fatal("validation processing waited for the fully validated callback")
	}

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-validationDone)
}

func TestProcessVerifiedValidationPublishesAfterFinality(t *testing.T) {
	adaptor := newMockAdaptor()
	trusted := consensus.NodeID{0x46}
	adaptor.quorum = 1
	adaptor.setTrusted([]consensus.NodeID{trusted})
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	adaptor.onFullyValidated = func(consensus.LedgerID, uint32) {
		close(entered)
		<-release
	}
	engine := NewEngine(adaptor, DefaultConfig())
	engine.eventBus = consensus.NewEventBus(1)
	subscriberRelease := make(chan struct{})
	var subscriberReleaseOnce sync.Once
	subscriber := &blockingTestSubscriber{
		entered: make(chan struct{}),
		release: subscriberRelease,
	}
	engine.Subscribe(subscriber)
	require.NoError(t, engine.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Stop()) })
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	t.Cleanup(func() { subscriberReleaseOnce.Do(func() { close(subscriberRelease) }) })
	require.True(t, engine.eventBus.Publish(&consensus.PhaseChangedEvent{}))
	select {
	case <-subscriber.entered:
	case <-time.After(time.Second):
		t.Fatal("event subscriber did not start")
	}
	require.True(t, engine.eventBus.Publish(&consensus.PhaseChangedEvent{}))
	require.Zero(t, engine.eventBus.DroppedEvents())
	now := adaptor.Now()

	validationDone := make(chan error, 1)
	go func() {
		_, err := engine.ProcessVerifiedValidation(&consensus.Validation{
			LedgerSeq: 102,
			LedgerID:  consensus.LedgerID{0xA2},
			NodeID:    trusted,
			SignTime:  now,
			SeenTime:  now,
			Full:      true,
		}, consensus.ValidationOrigin{PeerID: 7})
		validationDone <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fully validated callback did not start")
	}
	require.Zero(t, engine.eventBus.DroppedEvents(),
		"validation event was published before finality completed")

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-validationDone)
	require.Equal(t, uint64(1), engine.eventBus.DroppedEvents(),
		"validation event was not published after finality completed")
}

func TestStartRoundTrustRefreshDrainsFinalityOutsideEngineLock(t *testing.T) {
	adaptor := newMockAdaptor()
	listed := consensus.NodeID{0x47}
	adaptor.setListed([]consensus.NodeID{listed})
	engine := startedEngine(t, adaptor)
	now := adaptor.Now()
	_, err := engine.ProcessVerifiedValidation(&consensus.Validation{
		LedgerSeq: 103,
		LedgerID:  consensus.LedgerID{0xA3},
		NodeID:    listed,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}, consensus.ValidationOrigin{PeerID: 8})
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	adaptor.onFullyValidated = func(consensus.LedgerID, uint32) {
		close(entered)
		<-release
	}
	adaptor.onRefreshUNL = func() {
		adaptor.mu.Lock()
		adaptor.trusted = map[consensus.NodeID]bool{listed: true}
		adaptor.quorum = 1
		adaptor.mu.Unlock()
		adaptor.notifyTrustChanged()
	}
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	roundDone := make(chan error, 1)
	go func() {
		roundDone <- engine.StartRound(consensus.RoundID{
			Seq:        101,
			ParentHash: consensus.LedgerID{1},
		}, true)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("trust refresh did not promote stored validation")
	}

	tickDone := make(chan struct{})
	go func() {
		engine.timerEntry()
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("consensus tick waited for trust-refresh finality callback")
	}

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-roundDone)
}

func TestSendValidationDrainsFinalityOutsideEngineLock(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.quorum = 1
	adaptor.setTrusted([]consensus.NodeID{adaptor.nodeID})
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	adaptor.onFullyValidated = func(consensus.LedgerID, uint32) {
		close(entered)
		<-release
	}
	engine := startedEngine(t, adaptor)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	engine.mu.Lock()
	engine.setMode(consensus.ModeProposing)
	engine.deferPostUnlock++
	engine.sendValidation(&mockLedger{
		id:        consensus.LedgerID{0xA2},
		seq:       101,
		closeTime: adaptor.Now(),
	})
	engine.deferPostUnlock--
	pending := engine.takePendingPostUnlockLocked()
	engine.mu.Unlock()

	flushDone := make(chan struct{})
	go func() {
		runPostUnlock(pending)
		close(flushDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("self-validation finality callback did not start")
	}

	tickDone := make(chan struct{})
	go func() {
		engine.timerEntry()
		close(tickDone)
	}()
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("consensus tick waited for the self-validation finality callback")
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-flushDone:
	case <-time.After(time.Second):
		t.Fatal("deferred finality work did not finish")
	}
}

func TestProcessVerifiedValidationDisposition(t *testing.T) {
	adaptor := newMockAdaptor()
	trusted := consensus.NodeID{0x44}
	adaptor.setTrusted([]consensus.NodeID{trusted})
	engine := startedEngine(t, adaptor)
	subscriber := &testSubscriber{events: make(chan consensus.Event, 4)}
	engine.Subscribe(subscriber)
	now := adaptor.Now()

	current := &consensus.Validation{
		LedgerSeq: 100,
		LedgerID:  consensus.LedgerID{0xA},
		NodeID:    trusted,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}
	disposition, err := engine.ProcessVerifiedValidation(
		current,
		consensus.ValidationOrigin{PeerID: 7},
	)
	require.NoError(t, err)
	require.Equal(t, consensus.ValidationDisposition{
		Status:  consensus.ValidationCurrent,
		Tracked: true,
		Trusted: true,
		Relay:   true,
	}, disposition)
	require.True(t, disposition.AcquireEligible())
	require.Empty(t, adaptor.relayedValidations(),
		"verified processing must not perform network I/O")

	conflicting := &consensus.Validation{
		LedgerSeq: 100,
		LedgerID:  consensus.LedgerID{0xB},
		NodeID:    trusted,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}
	disposition, err = engine.ProcessVerifiedValidation(
		conflicting,
		consensus.ValidationOrigin{PeerID: 7},
	)
	require.NoError(t, err)
	require.Equal(t, consensus.ValidationConflicting, disposition.Status)
	require.True(t, disposition.Relay)
	require.False(t, disposition.AcquireEligible())
	require.Empty(t, adaptor.relayedValidations(),
		"Byzantine validations are published locally but relayed by the router")

	badSeq := &consensus.Validation{
		LedgerSeq: 99,
		LedgerID:  consensus.LedgerID{0x9},
		NodeID:    trusted,
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}
	disposition, err = engine.ProcessVerifiedValidation(
		badSeq,
		consensus.ValidationOrigin{PeerID: 7},
	)
	require.NoError(t, err)
	require.Equal(t, consensus.ValidationBadSeq, disposition.Status)
	require.True(t, disposition.Relay)
	require.False(t, disposition.AcquireEligible())

	var validationEvents int
	timeout := time.After(time.Second)
	for validationEvents < 3 {
		select {
		case event := <-subscriber.events:
			if _, ok := event.(*consensus.ValidationReceivedEvent); ok {
				validationEvents++
			}
		case <-timeout:
			t.Fatalf("received %d validation events, want 3", validationEvents)
		}
	}
}

func TestProcessVerifiedValidationClusterForcesRelay(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := startedEngine(t, adaptor)
	now := adaptor.Now()
	validation := &consensus.Validation{
		LedgerSeq: 200,
		LedgerID:  consensus.LedgerID{0xC},
		NodeID:    consensus.NodeID{0x55},
		SignTime:  now,
		SeenTime:  now,
		Full:      true,
	}

	disposition, err := engine.ProcessVerifiedValidation(
		validation,
		consensus.ValidationOrigin{PeerID: 8, Cluster: true},
	)
	require.NoError(t, err)
	require.Equal(t, consensus.ValidationUntracked, disposition.Status)
	require.False(t, disposition.Tracked)
	require.False(t, disposition.Trusted)
	require.True(t, disposition.Relay)
	require.False(t, disposition.AcquireEligible())
	require.Empty(t, adaptor.relayedValidations())
}

func TestValidationDispositionStatusMappingAndAcquisitionGate(t *testing.T) {
	tests := []struct {
		input valStatus
		want  consensus.ValidationStatus
	}{
		{ValStatusCurrent, consensus.ValidationCurrent},
		{ValStatusStale, consensus.ValidationStale},
		{ValStatusBadSeq, consensus.ValidationBadSeq},
		{ValStatusMultiple, consensus.ValidationMultiple},
		{ValStatusConflicting, consensus.ValidationConflicting},
	}
	for _, test := range tests {
		got := validationDispositionStatus(test.input)
		require.Equal(t, test.want, got)
		disposition := consensus.ValidationDisposition{
			Status:  got,
			Tracked: true,
			Trusted: true,
		}
		require.Equal(t, got == consensus.ValidationCurrent, disposition.AcquireEligible())
	}

	require.False(t, (consensus.ValidationDisposition{
		Status:  consensus.ValidationCurrent,
		Tracked: true,
	}).AcquireEligible())
	require.False(t, (consensus.ValidationDisposition{
		Status:  consensus.ValidationCurrent,
		Trusted: true,
	}).AcquireEligible())
}

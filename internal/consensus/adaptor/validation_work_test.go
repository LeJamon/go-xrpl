package adaptor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

type validationProcessorEngine struct {
	*mockEngine

	muProcessor sync.Mutex
	disposition consensus.ValidationDisposition
	processed   []*consensus.Validation
	origins     []consensus.ValidationOrigin
}

func (e *validationProcessorEngine) ProcessVerifiedValidation(
	validation *consensus.Validation,
	origin consensus.ValidationOrigin,
) (consensus.ValidationDisposition, error) {
	e.muProcessor.Lock()
	defer e.muProcessor.Unlock()
	e.processed = append(e.processed, validation)
	e.origins = append(e.origins, origin)
	return e.disposition, nil
}

func (e *validationProcessorEngine) processedCount() int {
	e.muProcessor.Lock()
	defer e.muProcessor.Unlock()
	return len(e.processed)
}

type validationCaptureSender struct {
	noopSender

	mu      sync.Mutex
	relayed []*consensus.Validation
	bad     []string
	sources map[[32]byte][]uint64
}

func (s *validationCaptureSender) RelayValidation(
	validation *consensus.Validation,
	_ uint64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relayed = append(s.relayed, validation)
	return nil
}

func (s *validationCaptureSender) IncPeerBadData(_ uint64, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bad = append(s.bad, reason)
}

func (s *validationCaptureSender) RecordMessageSource(hash [32]byte, peerID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sources == nil {
		s.sources = make(map[[32]byte][]uint64)
	}
	s.sources[hash] = append(s.sources[hash], peerID)
}

type validationPeerSessions struct {
	peers []peermanagement.PeerInfo
}

func (s *validationPeerSessions) IsPeerConnected(peermanagement.PeerID) bool {
	return true
}

func (s *validationPeerSessions) Peers() []peermanagement.PeerInfo {
	return append([]peermanagement.PeerInfo(nil), s.peers...)
}

func TestValidationWorkLaneKeepsTrustedVerificationIndependent(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var unblockOnce sync.Once
	release := func() { unblockOnce.Do(func() { close(unblock) }) }
	lane := newValidationWorkLane(func(validation *consensus.Validation) error {
		if validation.LedgerID[0] == 1 {
			close(started)
			<-unblock
		}
		return nil
	}, nil, nil)
	lane.trustedWorkers = 1
	lane.untrustedWorkers = 1
	lane.start(t.Context())
	defer lane.stop()
	defer release()

	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{1}},
	}))
	<-started
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{3}},
		trusted:    true,
	}))
	select {
	case result := <-lane.trustedResultCh:
		require.Equal(t, byte(3), result.validation.LedgerID[0])
	case <-time.After(time.Second):
		t.Fatal("trusted verification was blocked by untrusted verification")
	}
	release()
	select {
	case result := <-lane.untrustedResultCh:
		require.Equal(t, byte(1), result.validation.LedgerID[0])
		result.permit.release()
	case <-time.After(time.Second):
		t.Fatal("untrusted verification did not complete")
	}
}

func TestValidationWorkLaneStopsWithoutClosingProducerChannels(t *testing.T) {
	lane := newValidationWorkLane(func(*consensus.Validation) error { return nil }, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	lane.start(ctx)
	cancel()
	var stops sync.WaitGroup
	stops.Add(2)
	go func() {
		defer stops.Done()
		lane.stop()
	}()
	go func() {
		defer stops.Done()
		lane.stop()
	}()
	stops.Wait()

	require.False(t, lane.running())
	require.Equal(t, validationQueueStopped,
		lane.submit(validationWork{validation: &consensus.Validation{}}))
}

func TestValidationWorkLaneRestartDrainsOutstandingPermits(t *testing.T) {
	lane := newValidationWorkLane(func(*consensus.Validation) error { return nil }, nil, nil)
	lane.trustedWorkers = 0
	lane.untrustedWorkers = 0
	lane.start(t.Context())

	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
	}))
	lane.stop()
	require.Equal(t, cap(lane.untrustedPermits), len(lane.untrustedPermits))

	lane.start(t.Context())
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
	}))
	work := <-lane.untrustedJobs
	lane.untrustedResultCh <- validationWorkResult{permit: work.permit}
	lane.stop()
	require.Equal(t, cap(lane.untrustedPermits), len(lane.untrustedPermits))
}

func TestValidationWorkLaneLimitsTrustedClaimsPerPeer(t *testing.T) {
	lane := newValidationWorkLane(func(*consensus.Validation) error { return nil }, nil, nil)
	lane.trustedWorkers = 0
	lane.untrustedWorkers = 0
	lane.start(t.Context())
	defer lane.stop()

	for range trustedValidationPerPeerDepth {
		require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
			validation: &consensus.Validation{},
			origin:     consensus.ValidationOrigin{PeerID: 10},
			trusted:    true,
		}))
	}
	require.Equal(t, validationQueueSaturated, lane.submit(validationWork{
		validation: &consensus.Validation{},
		origin:     consensus.ValidationOrigin{PeerID: 10},
		trusted:    true,
	}))
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
		origin:     consensus.ValidationOrigin{PeerID: 11},
		trusted:    true,
	}))

	work, ok := lane.next(t.Context(), true)
	require.True(t, ok)
	require.Equal(t, uint64(10), work.origin.PeerID)
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
		origin:     consensus.ValidationOrigin{PeerID: 10},
		trusted:    true,
	}))
}

func TestRouterTrustedValidationQueueFullShedsWithoutCrypto(t *testing.T) {
	adaptor := newTestAdaptor(t)
	engine := &validationProcessorEngine{
		mockEngine: &mockEngine{},
		disposition: consensus.ValidationDisposition{
			Status:  consensus.ValidationCurrent,
			Tracked: true,
			Trusted: true,
		},
	}
	router := newTestRouter(engine, adaptor, nil)
	verifyCalls := 0
	lane := newValidationWorkLane(func(*consensus.Validation) error {
		verifyCalls++
		return nil
	}, nil, nil)
	lane.trustedWorkers = 0
	lane.untrustedWorkers = 0
	lane.start(t.Context())
	defer lane.stop()
	router.validationWork = lane
	var logs bytes.Buffer
	router.logger = slog.New(slog.NewTextHandler(&logs, nil))

	require.Equal(t, 64, cap(lane.untrustedJobs))
	for range cap(lane.trustedJobs) {
		require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
			validation: &consensus.Validation{},
			trusted:    true,
		}))
	}

	for range 130 {
		outcome := router.submitValidationWork(validationWork{
			validation: &consensus.Validation{},
			origin:     consensus.ValidationOrigin{PeerID: 15},
			trusted:    true,
		})
		require.Equal(t, validationWorkShedTrusted, outcome)
	}
	require.Zero(t, verifyCalls, "unverified trust claims must not force router-thread crypto")
	require.Zero(t, engine.processedCount())
	require.Equal(t, uint64(130), router.validationShedTrusted.Load())
	require.Zero(t, router.validationShedUntrusted.Load())
	require.Equal(t, 3, strings.Count(logs.String(), "trusted validation verifier saturated"),
		"saturation logs should emit for counts 1, 64, and 128")
}

func TestRouterValidationAdmissionFailureAllowsDuplicateRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		trusted    bool
		queue      func(*validationWorkLane) chan validationWork
		validation func(*testing.T, *Adaptor) *consensus.Validation
	}{
		{
			name:    "trusted",
			trusted: true,
			queue:   func(lane *validationWorkLane) chan validationWork { return lane.trustedJobs },
			validation: func(t *testing.T, adaptor *Adaptor) *consensus.Validation {
				validation := &consensus.Validation{
					Full:      true,
					LedgerSeq: 42,
					LedgerID:  consensus.LedgerID{1},
					SignTime:  time.Now(),
				}
				require.NoError(t, adaptor.identity.SignValidation(validation))
				return validation
			},
		},
		{
			name:  "untrusted",
			queue: func(lane *validationWorkLane) chan validationWork { return lane.untrustedJobs },
			validation: func(_ *testing.T, _ *Adaptor) *consensus.Validation {
				return &consensus.Validation{
					Full:          true,
					LedgerSeq:     42,
					LedgerID:      consensus.LedgerID{1},
					SigningPubKey: consensus.SigningPubKey{0x02, 1},
					SignTime:      time.Now(),
					Signature:     make([]byte, 70),
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := newTestAdaptor(t)
			router := newTestRouter(&validationProcessorEngine{mockEngine: &mockEngine{}}, adaptor, nil)
			lane := router.validationWork
			lane.trustedWorkers = 0
			lane.untrustedWorkers = 0
			lane.start(t.Context())
			defer lane.stop()
			router.setPeerSessionView(&validationPeerSessions{})

			jobs := tc.queue(lane)
			for range cap(jobs) {
				require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
					validation: &consensus.Validation{},
					trusted:    tc.trusted,
				}))
			}

			serialized := serializeSTValidation(tc.validation(t, adaptor))
			payload := encodePayload(t, &message.Validation{Validation: serialized})
			suppressionHash := hashValidationSuppression(serialized)
			router.handleValidation(&peermanagement.InboundMessage{PeerID: 12, Payload: payload})

			require.False(t, router.messageSeen.seenRecently(suppressionHash))
			work := <-jobs
			work.permit.release()
			router.handleValidation(&peermanagement.InboundMessage{PeerID: 13, Payload: payload})
			require.Len(t, jobs, cap(jobs))
		})
	}
}

func TestRouterUntrustedValidationQueueSaturationRateLimited(t *testing.T) {
	adaptor := newTestAdaptor(t)
	engine := &validationProcessorEngine{mockEngine: &mockEngine{}}
	router := newTestRouter(engine, adaptor, nil)
	var logs bytes.Buffer
	router.logger = slog.New(slog.NewTextHandler(&logs, nil))
	lane := router.validationWork
	lane.trustedWorkers = 0
	lane.untrustedWorkers = 0
	lane.start(t.Context())
	defer lane.stop()

	for range cap(lane.untrustedJobs) {
		require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
			validation: &consensus.Validation{},
		}))
	}
	for range 130 {
		outcome := router.submitValidationWork(validationWork{
			validation: &consensus.Validation{},
			origin:     consensus.ValidationOrigin{PeerID: 16},
		})
		require.Equal(t, validationWorkShedUntrusted, outcome)
	}

	require.Equal(t, uint64(130), router.validationShedUntrusted.Load())
	require.Equal(t, 3, strings.Count(logs.String(), "untrusted validation verifier saturated"),
		"saturation logs should emit for counts 1, 64, and 128")
}

func TestValidationResultBackpressureBoundsUntrustedVerification(t *testing.T) {
	var verifyCalls atomic.Int64
	adaptor := newTestAdaptor(t)
	engine := &validationProcessorEngine{mockEngine: &mockEngine{}}
	router := newTestRouter(engine, adaptor, nil)
	lane := newValidationWorkLane(func(*consensus.Validation) error {
		verifyCalls.Add(1)
		return nil
	}, nil, nil)
	lane.trustedWorkers = 0
	lane.untrustedWorkers = cap(lane.untrustedPermits)
	lane.start(t.Context())
	defer lane.stop()

	for range cap(lane.untrustedPermits) {
		require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
			validation: &consensus.Validation{},
		}))
	}
	require.Eventually(t, func() bool {
		return verifyCalls.Load() == int64(cap(lane.untrustedPermits)) &&
			len(lane.untrustedResultCh) == cap(lane.untrustedResultCh)
	}, time.Second, time.Millisecond)
	require.Equal(t, validationQueueSaturated, lane.submit(validationWork{
		validation: &consensus.Validation{},
	}))
	require.Equal(t, int64(cap(lane.untrustedPermits)), verifyCalls.Load(),
		"a full result lane must backpressure before another verification starts")

	result := <-lane.untrustedResultCh
	router.handleValidationWorkResult(result)
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
	}))
}

func TestRouterReleasesUntrustedPermitWhenPriorityDrainCancels(t *testing.T) {
	adaptor := newTestAdaptor(t)
	router := newTestRouter(&validationProcessorEngine{mockEngine: &mockEngine{}}, adaptor, nil)
	lane := router.validationWork
	lane.trustedWorkers = 0
	lane.untrustedWorkers = 0
	lane.start(t.Context())
	defer lane.stop()

	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{},
	}))
	work := <-lane.untrustedJobs
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.False(t, router.handleUntrustedValidationWorkResult(ctx, validationWorkResult{
		permit: work.permit,
	}))
	require.Equal(t, cap(lane.untrustedPermits), len(lane.untrustedPermits))
}

func TestUntrustedResultSaturationDoesNotBlockTrustedWorker(t *testing.T) {
	untrustedStarted := make(chan struct{})
	unblockUntrusted := make(chan struct{})
	var unblockOnce sync.Once
	release := func() { unblockOnce.Do(func() { close(unblockUntrusted) }) }
	lane := newValidationWorkLane(func(validation *consensus.Validation) error {
		if validation.LedgerID[0] == 1 {
			close(untrustedStarted)
			<-unblockUntrusted
		}
		return nil
	}, nil, nil)
	lane.trustedWorkers = 1
	lane.untrustedWorkers = 1
	for range cap(lane.untrustedResultCh) {
		lane.untrustedResultCh <- validationWorkResult{}
	}
	lane.start(t.Context())
	defer lane.stop()
	defer release()

	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{1}},
	}))
	<-untrustedStarted
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{2}},
		trusted:    true,
	}))
	release()

	select {
	case result := <-lane.trustedResultCh:
		require.Equal(t, byte(2), result.validation.LedgerID[0])
	case <-time.After(time.Second):
		t.Fatal("trusted worker result blocked behind saturated untrusted results")
	}
}

func TestValidationWorkLanePromotesQueuedResultAfterTrustChange(t *testing.T) {
	verificationStarted := make(chan struct{})
	finishVerification := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishVerification) }) }
	nodeID := consensus.NodeID{0x31}
	var trusted atomic.Bool
	lane := newValidationWorkLane(
		func(*consensus.Validation) error {
			close(verificationStarted)
			<-finishVerification
			return nil
		},
		nil,
		func(candidate consensus.NodeID) bool {
			return candidate == nodeID && trusted.Load()
		},
	)
	lane.trustedWorkers = 1
	lane.untrustedWorkers = 1
	for range cap(lane.untrustedResultCh) {
		lane.untrustedResultCh <- validationWorkResult{}
	}
	lane.start(t.Context())
	defer lane.stop()
	defer finish()

	validation := &consensus.Validation{
		LedgerID: consensus.LedgerID{3},
		NodeID:   nodeID,
	}
	require.Equal(t, validationQueueAccepted, lane.submit(validationWork{
		validation: validation,
		trusted:    false,
	}))
	<-verificationStarted
	trusted.Store(true)
	finish()

	select {
	case result := <-lane.trustedResultCh:
		require.Same(t, validation, result.validation)
		result.permit.release()
	case <-time.After(time.Second):
		t.Fatal("newly trusted verified result was not routed to the trusted result lane")
	}
	require.Equal(t, cap(lane.untrustedResultCh), len(lane.untrustedResultCh),
		"saturated untrusted results must remain isolated from a promoted result")
}

func TestRouterDrainsTrustedValidationResultsBeforeUntrusted(t *testing.T) {
	adaptor := newTestAdaptor(t)
	engine := &validationProcessorEngine{mockEngine: &mockEngine{}}
	router := newTestRouter(engine, adaptor, nil)
	lane := router.validationWork
	require.NotNil(t, lane)

	lane.untrustedResultCh <- validationWorkResult{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{1}},
		origin:     consensus.ValidationOrigin{PeerID: 1},
	}
	lane.trustedResultCh <- validationWorkResult{
		validation: &consensus.Validation{LedgerID: consensus.LedgerID{2}},
		origin:     consensus.ValidationOrigin{PeerID: 2},
	}

	require.True(t, router.drainTrustedValidationResults(t.Context()))
	require.Equal(t, 1, engine.processedCount())
	require.Len(t, lane.untrustedResultCh, 1)
	engine.muProcessor.Lock()
	require.Equal(t, uint64(2), engine.origins[0].PeerID)
	engine.muProcessor.Unlock()
}

func TestRouterBoundsTrustedValidationDrain(t *testing.T) {
	adaptor := newTestAdaptor(t)
	engine := &validationProcessorEngine{mockEngine: &mockEngine{}}
	router := newTestRouter(engine, adaptor, nil)
	lane := router.validationWork
	require.NotNil(t, lane)

	total := trustedValidationDrainBatch + 1
	for i := range total {
		lane.trustedResultCh <- validationWorkResult{
			validation: &consensus.Validation{LedgerID: consensus.LedgerID{byte(i + 1)}},
			origin:     consensus.ValidationOrigin{PeerID: uint64(i + 1)},
		}
	}

	require.True(t, router.drainTrustedValidationResults(t.Context()))
	require.Equal(t, trustedValidationDrainBatch, engine.processedCount())
	require.Len(t, lane.trustedResultCh, 1,
		"a trusted-validation burst must yield before starving the router select loop")
}

func TestRouterValidationResultChargesOnlyInvalidSignature(t *testing.T) {
	adaptor := newTestAdaptor(t)
	sender := &validationCaptureSender{}
	adaptor.sender = sender
	engine := &validationProcessorEngine{
		mockEngine:  &mockEngine{},
		disposition: consensus.ValidationDisposition{Relay: true},
	}
	router := newTestRouter(engine, adaptor, nil)
	validation := &consensus.Validation{LedgerID: consensus.LedgerID{1}}

	router.handleValidationWorkResult(validationWorkResult{
		validation: validation,
		origin:     consensus.ValidationOrigin{PeerID: 9},
		err:        errors.New("bad signature"),
	})

	require.Zero(t, engine.processedCount())
	require.Equal(t, []string{"validation-invalid-signature"}, sender.bad)
	require.Empty(t, sender.relayed)
}

func TestRouterValidationResultUsesDispositionForAcquireAndRelay(t *testing.T) {
	adaptor := newTestAdaptor(t)
	sender := &validationCaptureSender{}
	adaptor.sender = sender
	engine := &validationProcessorEngine{
		mockEngine: &mockEngine{},
		disposition: consensus.ValidationDisposition{
			Status:  consensus.ValidationCurrent,
			Tracked: true,
			Trusted: true,
			Relay:   true,
		},
	}
	router := newTestRouter(engine, adaptor, nil)
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0xAA},
		LedgerSeq: 77,
		NodeID:    adaptor.identity.NodeID,
	}

	router.handleValidationWorkResult(validationWorkResult{
		validation: validation,
		origin:     consensus.ValidationOrigin{PeerID: 9},
	})

	require.Equal(t, 1, engine.processedCount())
	require.Len(t, sender.relayed, 1)
	entry, ok := router.seqHash[validation.LedgerSeq]
	require.True(t, ok, "trusted current validation must enter acquisition bookkeeping")
	require.Equal(t, [32]byte(validation.LedgerID), entry.hash)

	engine.disposition.Status = consensus.ValidationConflicting
	delete(router.seqHash, validation.LedgerSeq)
	router.handleValidationWorkResult(validationWorkResult{
		validation: validation,
		origin:     consensus.ValidationOrigin{PeerID: 9},
	})
	_, ok = router.seqHash[validation.LedgerSeq]
	require.False(t, ok, "Byzantine validation must not drive acquisition")
	require.Len(t, sender.relayed, 2, "valid Byzantine validation must still relay")
	require.Empty(t, sender.bad, "Byzantine signer behavior must not charge the relay peer")
}

func TestRouterValidationAdmissionDropsUntrustedUnderLoadOrDivergence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(*Router, *Adaptor)
		tracking peermanagement.PeerTracking
	}{
		{
			name: "local load",
			prepare: func(_ *Router, adaptor *Adaptor) {
				adaptor.LedgerService().FeeTrack().RaiseLocalFee()
				adaptor.LedgerService().FeeTrack().RaiseLocalFee()
			},
		},
		{
			name:     "diverged peer",
			tracking: peermanagement.PeerTrackingDiverged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := newTestAdaptor(t)
			sender := &validationCaptureSender{}
			adaptor.sender = sender
			engine := &validationProcessorEngine{
				mockEngine:  &mockEngine{},
				disposition: consensus.ValidationDisposition{Relay: true},
			}
			router := newTestRouter(engine, adaptor, nil)
			router.setPeerSessionView(&validationPeerSessions{
				peers: []peermanagement.PeerInfo{{
					ID:       12,
					Tracking: tc.tracking,
				}},
			})
			if tc.prepare != nil {
				tc.prepare(router, adaptor)
			}

			validation := &consensus.Validation{
				Full:          true,
				LedgerSeq:     42,
				LedgerID:      consensus.LedgerID{1},
				SigningPubKey: consensus.SigningPubKey{0x02, 1},
				SignTime:      time.Now(),
				Signature:     make([]byte, 70),
			}
			router.handleValidation(&peermanagement.InboundMessage{
				PeerID: 12,
				Payload: encodePayload(t, &message.Validation{
					Validation: serializeSTValidation(validation),
				}),
			})

			require.Zero(t, engine.processedCount())
			require.Empty(t, sender.bad,
				"admission shedding occurs before signature verification")
			sender.mu.Lock()
			sourceCount := 0
			for _, peers := range sender.sources {
				sourceCount += len(peers)
			}
			sender.mu.Unlock()
			require.Equal(t, 1, sourceCount,
				"the proven inbound source is recorded before admission shedding")
		})
	}
}

func TestRouterValidationAdmissionKeepsTrustedUnderLocalLoad(t *testing.T) {
	adaptor := newTestAdaptor(t)
	adaptor.LedgerService().FeeTrack().RaiseLocalFee()
	adaptor.LedgerService().FeeTrack().RaiseLocalFee()
	engine := &validationProcessorEngine{
		mockEngine: &mockEngine{},
		disposition: consensus.ValidationDisposition{
			Status:  consensus.ValidationCurrent,
			Tracked: true,
			Trusted: true,
		},
	}
	router := newTestRouter(engine, adaptor, nil)
	validation := &consensus.Validation{
		Full:      true,
		LedgerSeq: 42,
		LedgerID:  consensus.LedgerID{1},
		SignTime:  time.Now(),
	}
	require.NoError(t, adaptor.identity.SignValidation(validation))

	router.handleValidation(&peermanagement.InboundMessage{
		PeerID: 12,
		Payload: encodePayload(t, &message.Validation{
			Validation: serializeSTValidation(validation),
		}),
	})

	require.Equal(t, 1, engine.processedCount())
}

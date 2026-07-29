package adaptor

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

const (
	trustedValidationQueueDepth   = 256
	untrustedValidationQueueDepth = 64
	validationWorkerCount         = 2
	trustedValidationPerPeerDepth = trustedValidationQueueDepth / 4
)

type validationWork struct {
	validation *consensus.Validation
	origin     consensus.ValidationOrigin
	trusted    bool
}

type validationWorkResult struct {
	validation *consensus.Validation
	origin     consensus.ValidationOrigin
	err        error
}

type validationResultDelivery uint8

const (
	validationResultDelivered validationResultDelivery = iota
	validationResultUntrustedSaturated
	validationResultCancelled
)

type validationQueueAdmission uint8

const (
	validationQueueAccepted validationQueueAdmission = iota
	validationQueueSaturated
	validationQueueStopped
)

type validationWorkLane struct {
	verify      func(*consensus.Validation) error
	peerPresent func(peermanagement.PeerID) bool
	isTrusted   func(consensus.NodeID) bool

	trustedJobs       chan validationWork
	untrustedJobs     chan validationWork
	trustedResultCh   chan validationWorkResult
	untrustedResultCh chan validationWorkResult
	workers           int
	trustedPending    map[peermanagement.PeerID]int

	// Set before start and immutable while workers run.
	onUntrustedResultShed func(validationWorkResult, uint64)
	untrustedResultShed   atomic.Uint64

	mu     sync.Mutex
	done   <-chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newValidationWorkLane(
	verify func(*consensus.Validation) error,
	peerPresent func(peermanagement.PeerID) bool,
	isTrusted func(consensus.NodeID) bool,
) *validationWorkLane {
	return &validationWorkLane{
		verify:            verify,
		peerPresent:       peerPresent,
		isTrusted:         isTrusted,
		trustedJobs:       make(chan validationWork, trustedValidationQueueDepth),
		untrustedJobs:     make(chan validationWork, untrustedValidationQueueDepth),
		trustedResultCh:   make(chan validationWorkResult, trustedValidationQueueDepth),
		untrustedResultCh: make(chan validationWorkResult, untrustedValidationQueueDepth),
		workers:           validationWorkerCount,
		trustedPending:    make(map[peermanagement.PeerID]int),
	}
}

func (l *validationWorkLane) start(ctx context.Context) {
	if l == nil || l.verify == nil {
		return
	}

	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = workerCtx.Done()
	l.mu.Unlock()

	for range l.workers {
		l.wg.Add(1)
		go l.run(workerCtx)
	}
}

func (l *validationWorkLane) stop() {
	if l == nil {
		return
	}

	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.done = nil
	l.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	l.wg.Wait()
}

func (l *validationWorkLane) submit(work validationWork) validationQueueAdmission {
	if l == nil {
		return validationQueueStopped
	}

	l.mu.Lock()
	done := l.done
	if done == nil {
		l.mu.Unlock()
		return validationQueueStopped
	}

	jobs := l.untrustedJobs
	if work.trusted {
		jobs = l.trustedJobs
		peerID := peermanagement.PeerID(work.origin.PeerID)
		if peerID != 0 && l.trustedPending[peerID] >= trustedValidationPerPeerDepth {
			l.mu.Unlock()
			return validationQueueSaturated
		}
	}
	select {
	case jobs <- work:
		if work.trusted && work.origin.PeerID != 0 {
			l.trustedPending[peermanagement.PeerID(work.origin.PeerID)]++
		}
		l.mu.Unlock()
		return validationQueueAccepted
	case <-done:
		l.mu.Unlock()
		return validationQueueStopped
	default:
		l.mu.Unlock()
		return validationQueueSaturated
	}
}

func (l *validationWorkLane) running() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.done != nil
}

func (l *validationWorkLane) trustedResults() <-chan validationWorkResult {
	if l == nil {
		return nil
	}
	return l.trustedResultCh
}

func (l *validationWorkLane) untrustedResults() <-chan validationWorkResult {
	if l == nil {
		return nil
	}
	return l.untrustedResultCh
}

func (r *Router) trustedValidationWorkResults() <-chan validationWorkResult {
	if r.validationWork == nil {
		return nil
	}
	return r.validationWork.trustedResults()
}

func (r *Router) untrustedValidationWorkResults() <-chan validationWorkResult {
	if r.validationWork == nil {
		return nil
	}
	return r.validationWork.untrustedResults()
}

func (l *validationWorkLane) run(ctx context.Context) {
	defer l.wg.Done()
	for {
		work, ok := l.next(ctx)
		if !ok {
			return
		}
		if l.peerPresent != nil &&
			work.origin.PeerID != 0 &&
			!l.peerPresent(peermanagement.PeerID(work.origin.PeerID)) {
			continue
		}

		result := validationWorkResult{
			validation: work.validation,
			origin:     work.origin,
			err:        l.verify(work.validation),
		}
		trustedResult := work.trusted
		if !trustedResult && l.isTrusted != nil {
			trustedResult = l.isTrusted(work.validation.NodeID)
		}
		if l.deliverResult(ctx, result, trustedResult) == validationResultCancelled {
			return
		}
	}
}

func (l *validationWorkLane) deliverResult(
	ctx context.Context,
	result validationWorkResult,
	trusted bool,
) validationResultDelivery {
	if trusted {
		select {
		case l.trustedResultCh <- result:
			return validationResultDelivered
		case <-ctx.Done():
			return validationResultCancelled
		}
	}

	select {
	case l.untrustedResultCh <- result:
		return validationResultDelivered
	case <-ctx.Done():
		return validationResultCancelled
	default:
		count := l.untrustedResultShed.Add(1)
		if l.onUntrustedResultShed != nil {
			l.onUntrustedResultShed(result, count)
		}
		return validationResultUntrustedSaturated
	}
}

func (l *validationWorkLane) next(ctx context.Context) (validationWork, bool) {
	select {
	case work := <-l.trustedJobs:
		l.markDequeued(work)
		return work, true
	default:
	}

	select {
	case work := <-l.trustedJobs:
		l.markDequeued(work)
		return work, true
	case work := <-l.untrustedJobs:
		return work, true
	case <-ctx.Done():
		return validationWork{}, false
	}
}

func (l *validationWorkLane) markDequeued(work validationWork) {
	if !work.trusted || work.origin.PeerID == 0 {
		return
	}
	peerID := peermanagement.PeerID(work.origin.PeerID)
	l.mu.Lock()
	if l.trustedPending[peerID] <= 1 {
		delete(l.trustedPending, peerID)
	} else {
		l.trustedPending[peerID]--
	}
	l.mu.Unlock()
}

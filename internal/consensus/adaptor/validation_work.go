package adaptor

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

const (
	trustedValidationQueueDepth    = 256
	untrustedValidationQueueDepth  = 64
	validationWorkerCount          = 2
	trustedValidationWorkerCount   = 1
	untrustedValidationWorkerCount = validationWorkerCount - trustedValidationWorkerCount
	trustedValidationPerPeerDepth  = trustedValidationQueueDepth / 4
)

type validationPermit struct {
	lane     *validationWorkLane
	released atomic.Bool
}

func (p *validationPermit) release() {
	if p == nil || p.lane == nil || p.released.Swap(true) {
		return
	}
	p.lane.untrustedPermits <- struct{}{}
}

type validationWork struct {
	validation *consensus.Validation
	origin     consensus.ValidationOrigin
	trusted    bool
	permit     *validationPermit
}

type validationWorkResult struct {
	validation *consensus.Validation
	origin     consensus.ValidationOrigin
	err        error
	permit     *validationPermit
}

type validationResultDelivery uint8

const (
	validationResultDelivered validationResultDelivery = iota
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
	// Untrusted permits cover queueing, verification, and result consumption.
	untrustedPermits chan struct{}
	trustedWorkers   int
	untrustedWorkers int
	trustedPending   map[peermanagement.PeerID]int

	mu       sync.Mutex
	done     <-chan struct{}
	cancel   context.CancelFunc
	stopped  chan struct{}
	stopping bool
	wg       sync.WaitGroup
}

func newValidationWorkLane(
	verify func(*consensus.Validation) error,
	peerPresent func(peermanagement.PeerID) bool,
	isTrusted func(consensus.NodeID) bool,
) *validationWorkLane {
	lane := &validationWorkLane{
		verify:            verify,
		peerPresent:       peerPresent,
		isTrusted:         isTrusted,
		trustedJobs:       make(chan validationWork, trustedValidationQueueDepth),
		untrustedJobs:     make(chan validationWork, untrustedValidationQueueDepth),
		trustedResultCh:   make(chan validationWorkResult, trustedValidationQueueDepth),
		untrustedResultCh: make(chan validationWorkResult, untrustedValidationQueueDepth),
		untrustedPermits:  make(chan struct{}, untrustedValidationQueueDepth),
		trustedWorkers:    trustedValidationWorkerCount,
		untrustedWorkers:  untrustedValidationWorkerCount,
		trustedPending:    make(map[peermanagement.PeerID]int),
	}
	for range untrustedValidationQueueDepth {
		lane.untrustedPermits <- struct{}{}
	}
	return lane
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
	l.stopping = false
	workerCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = workerCtx.Done()
	l.stopped = make(chan struct{})
	for range l.trustedWorkers {
		l.wg.Add(1)
		go l.run(workerCtx, true)
	}
	for range l.untrustedWorkers {
		l.wg.Add(1)
		go l.run(workerCtx, false)
	}
	l.mu.Unlock()
}

func (l *validationWorkLane) stop() {
	if l == nil {
		return
	}

	l.mu.Lock()
	cancel := l.cancel
	stopped := l.stopped
	if cancel == nil {
		l.mu.Unlock()
		return
	}
	if l.stopping {
		l.mu.Unlock()
		<-stopped
		return
	}
	l.stopping = true
	l.mu.Unlock()
	cancel()
	l.wg.Wait()
	l.drainPending()
	l.mu.Lock()
	l.cancel = nil
	l.done = nil
	l.stopping = false
	l.stopped = nil
	close(stopped)
	l.mu.Unlock()
}

func (l *validationWorkLane) submit(work validationWork) validationQueueAdmission {
	if l == nil {
		return validationQueueStopped
	}

	l.mu.Lock()
	done := l.done
	if done == nil || l.stopping {
		l.mu.Unlock()
		return validationQueueStopped
	}
	select {
	case <-done:
		l.mu.Unlock()
		return validationQueueStopped
	default:
	}

	var permit *validationPermit
	if !work.trusted {
		select {
		case <-l.untrustedPermits:
			permit = &validationPermit{lane: l}
		default:
			l.mu.Unlock()
			return validationQueueSaturated
		}
		work.permit = permit
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
		if permit != nil {
			permit.release()
		}
		l.mu.Unlock()
		return validationQueueStopped
	default:
		if permit != nil {
			permit.release()
		}
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

func (l *validationWorkLane) run(ctx context.Context, trustedWorker bool) {
	defer l.wg.Done()
	for {
		work, ok := l.next(ctx, trustedWorker)
		if !ok {
			return
		}
		if l.peerPresent != nil &&
			work.origin.PeerID != 0 &&
			!l.peerPresent(peermanagement.PeerID(work.origin.PeerID)) {
			work.permit.release()
			continue
		}

		result := validationWorkResult{
			validation: work.validation,
			origin:     work.origin,
			permit:     work.permit,
			err:        l.verify(work.validation),
		}
		trustedResult := work.trusted
		if !trustedResult && l.isTrusted != nil {
			trustedResult = l.isTrusted(work.validation.NodeID)
		}
		if l.deliverResult(ctx, result, trustedResult) == validationResultCancelled {
			work.permit.release()
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
	}
}

func (l *validationWorkLane) next(ctx context.Context, trustedWorker bool) (validationWork, bool) {
	if ctx.Err() != nil {
		return validationWork{}, false
	}
	jobs := l.untrustedJobs
	if trustedWorker {
		jobs = l.trustedJobs
	}
	select {
	case work := <-jobs:
		if trustedWorker {
			l.markDequeued(work)
		}
		if ctx.Err() != nil {
			work.permit.release()
			return validationWork{}, false
		}
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

func (l *validationWorkLane) drainPending() {
	if l == nil {
		return
	}
	for {
		select {
		case work := <-l.trustedJobs:
			work.permit.release()
		case work := <-l.untrustedJobs:
			work.permit.release()
		default:
			l.mu.Lock()
			l.trustedPending = make(map[peermanagement.PeerID]int)
			l.mu.Unlock()
			for {
				select {
				case result := <-l.trustedResultCh:
					result.permit.release()
				case result := <-l.untrustedResultCh:
					result.permit.release()
				default:
					return
				}
			}
		}
	}
}

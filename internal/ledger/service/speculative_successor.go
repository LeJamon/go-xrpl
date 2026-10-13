package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

const speculativeSuccessorMemoryLimit = 64 << 20

// Inputs remain immutable until publication or handoff to a newer frontier.
// Only the single service worker may execute a successor's private state.
type speculativeSuccessor struct {
	parent         *ledger.Ledger
	view           *openledger.OpenLedger
	queue          *txq.TxQ
	validated      *ledger.Ledger
	prior, retries []openledger.PendingTx
	locals         []openledger.PendingTx
	salt           *[32]byte
	anyDisputes    bool
	networkID      uint32
	relayFn        func([]byte)
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	queuedAt       time.Time
	started        bool
	finished       bool
	releaseWork    func()
	releaseMemory  func()
	memory         *successorMemory
	releaseOnce    sync.Once
}

func saturatedSuccessorBytes(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func (j *speculativeSuccessor) inputBytes() uint64 {
	var size uint64
	for _, group := range [][]openledger.PendingTx{j.prior, j.retries} {
		for _, item := range group {
			size = saturatedSuccessorBytes(size, uint64(len(item.Blob))*2+preferredTransitionMetadataBytes)
		}
	}
	return size
}

func mergeSuccessorTransactions(first, second []openledger.PendingTx) []openledger.PendingTx {
	if len(second) == 0 {
		return first
	}
	seen := make(map[[32]byte]struct{}, len(first)+len(second))
	merged := make([]openledger.PendingTx, 0, len(first)+len(second))
	for _, group := range [][]openledger.PendingTx{first, second} {
		for _, item := range group {
			if _, exists := seen[item.Hash]; !exists {
				seen[item.Hash] = struct{}{}
				merged = append(merged, item)
			}
		}
	}
	return merged
}

func (s *Service) scheduleSpeculativeSuccessorLocked(p *PreparedPreferredLedger) {
	j := &speculativeSuccessor{
		parent: p.parent, view: p.openView, queue: p.queue, validated: p.validatedAtPrepare,
		prior: p.prior, retries: p.options.retries, locals: p.locals,
		salt: p.options.retrySalt, anyDisputes: p.options.anyDisputes,
		networkID: p.networkID, relayFn: p.relayFn, done: make(chan struct{}), queuedAt: time.Now(),
		releaseWork: p.releaseWork, releaseMemory: p.releaseMemory, memory: p.memory,
	}
	p.releaseWork, p.releaseMemory = nil, nil
	j.ctx, j.cancel = context.WithCancel(context.Background())
	s.speculativeSuccessor = j
	if !s.speculativeWorkerRunning {
		s.speculativeWorkerRunning = true
		go s.runSpeculativeSuccessors()
	}
}

func (j *speculativeSuccessor) release() {
	j.releaseOnce.Do(func() {
		j.cancel()
		if j.releaseMemory != nil {
			j.releaseMemory()
		}
		if j.releaseWork != nil {
			j.releaseWork()
		}
	})
}

func (s *Service) cancelSpeculativeSuccessorLocked() {
	j := s.speculativeSuccessor
	s.speculativeSuccessor = nil
	if j == nil {
		return
	}
	j.cancel()
	if !j.started || j.finished {
		if !j.finished {
			j.finished = true
			close(j.done)
		}
		j.release()
	}
}

func (s *Service) stopSpeculativeSuccessors() {
	s.mu.Lock()
	s.cancelSpeculativeSuccessorLocked()
	if s.speculativeRunning != nil {
		s.speculativeRunning.cancel()
	}
	s.mu.Unlock()
}

func (s *Service) waitSpeculativeSuccessor(ctx context.Context) error {
	for {
		s.mu.RLock()
		j := s.speculativeSuccessor
		finished := j == nil || j.finished
		s.mu.RUnlock()
		if finished {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-j.done:
		}
	}
}

func (s *Service) runSpeculativeSuccessors() {
	for {
		s.mu.Lock()
		j := s.speculativeSuccessor
		if j == nil || j.finished {
			s.speculativeWorkerRunning = false
			s.speculativeRunning = nil
			s.mu.Unlock()
			return
		}
		j.started = true
		s.speculativeRunning = j
		s.mu.Unlock()

		started := time.Now()
		p, err := s.executeSpeculativeSuccessor(j)
		preparation := time.Since(started)
		var gateWait openLedgerGateWait
		var gateHold time.Duration
		published := false
		if err == nil {
			gateWait, err = s.lockOpenLedgerIfRunningTimed(j.ctx, openLedgerTransition)
			if err == nil {
				s.mu.Lock()
				if s.speculativeSuccessor != j || s.closedLedger != j.parent || s.openLedgerView != j.view || s.txQueue != j.queue {
					err = ErrPreferredTransitionStale
				} else if s.ReplayBlocked() {
					err = replayfault.ErrBlocked
				} else if err = j.ctx.Err(); err == nil {
					s.openLedgerView = p.openView
					s.txQueue = p.queue
					s.tickLoadFeeLocked()
					s.speculativeSuccessor = nil
					published = true
				}
				s.mu.Unlock()
				gateHold = time.Since(gateWait.AcquiredAt)
				s.openLedgerMu.Unlock()
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPreferredTransitionStale) && !errors.Is(err, errServiceNotRunning) {
			s.logger.Warn("Speculative successor deferred", "sequence", j.parent.Sequence()+1, "err", err)
			s.recordExecutionStateFailure(j.ctx, j.parent, err)
		}
		if published {
			for _, item := range p.relay {
				s.rememberRelayTransaction(item.hash, item.blob, false)
			}
		}
		s.mu.Lock()
		j.finished = true
		close(j.done)
		retained := s.speculativeSuccessor == j
		s.mu.Unlock()
		// A failed attempt keeps its input reservation until the next frontier
		// captures those inputs. It no longer owns any storage work.
		if retained {
			if j.releaseWork != nil {
				j.releaseWork()
			}
		} else {
			j.release()
		}
		s.logger.Info("Speculative successor timings", "sequence", j.parent.Sequence()+1,
			"total", time.Since(started), "queue_wait", started.Sub(j.queuedAt), "preparation", preparation,
			"open_ledger_wait", gateWait.Wait, "open_ledger_hold", gateHold, "published", published,
			"prior", len(j.prior), "retries", len(j.retries), "locals", len(j.locals))
		if published && j.relayFn != nil {
			for _, item := range p.relay {
				j.relayFn(item.blob)
			}
		}
	}
}

func (s *Service) executeSpeculativeSuccessor(j *speculativeSuccessor) (*PreparedPreferredLedger, error) {
	if err := j.ctx.Err(); err != nil {
		return nil, err
	}
	if s.ReplayBlocked() {
		return nil, replayfault.ErrBlocked
	}
	p := &PreparedPreferredLedger{
		service: s, parent: j.parent, validatedAtPrepare: j.validated,
		prior: j.prior, locals: j.locals, networkID: j.networkID,
		ctx: j.ctx, ready: make(chan struct{}), state: preferredTransitionPreparing,
		options: successorOptions{replaySpeculation: true, retries: j.retries, retrySalt: j.salt, anyDisputes: j.anyDisputes},
		timings: preferredTransitionTimings{started: time.Now()},
	}
	var err error
	if j.queue != nil {
		p.queue, err = j.queue.Fork()
		if err != nil {
			return nil, err
		}
	}
	p.run()
	return p, p.Wait(j.ctx)
}

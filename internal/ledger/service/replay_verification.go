package service

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

const replayVerificationFlightLimit = 4

// replayVerificationKey binds a shared cold walk to both the ledger identity
// and the durable generation holding its node store. Roots are included so a
// replacement ledger with the same sequence cannot join an old walk.
type replayVerificationKey struct {
	sequence             uint32
	ledgerHash           [32]byte
	parentHash           [32]byte
	stateRoot            [32]byte
	txRoot               [32]byte
	transactionBacked    bool
	nodeStoreFingerprint [32]byte
}

type replayVerificationResult struct {
	state         ReplayAdmissionMapStats
	transactions  ReplayAdmissionMapStats
	proofCacheHit bool
}

type replayVerificationFlight struct {
	done    chan struct{}
	waiters int
	joined  int
	ctx     context.Context
	cancel  context.CancelFunc
	result  replayVerificationResult
	err     error
}

// replayVerificationRun is invoked by the flight owner with a service-owned
// context. It must not retain the context after returning.
type replayVerificationRun func(context.Context) (replayVerificationResult, error)

func (s *Service) joinReplayVerification(ctx context.Context, key replayVerificationKey) (*replayVerificationFlight, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		s.replayVerificationMu.Lock()
		if s.replayVerificationFlights == nil {
			s.replayVerificationFlights = make(map[replayVerificationKey]*replayVerificationFlight)
		}
		if flight, ok := s.replayVerificationFlights[key]; ok {
			if flight.waiters == 0 {
				done := flight.done
				s.replayVerificationMu.Unlock()
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case <-done:
					continue
				}
			}
			flight.joined++
			flight.waiters++
			s.replayVerificationMu.Unlock()
			return flight, false, nil
		}
		if len(s.replayVerificationFlights) < replayVerificationFlightLimit {
			ownerCtx, cancel := context.WithCancel(s.lifecycleContext()) //nolint:gosec // G118: the flight owns cancellation and calls it when its last waiter leaves or the owner finishes.
			flight := &replayVerificationFlight{
				done:    make(chan struct{}),
				waiters: 1,
				ctx:     ownerCtx,
				cancel:  cancel,
			}
			s.replayVerificationFlights[key] = flight
			s.replayVerificationMu.Unlock()
			return flight, true, nil
		}
		if s.replayVerificationWake == nil {
			s.replayVerificationWake = make(chan struct{})
		}
		wake := s.replayVerificationWake
		s.replayVerificationMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-wake:
		}
	}
}

func (s *Service) finishReplayVerification(key replayVerificationKey, flight *replayVerificationFlight, result replayVerificationResult, err error) {
	s.replayVerificationMu.Lock()
	if current, ok := s.replayVerificationFlights[key]; ok && current == flight {
		delete(s.replayVerificationFlights, key)
	}
	wake := s.replayVerificationWake
	s.replayVerificationWake = nil
	coalescedWaiters := flight.joined
	flight.ctx = nil
	flight.cancel = nil
	flight.result = result
	flight.err = err
	close(flight.done)
	s.replayVerificationMu.Unlock()
	if wake != nil {
		close(wake)
	}
	if coalescedWaiters > 0 {
		logger := s.logger
		if logger == nil {
			logger = xrpllog.Discard()
		}
		resultKind := "walk"
		if result.proofCacheHit {
			resultKind = "proof_hit"
		} else if err != nil {
			resultKind = "failed"
		}
		logger.Info("replay admission verification coalesced",
			"sequence", key.sequence,
			"ledger_hash", fmt.Sprintf("%x", key.ledgerHash[:8]),
			"parent_hash", fmt.Sprintf("%x", key.parentHash[:8]),
			"state_root", fmt.Sprintf("%x", key.stateRoot[:8]),
			"tx_root", fmt.Sprintf("%x", key.txRoot[:8]),
			"transaction_backed", key.transactionBacked,
			"generation", fmt.Sprintf("%x", key.nodeStoreFingerprint[:8]),
			"coalesced_waiters", coalescedWaiters,
			"result", resultKind,
			"err", err,
		)
	}
}

func (s *Service) leaveReplayVerification(key replayVerificationKey, flight *replayVerificationFlight) {
	s.replayVerificationMu.Lock()
	current, ok := s.replayVerificationFlights[key]
	if !ok || current != flight {
		s.replayVerificationMu.Unlock()
		return
	}
	if flight.waiters > 0 {
		flight.waiters--
	}
	var cancel context.CancelFunc
	if flight.waiters == 0 {
		cancel = flight.cancel
	}
	s.replayVerificationMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func markReplayVerificationCoalesced(result replayVerificationResult) replayVerificationResult {
	return replayVerificationResult{
		state:         ReplayAdmissionMapStats{Coalesced: true},
		transactions:  ReplayAdmissionMapStats{Coalesced: true},
		proofCacheHit: result.proofCacheHit,
	}
}

func (s *Service) logReplayVerificationDecision(h header.LedgerHeader, fingerprint [32]byte, decision, reason string, pinned bool) {
	logger := s.logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	logger.Debug("replay admission proof decision",
		"sequence", h.LedgerIndex,
		"ledger_hash", fmt.Sprintf("%x", h.Hash[:8]),
		"parent_hash", fmt.Sprintf("%x", h.ParentHash[:8]),
		"state_root", fmt.Sprintf("%x", h.AccountHash[:8]),
		"tx_root", fmt.Sprintf("%x", h.TxHash[:8]),
		"generation", fmt.Sprintf("%x", fingerprint[:8]),
		"generation_pinned", pinned,
		"proof", decision,
		"reason", reason,
	)
}

// coalesceReplayVerification shares the complete state+transaction admission
// walk. The pin is transferred to the owner; a joining caller releases its
// own pin immediately because the owner keeps the generation stable for the
// whole flight.
func (s *Service) coalesceReplayVerification(
	ctx context.Context,
	key replayVerificationKey,
	pin *replaySnapshotPin,
	run replayVerificationRun,
) (replayVerificationResult, error) {
	flight, owner, err := s.joinReplayVerification(ctx, key)
	if err != nil {
		if pin != nil {
			pin.Release()
		}
		return replayVerificationResult{}, err
	}
	if !owner {
		if pin != nil {
			pin.Release()
		}
		select {
		case <-ctx.Done():
			s.leaveReplayVerification(key, flight)
			return replayVerificationResult{}, ctx.Err()
		case <-flight.done:
			if err := ctx.Err(); err != nil {
				return replayVerificationResult{}, err
			}
			return markReplayVerificationCoalesced(flight.result), flight.err
		}
	}

	ownerCtx := flight.ctx
	cancel := flight.cancel
	releaseWork, err := s.acquireReplayPreparationWork(ownerCtx)
	if err != nil {
		cancel()
		if pin != nil {
			pin.Release()
		}
		s.finishReplayVerification(key, flight, replayVerificationResult{}, err)
		return replayVerificationResult{}, err
	}
	go func() {
		defer releaseWork()
		defer cancel()
		if pin != nil {
			defer pin.Release()
		}
		result, runErr := run(ownerCtx)
		s.finishReplayVerification(key, flight, result, runErr)
	}()

	select {
	case <-ctx.Done():
		s.leaveReplayVerification(key, flight)
		return replayVerificationResult{}, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return replayVerificationResult{}, err
		}
		return flight.result, flight.err
	}
}

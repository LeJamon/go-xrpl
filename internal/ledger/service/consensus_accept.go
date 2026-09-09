package service

import (
	"context"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func (s *Service) acceptConsensusResult(
	ctx context.Context,
	parent *ledger.Ledger,
	txBlobs, disputedBlobs [][]byte,
	closeTime time.Time,
	closeTimeCorrect bool,
) (uint32, error) {
	started := time.Now()
	var timings consensusAcceptanceTimings
	lifecycleStarted := time.Now()
	s.lifecycleMu.Lock()
	timings.lifecycleWait += time.Since(lifecycleStarted)
	if s.lifecycleState != serviceRunning {
		s.lifecycleMu.Unlock()
		return 0, errServiceNotRunning
	}
	s.consensusWG.Add(1)
	s.lifecycleMu.Unlock()
	var notification validatedLedgerNotification
	gateHeld := false
	var gateAcquiredAt time.Time
	defer func() {
		if gateHeld {
			timings.gateHold = time.Since(gateAcquiredAt)
			s.openLedgerMu.Unlock()
		}
		s.consensusWG.Done()
		callbackStarted := time.Now()
		notification.notify()
		timings.callback = time.Since(callbackStarted)
		timings.log(s, time.Since(started))
	}()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	lockStarted := time.Now()
	s.mu.RLock()
	timings.serviceWait += time.Since(lockStarted)
	// Every frontier transition replaces openLedger, including a switch away
	// and back to the same parent. Its identity fences detached publication.
	expectedClosed, expectedOpen, replay := s.closedLedger, s.openLedger, s.startupReplay
	s.mu.RUnlock()
	if expectedClosed == nil {
		return 0, svcerr.ErrNoClosedLedger
	}
	if parent != nil && (parent.Sequence() != expectedClosed.Sequence() || parent.Hash() != expectedClosed.Hash()) {
		return 0, fmt.Errorf("%w: closed=%d/%x parent=%d/%x", ErrConsensusParentMismatch,
			expectedClosed.Sequence(), expectedClosed.Hash(), parent.Sequence(), parent.Hash())
	}
	if expectedOpen == nil {
		return 0, svcerr.ErrNoOpenLedger
	}
	timings.sequence = expectedClosed.Sequence() + 1
	parseStarted := time.Now()

	// Build only the agreed set; speculative ingress must not affect the closed hash.
	pending := make([]openledger.PendingTx, 0, len(txBlobs))
	agreedSet := shamap.New(shamap.TypeTransaction)
	for _, blob := range txBlobs {
		// Malformed entries still contribute to the agreed set's ordering salt.
		hash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), blob)
		if err := agreedSet.PutWithNodeType(hash, blob, shamap.NodeTypeTransactionNoMeta); err != nil {
			return 0, fmt.Errorf("insert agreed transaction %x: %w", hash, err)
		}
		ptx, err := openledger.ParsePendingTx(blob)
		if err != nil {
			continue
		}
		pending = append(pending, ptx)
	}
	salt, err := agreedSet.Hash()
	if err != nil {
		return 0, fmt.Errorf("hash agreed transaction set: %w", err)
	}
	disputed := make([]openledger.PendingTx, 0, len(disputedBlobs))
	for _, blob := range disputedBlobs {
		if ptx, err := openledger.ParsePendingTx(blob); err == nil && !ptx.Parsed.TxType().IsPseudoTransaction() {
			disputed = append(disputed, ptx)
		}
	}
	anyDisputes := len(disputed) > 0
	timings.parse = time.Since(parseStarted)
	var closed *ledger.Ledger
	replayed := false
	buildStarted := time.Now()
	if replay != nil {
		replayParent := replay.Parent()
		if replayParent != nil && replayParent.Sequence() == expectedClosed.Sequence() && replayParent.Hash() == expectedClosed.Hash() {
			replayed = true
			closed, err = replay.Apply(s.EngineConfigForReplay(expectedClosed))
			if err != nil {
				return 0, fmt.Errorf("apply startup replay: %w", err)
			}
		}
	}

	var retriableTxs []openledger.PendingTx
	if !replayed {
		closed, retriableTxs, err = s.buildClosedLedger(expectedClosed, pending, salt, closeTime, false, &timings.apply)
		if err != nil {
			return 0, err
		}
	} else {
		openledger.CanonicalSort(pending, salt)
		retriableTxs = append(retriableTxs, pending...)
		closeTime = closed.CloseTime()
		closeTimeCorrect = closed.Header().CloseFlags&header.LCFNoConsensusTime == 0
	}

	timings.build = time.Since(buildStarted)

	// Pseudo-txs can't succeed in a later ledger; malformed blobs are
	// dropped. The merged set is re-sorted with the agreed set's SHAMap root
	// as salt, matching the canonical order rippled's retriable set applies in.
	if len(disputed) > 0 {
		seen := make(map[[32]byte]struct{}, len(retriableTxs))
		for _, ptx := range retriableTxs {
			seen[ptx.Hash] = struct{}{}
		}
		added := false
		for _, ptx := range disputed {
			if _, dup := seen[ptx.Hash]; dup {
				continue
			}
			seen[ptx.Hash] = struct{}{}
			retriableTxs = append(retriableTxs, ptx)
			added = true
		}
		if added {
			openledger.CanonicalSort(retriableTxs, salt)
		}
	}

	canonicalTxHashes := make([]string, 0, len(pending))
	for _, ptx := range pending {
		canonicalTxHashes = append(canonicalTxHashes, fmt.Sprintf("%x", ptx.Hash[:8]))
	}

	// Close at the consensus close time; set NoConsensusTime when consensus
	// didn't agree, so the hash matches rippled (issue #361).
	var closeFlags uint8
	if !closeTimeCorrect {
		closeFlags = header.LCFNoConsensusTime
	}
	hashStarted := time.Now()
	if !replayed {
		if err := closed.Close(closeTime, closeFlags); err != nil {
			return 0, fmt.Errorf("failed to close ledger: %w", err)
		}
	} else {
		closeFlags = closed.Header().CloseFlags
	}

	timings.stateTxHash = time.Since(hashStarted)

	closedSeq := closed.Sequence()
	closedLedgerHash := closed.Hash()
	stageStarted := time.Now()
	stagedResults, err := stageTransactionResults(closed, closedSeq, closedLedgerHash)
	if err != nil {
		return 0, fmt.Errorf("collect transaction results: %w", err)
	}
	timings.stage = time.Since(stageStarted)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	gateWait, gateErr := s.lockOpenLedgerIfRunningTimed(openLedgerConsensus)
	timings.gateWait = gateWait.Wait
	timings.lifecycleWait += gateWait.LifecycleWait
	if gateErr != nil {
		return 0, gateErr
	}
	gateAcquiredAt = gateWait.AcquiredAt
	gateHeld = true
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	lockStarted = time.Now()
	s.mu.Lock()
	timings.serviceWait += time.Since(lockStarted)
	if s.closedLedger != expectedClosed || s.openLedger != expectedOpen || s.startupReplay != replay {
		s.mu.Unlock()
		return 0, fmt.Errorf("%w: ledger ownership changed during build", ErrConsensusParentMismatch)
	}
	acceptOpen := s.openLedgerAcceptanceLocked(&timings.relay, &salt)
	s.mu.Unlock()
	nextStarted := time.Now()
	newOpen, err := ledger.NewOpen(closed, time.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to create new open ledger: %w", err)
	}
	err = acceptOpen(closed, retriableTxs, anyDisputes, func(publish func()) {
		timings.nextOpen = time.Since(nextStarted)
		lockStarted = time.Now()
		s.mu.Lock()
		timings.serviceWait += time.Since(lockStarted)
		defer s.mu.Unlock()
		previousValidated := s.validatedLedger
		historyStarted := time.Now()
		s.historyComponent.mu.Lock()
		timings.historyWait += time.Since(historyStarted)
		defer s.historyComponent.mu.Unlock()

		s.pendingTxs = nil
		if replay != nil {
			s.startupReplay = nil
		}

		// Validated entry wins the by-seq map; closedLedger still reflects the local
		// build so divergence is observable via server_info/ledger_closed.
		if existing, ok := s.ledgerHistory[closedSeq]; ok && existing.Hash() != closedLedgerHash && existing.IsValidated() {
			existingHash := existing.Hash()
			s.logger.Warn("local consensus close diverges from validated ledger; preserving validated in history, keeping local-build as closedLedger reference",
				"seq", closedSeq,
				"local_hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
				"validated_hash", fmt.Sprintf("%x", existingHash[:8]),
			)
			s.closedLedger = closed
		} else {
			s.closedLedger = closed
			s.putHistoryLocked(closed)
		}

		s.commitTransactionResultsLocked(stagedResults)
		var txResults []TransactionResultEvent
		if s.hasEventSink() {
			txResults = stagedResults.results
		}
		persistStarted := time.Now()
		if s.closedLedger.IsValidated() {
			s.enqueueValidatedHistoryPersist(s.closedLedger)
		} else {
			s.enqueueNodePersist(s.closedLedger)
		}

		timings.persist = time.Since(persistStarted)
		s.openLedger = newOpen
		s.tickLoadFeeLocked()

		// Quorum validation publishes the event, rather than the speculative close.
		event := &LedgerAcceptedEvent{
			LedgerInfo:         ledgerInfo(closed),
			Ledger:             s.closedLedger,
			TransactionResults: txResults,
		}
		s.retainValidationCandidateLocked(closed)
		if s.hasEventSink() {
			s.stashPendingValidationLocked(closedLedgerHash, event)
		}

		s.logger.Info("Consensus ledger accepted",
			"sequence", closedSeq,
			"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"txs", len(txResults),
		)

		notification = s.validatedLedgerNotificationLocked(previousValidated)
		publish()
	})
	if err != nil {
		return 0, err
	}

	timings.gateHold = time.Since(gateAcquiredAt)
	s.openLedgerMu.Unlock()
	gateHeld = false
	{
		stateRoot, _ := closed.StateMapHash()
		txRoot, _ := closed.TxMapHash()
		parentHash := closed.ParentHash()
		s.logger.Info("local-built ledger round-summary",
			"t", "consensus-build",
			"event", "round-summary",
			"seq", closedSeq,
			"hash", fmt.Sprintf("%x", closedLedgerHash[:8]),
			"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
			"close_time", closeTime.UTC().Format(time.RFC3339Nano),
			"close_time_correct", closeTimeCorrect,
			"close_flags", closeFlags,
			"state_root", fmt.Sprintf("%x", stateRoot[:8]),
			"tx_root", fmt.Sprintf("%x", txRoot[:8]),
			"total_drops", closed.TotalDrops(),
			"tx_count", closed.TxCount(),
			"tx_hashes", canonicalTxHashes,
		)
	}
	return closedSeq, nil
}

type consensusAcceptanceTimings struct {
	sequence                                                                    uint32
	lifecycleWait, gateWait, gateHold, serviceWait, historyWait                 time.Duration
	parse, build, apply, stateTxHash, stage, nextOpen, persist, callback, relay time.Duration
}

func (t consensusAcceptanceTimings) log(s *Service, total time.Duration) {
	if t.sequence == 0 {
		return
	}
	s.logger.Info("Consensus acceptance timings", "seq", t.sequence,
		"total", total, "lifecycle_wait", t.lifecycleWait, "open_ledger_wait", t.gateWait, "open_ledger_hold", t.gateHold,
		"service_wait", t.serviceWait, "history_wait", t.historyWait,
		"parse", t.parse, "build", t.build, "transaction_apply", t.apply, "close_state_tx_hash", t.stateTxHash,
		"stage_results", t.stage, "next_open", t.nextOpen, "persistence_enqueue", t.persist, "callback", t.callback, "relay_callback", t.relay)
}

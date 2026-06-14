package adaptor

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// activeReplayTask bundles the in-flight LedgerReplayTask with the
// router-side state used to route inbound responses and drive chain-
// order adoption. Held under r.replayTaskMu.
type activeReplayTask struct {
	task *inbound.LedgerReplayTask

	// chainHashes is the set of hashes the task owns, including the
	// tip. Used by handleReplayDeltaResponse to decide between the
	// task's OnDeltaResponse path and the single-ledger Apply+adopt
	// path.
	chainHashes map[[32]byte]bool

	// anchorParent is the local ledger at seq tipSeq-depth (parent of
	// the oldest chain entry's Apply call).
	anchorParent *ledger.Ledger

	// pendingByHash holds verified-but-not-yet-applied ReplayDeltas.
	// Drained in chain order once each predecessor adopts.
	pendingByHash map[[32]byte]*inbound.ReplayDelta

	// adopted tracks ledger hashes whose Apply+adopt completed, so the
	// parent for the next pending entry is a map lookup rather than a
	// service round-trip.
	adopted map[[32]byte]*ledger.Ledger

	// nextSeqToAdopt is the sequence of the next chain entry to
	// attempt. Initialized to tipSeq-depth+1; monotonically increases.
	nextSeqToAdopt uint32

	// Inverse lookups built once when the task transitions out of
	// WantSkipList, so drainPending can pluck the next-by-seq without
	// scanning.
	chainSeqByHash map[[32]byte]uint32
	chainHashBySeq map[uint32][32]byte
}

// StartReplayTask arms a LedgerReplayTask for a multi-ledger backward
// walk from `tipHash` (at `tipSeq`, with `stateHash` =
// tip.AccountHash) back `depth` ledgers, anchored on the local
// `anchorParent` at seq tipSeq-depth. `peers` is the rotation set the
// task round-robins through when the per-peer Replayer cap is
// reached.
//
// STATUS: this multi-ledger replay task has no production driver yet —
// StartReplayTask and HasActiveReplayTask are exercised only by tests, so
// routeDeltaToActiveTask never matches in production and
// handleProofPathResponse always falls through to the single-ledger path.
// The intended wiring is checkBehind's deep-gap branch (the range-walk
// policy noted in startLedgerAcquisition); until that lands, deep catch-up
// runs one ledger at a time via startLedgerAcquisition.
func (r *Router) StartReplayTask(
	tipHash, stateHash [32]byte,
	tipSeq, depth uint32,
	anchorParent *ledger.Ledger,
	peers []uint64,
) error {
	if anchorParent == nil {
		return errors.New("StartReplayTask: anchorParent must be non-nil")
	}
	if anchorParent.Sequence() != tipSeq-depth {
		return fmt.Errorf("StartReplayTask: anchorParent.Sequence()=%d, want tipSeq-depth=%d",
			anchorParent.Sequence(), tipSeq-depth)
	}

	r.replayTaskMu.Lock()
	if r.activeTask != nil {
		r.replayTaskMu.Unlock()
		return errors.New("StartReplayTask: a task is already in flight")
	}

	state := &activeReplayTask{
		chainHashes:    make(map[[32]byte]bool),
		anchorParent:   anchorParent,
		pendingByHash:  make(map[[32]byte]*inbound.ReplayDelta),
		adopted:        make(map[[32]byte]*ledger.Ledger),
		nextSeqToAdopt: tipSeq - depth + 1,
		chainSeqByHash: make(map[[32]byte]uint32),
		chainHashBySeq: make(map[uint32][32]byte),
	}
	// Seed the anchor into adopted so the oldest chain entry can find
	// its parent via the same map lookup as later entries.
	state.adopted[anchorParent.Hash()] = anchorParent

	cb := inbound.TaskCallbacks{
		OnDeltaVerified: func(seq uint32, h [32]byte, rd *inbound.ReplayDelta) {
			r.onTaskDeltaVerified(seq, h, rd)
		},
		OnComplete: func() {
			r.onTaskComplete()
		},
	}

	task, err := inbound.NewLedgerReplayTask(
		tipHash, stateHash, tipSeq, depth,
		peers,
		r.replayer,
		taskSenderAdapter{adaptor: r.adaptor},
		r.logger,
		cb,
	)
	if err != nil {
		r.replayTaskMu.Unlock()
		return fmt.Errorf("new replay task: %w", err)
	}
	state.task = task
	r.activeTask = state
	r.replayTaskMu.Unlock()

	if err := task.Start(); err != nil {
		r.replayTaskMu.Lock()
		r.activeTask = nil
		r.replayTaskMu.Unlock()
		return fmt.Errorf("start replay task: %w", err)
	}
	anchorHash := anchorParent.Hash()
	r.logger.Info("replay task armed",
		"tip_seq", tipSeq,
		"tip_hash", fmt.Sprintf("%x", tipHash[:8]),
		"depth", depth,
		"anchor_seq", anchorParent.Sequence(),
		"anchor_hash", fmt.Sprintf("%x", anchorHash[:8]),
	)
	return nil
}

// HasActiveReplayTask reports whether a LedgerReplayTask is currently
// in flight.
func (r *Router) HasActiveReplayTask() bool {
	r.replayTaskMu.Lock()
	defer r.replayTaskMu.Unlock()
	return r.activeTask != nil
}

// AbortActiveReplayTask cancels the in-flight task and clears the
// registry. Safe to call when no task is in flight.
func (r *Router) AbortActiveReplayTask(reason error) {
	r.replayTaskMu.Lock()
	at := r.activeTask
	r.activeTask = nil
	r.replayTaskMu.Unlock()
	if at != nil && at.task != nil {
		at.task.Abort(reason)
	}
}

// handleProofPathResponse decodes a mtPROOF_PATH_RESPONSE, routes it
// to the active task's OnSkipListResponse, then populates the chain-
// hash lookup tables so subsequent replay-delta responses are routed
// correctly.
func (r *Router) handleProofPathResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProofPathResponse, msg.Payload)
	if err != nil {
		r.logger.Debug("failed to decode proof path response", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "proof-path-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ProofPathResponse)
	if !ok || resp == nil {
		return
	}

	r.replayTaskMu.Lock()
	at := r.activeTask
	r.replayTaskMu.Unlock()
	if at == nil {
		// No task armed — stale or unsolicited. Drop silently.
		r.logger.Debug("proof path response with no active task",
			"peer", msg.PeerID)
		return
	}

	if err := at.task.OnSkipListResponse(resp); err != nil {
		r.logger.Warn("proof path verification failed; aborting replay task",
			"error", err,
			"peer", msg.PeerID,
		)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "proof-path-verify")
		r.replayTaskMu.Lock()
		r.activeTask = nil
		r.replayTaskMu.Unlock()
		return
	}

	// The task just built its chain inside OnSkipListResponse, so
	// ChainEntries is authoritative now. Populate the lookup so
	// handleReplayDeltaResponse can route subsequent inbound deltas.
	r.replayTaskMu.Lock()
	if r.activeTask == at {
		seqs, hashes := at.task.ChainEntries()
		for i, h := range hashes {
			at.chainHashes[h] = true
			at.chainSeqByHash[h] = seqs[i]
			at.chainHashBySeq[seqs[i]] = h
		}
	}
	r.replayTaskMu.Unlock()
}

// routeDeltaToActiveTask routes a TMReplayDeltaResponse to the
// in-flight LedgerReplayTask if the hash is task-owned. Returns true
// iff the task handled it; on true, the caller MUST skip the legacy
// single-ledger Apply+adopt path.
func (r *Router) routeDeltaToActiveTask(resp *message.ReplayDeltaResponse) (handled bool) {
	hash, ok := inbound.ToHash32(resp.LedgerHash)
	if !ok {
		return false
	}
	r.replayTaskMu.Lock()
	at := r.activeTask
	owned := at != nil && at.chainHashes[hash]
	r.replayTaskMu.Unlock()
	if !owned {
		return false
	}

	err := at.task.OnDeltaResponse(resp)
	if err != nil {
		r.logger.Warn("replay task: delta verification failed; aborting task",
			"error", err,
		)
		r.replayTaskMu.Lock()
		r.activeTask = nil
		r.replayTaskMu.Unlock()
	}
	return true
}

// onTaskDeltaVerified is the task's OnDeltaVerified callback. Stashes
// the verified ReplayDelta and drains the chain in oldest-first
// order, applying each delta whose parent has already adopted.
//
// Runs WITHOUT the task lock (the task fires callbacks unlocked) but
// must take r.replayTaskMu to read activeTask.
func (r *Router) onTaskDeltaVerified(seq uint32, h [32]byte, rd *inbound.ReplayDelta) {
	r.replayTaskMu.Lock()
	at := r.activeTask
	if at == nil {
		r.replayTaskMu.Unlock()
		return
	}
	at.pendingByHash[h] = rd
	at.chainSeqByHash[h] = seq
	at.chainHashBySeq[seq] = h
	at.chainHashes[h] = true
	r.replayTaskMu.Unlock()

	r.drainTaskChain()
}

// drainTaskChain walks the pending verified deltas in chain order
// (next expected seq first) and applies+adopts each one whose parent
// has already been adopted. Stops at the first gap. Re-entrant via
// the task's OnDeltaVerified callbacks, but the lock + nextSeqToAdopt
// monotonic increment guarantee at most one drain runs at a time.
func (r *Router) drainTaskChain() {
	for {
		r.replayTaskMu.Lock()
		at := r.activeTask
		if at == nil {
			r.replayTaskMu.Unlock()
			return
		}
		nextSeq := at.nextSeqToAdopt
		nextHash, haveHash := at.chainHashBySeq[nextSeq]
		if !haveHash {
			r.replayTaskMu.Unlock()
			return
		}
		rd, havePending := at.pendingByHash[nextHash]
		if !havePending {
			r.replayTaskMu.Unlock()
			return
		}
		verifiedLedger, resErr := rd.Result()
		if resErr != nil || verifiedLedger == nil {
			r.replayTaskMu.Unlock()
			r.logger.Warn("replay task: Result() failed unexpectedly",
				"seq", nextSeq, "err", resErr)
			return
		}
		parentHash := verifiedLedger.Header().ParentHash
		parent, haveParent := at.adopted[parentHash]
		if !haveParent {
			// Predecessor not adopted yet — wait.
			r.replayTaskMu.Unlock()
			return
		}
		delete(at.pendingByHash, nextHash)
		at.nextSeqToAdopt++
		r.replayTaskMu.Unlock()
		anchorParent := parent

		if err := rd.SetParent(anchorParent); err != nil {
			r.logger.Error("replay task: SetParent refused",
				"seq", nextSeq, "err", err)
			r.AbortActiveReplayTask(err)
			return
		}
		engineCfg := r.adaptor.EngineConfigForReplay(anchorParent)
		derived, err := rd.Apply(engineCfg)
		if err != nil {
			r.logger.Error("replay task: Apply failed; aborting",
				"seq", nextSeq,
				"hash", fmt.Sprintf("%x", nextHash[:8]),
				"err", err,
			)
			r.AbortActiveReplayTask(err)
			return
		}
		if adoptErr := r.adoptVerifiedLedger(derived, rd.PeerID()); adoptErr != nil {
			r.logger.Warn("replay task: adoptVerifiedLedger failed",
				"seq", nextSeq, "err", adoptErr)
			r.AbortActiveReplayTask(adoptErr)
			return
		}

		r.replayTaskMu.Lock()
		if r.activeTask == at {
			at.adopted[derived.Hash()] = derived
		}
		r.replayTaskMu.Unlock()
	}
}

// onTaskComplete is the task's OnComplete callback. Clears the
// registry once the final drain has finished. May fire before the
// last drainTaskChain iteration completes — drainTaskChain then sees
// a cleared activeTask and exits cleanly.
func (r *Router) onTaskComplete() {
	r.replayTaskMu.Lock()
	at := r.activeTask
	r.activeTask = nil
	r.replayTaskMu.Unlock()
	if at != nil {
		r.logger.Info("replay task complete",
			"tip_seq", at.nextSeqToAdopt-1,
			"adopted", len(at.adopted)-1, // subtract the anchor we pre-seeded
		)
	}
}

// taskSenderAdapter satisfies inbound.TaskSender by delegating to the
// Adaptor's NetworkSender methods.
type taskSenderAdapter struct {
	adaptor *Adaptor
}

func (s taskSenderAdapter) RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mt message.LedgerMapType) error {
	return s.adaptor.RequestProofPath(peerID, ledgerHash, key, mt)
}

func (s taskSenderAdapter) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	return s.adaptor.RequestReplayDelta(peerID, hash)
}

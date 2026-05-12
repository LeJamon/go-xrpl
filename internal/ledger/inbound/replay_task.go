package inbound

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// TaskState tracks LedgerReplayTask progress. Mirrors rippled's
// LedgerReplayTask state model (WantSkipList → RunningDeltas →
// Complete/Failed) without re-using the package-wide State enum,
// which is for single-ledger acquisitions and does not have a
// "running fan-out" notion.
type TaskState int

const (
	// TaskStateWantSkipList: we've sent a TMProofPathRequest for the
	// tip's LedgerHashes entry and are waiting for the proof.
	TaskStateWantSkipList TaskState = iota
	// TaskStateRunningDeltas: skip-list verified, we're fanning out
	// per-ancestor TMReplayDeltaRequest acquisitions bounded by the
	// Replayer's global / per-peer caps.
	TaskStateRunningDeltas
	// TaskStateComplete: every ancestor in the requested depth window
	// has had its framing verified. Caller orchestrates downstream
	// Apply against the local anchor.
	TaskStateComplete
	// TaskStateFailed: the skip-list proof failed or no peer could
	// serve a needed delta. Caller falls back to the legacy single-
	// ledger forward acquisition path.
	TaskStateFailed
)

// MaxBackwardDepth caps how many ledgers a single LedgerReplayTask can
// walk in one shot. Matches rippled's MAX_TASK_SIZE=256
// (rippled/src/xrpld/app/ledger/LedgerReplayer.h:31): the rolling-256
// skip-list of the tip ledger contains at most 256 ancestor hashes,
// and replaying further back requires another task on a deeper tip.
const MaxBackwardDepth = 256

// TaskSender is the minimal wire-issuing interface the task needs.
// Production wires this to the OverlaySender; tests fabricate a fake.
type TaskSender interface {
	// RequestProofPath issues a TMProofPathRequest for (ledgerHash, key,
	// mapType) to peerID. Mirrors OverlaySender.RequestProofPath.
	RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mapType message.LedgerMapType) error
	// RequestReplayDelta issues a TMReplayDeltaRequest for ledger hash
	// to peerID. Mirrors OverlaySender.RequestReplayDelta.
	RequestReplayDelta(peerID uint64, hash [32]byte) error
}

// Sentinel errors that the LedgerReplayTask returns. Wrapped with
// fmt.Errorf so callers can errors.Is them without string matching.
var (
	// ErrTaskBadDepth signals depth is 0 or above MaxBackwardDepth.
	ErrTaskBadDepth = errors.New("replay task: depth out of range")

	// ErrTaskNoPeers signals the task was started with an empty peer
	// list, so no skip-list request can be issued. Caller chooses a
	// peer before starting.
	ErrTaskNoPeers = errors.New("replay task: no peers available")

	// ErrTaskSkipListTooShort signals the verified skip-list contains
	// fewer hashes than the requested depth-1. Either the tip is too
	// young (early in chain history) or the peer served a truncated
	// proof; caller falls back to single-ledger acquisition.
	ErrTaskSkipListTooShort = errors.New("replay task: skip-list too short for depth")

	// ErrTaskWrongState signals an operation was called from an
	// invalid state (e.g., OnDelta before OnSkipList). Indicates a
	// logic bug in the caller's wiring, not a peer or wire issue.
	ErrTaskWrongState = errors.New("replay task: invalid state for operation")
)

// subtask tracks a single ancestor's acquisition status inside the
// task. The chain slice is laid out oldest-first: chain[0] is the
// oldest ledger we want (seq = tipSeq - depth + 1), chain[depth-1] is
// the tip itself.
type subtask struct {
	hash     [32]byte
	seq      uint32
	acquired bool       // RequestReplayDelta has been sent
	verified bool       // framing-verified by the underlying ReplayDelta
	delta    *ReplayDelta
}

// LedgerReplayTask coordinates a multi-ledger backward catch-up. Given
// a known tip and the local anchor's depth-below-tip, it:
//
//  1. Acquires the tip's rolling-256 LedgerHashes SLE via
//     SkipListAcquire (one TMProofPathRequest).
//  2. Extracts the (depth-1) ancestor hashes plus the tip's hash and
//     enumerates them in chain order.
//  3. Fans out per-ancestor TMReplayDeltaRequest acquisitions
//     concurrently, bounded by Replayer's caps. As each completes its
//     framing verification, the task fires the supplied callback so
//     the caller can stitch the chain together via Apply.
//
// Mirrors rippled's LedgerReplayTask
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayTask.cpp:135-209)
// with two intentional simplifications:
//   - The task does NOT run engine Apply itself; the caller does.
//     Rippled keeps Apply in `LedgerReplayMsgHandler::processReplayDeltaResponse`
//     which fires from the receive loop; goXRPL's router does the
//     same thing today via Replayer.HandleResponse + Apply, so this
//     task plugs into the existing seam.
//   - The task does NOT split into multiple peers automatically; the
//     caller supplies an ordered peer list and the task rotates
//     through it when the Replayer rejects an Acquire with
//     ErrPerPeerCapacityFull. (Future work: integrate peer-rotation
//     on sub-task timeout, mirroring ReplayDelta's NoteSubTaskRetry.)
type LedgerReplayTask struct {
	tipHash   [32]byte
	tipSeq    uint32
	stateHash [32]byte // tip.AccountHash, used by SkipListAcquire
	depth     uint32

	peers   []uint64 // rotated through when per-peer cap is hit
	cursor  int      // next peer index to try

	replayer *Replayer
	sender   TaskSender
	logger   *slog.Logger

	// OnDeltaVerified fires every time a chain ancestor's framing
	// verification succeeds. The caller can adopt incrementally (in
	// chain order) or batch until OnComplete fires. The callback is
	// invoked WITHOUT the task lock held so callers may re-enter the
	// task (e.g., Abort) from inside it.
	onDeltaVerified func(seq uint32, hash [32]byte, rd *ReplayDelta)

	// OnComplete fires once after every chain entry has verified.
	onComplete func()

	mu        sync.Mutex
	state     TaskState
	err       error
	skipList  *SkipListAcquire
	chain     []subtask
	completed int
}

// TaskCallbacks bundles the optional progress callbacks for a task.
// Both are invoked without the task lock held.
type TaskCallbacks struct {
	OnDeltaVerified func(seq uint32, hash [32]byte, rd *ReplayDelta)
	OnComplete      func()
}

// NewLedgerReplayTask constructs a task targeting tipHash (with the
// tip's known AccountHash) and a depth of `depth` ledgers backward.
// The local anchor must sit at sequence tipSeq-depth — that anchor is
// the caller's concern; the task only orchestrates wire fetches.
//
// Returns ErrTaskBadDepth if depth is 0 or > MaxBackwardDepth, and
// ErrTaskNoPeers if peers is empty.
func NewLedgerReplayTask(
	tipHash, stateHash [32]byte,
	tipSeq, depth uint32,
	peers []uint64,
	replayer *Replayer,
	sender TaskSender,
	logger *slog.Logger,
	cb TaskCallbacks,
) (*LedgerReplayTask, error) {
	if depth == 0 || depth > MaxBackwardDepth {
		return nil, fmt.Errorf("%w: depth=%d", ErrTaskBadDepth, depth)
	}
	if len(peers) == 0 {
		return nil, ErrTaskNoPeers
	}
	if logger == nil {
		logger = slog.Default()
	}
	peersCopy := append([]uint64(nil), peers...)
	return &LedgerReplayTask{
		tipHash:         tipHash,
		tipSeq:          tipSeq,
		stateHash:       stateHash,
		depth:           depth,
		peers:           peersCopy,
		replayer:        replayer,
		sender:          sender,
		logger:          logger,
		state:           TaskStateWantSkipList,
		onDeltaVerified: cb.OnDeltaVerified,
		onComplete:      cb.OnComplete,
	}, nil
}

// State returns the current task state.
func (t *LedgerReplayTask) State() TaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Err returns the failure error, or nil if not in TaskStateFailed.
func (t *LedgerReplayTask) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// IsComplete reports whether every chain entry has verified.
func (t *LedgerReplayTask) IsComplete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state == TaskStateComplete
}

// ChainSeqs returns the ordered sequence numbers the task is
// fetching: chain[0]=tipSeq-depth+1 ... chain[depth-1]=tipSeq.
// Returned slice is a defensive copy.
func (t *LedgerReplayTask) ChainSeqs() []uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]uint32, len(t.chain))
	for i, st := range t.chain {
		out[i] = st.seq
	}
	return out
}

// ChainEntries returns the (seq, hash) pairs of every chain entry,
// oldest-first. Empty until the skip-list response has been
// verified. Returned slices are defensive copies.
//
// Exposed so the router can pre-register hashes in its task-routing
// map the moment the chain is known, rather than relying on lazy
// registration during each subtask's OnDeltaVerified callback.
func (t *LedgerReplayTask) ChainEntries() (seqs []uint32, hashes [][32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seqs = make([]uint32, len(t.chain))
	hashes = make([][32]byte, len(t.chain))
	for i, st := range t.chain {
		seqs[i] = st.seq
		hashes[i] = st.hash
	}
	return
}

// Start arms the skip-list acquisition and issues the wire request to
// the first peer in the rotation. Subsequent OnSkipListResponse drives
// the rest of the task.
func (t *LedgerReplayTask) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state != TaskStateWantSkipList {
		return fmt.Errorf("%w: Start called from state %d", ErrTaskWrongState, t.state)
	}

	peer := t.peers[t.cursor]
	sl, err := t.replayer.AcquireSkipList(t.tipHash, t.stateHash, peer)
	if err != nil {
		t.state = TaskStateFailed
		t.err = fmt.Errorf("acquire skip-list: %w", err)
		return t.err
	}
	t.skipList = sl

	skipKL := keylet.LedgerHashes()
	if wireErr := t.sender.RequestProofPath(peer, t.tipHash, skipKL.Key, message.LedgerMapAccountState); wireErr != nil {
		t.replayer.AbandonSkipList(t.tipHash)
		t.skipList = nil
		t.state = TaskStateFailed
		t.err = fmt.Errorf("send proof-path request: %w", wireErr)
		return t.err
	}

	t.logger.Info("replay task started",
		"tip", hex.EncodeToString(t.tipHash[:8]),
		"tipSeq", t.tipSeq,
		"depth", t.depth,
		"peer", peer,
	)
	return nil
}

// OnSkipListResponse handles a TMProofPathResponse routed to the task
// (matched on resp.LedgerHash == tipHash). Verifies the proof,
// populates the chain, and kicks off the delta fan-out.
//
// Returns nil on success, or a wrapped error on proof failure. After
// a proof failure the task transitions to TaskStateFailed and the
// caller falls back to single-ledger acquisition.
func (t *LedgerReplayTask) OnSkipListResponse(resp *message.ProofPathResponse) error {
	t.mu.Lock()

	if t.state != TaskStateWantSkipList {
		t.mu.Unlock()
		return fmt.Errorf("%w: OnSkipListResponse from state %d", ErrTaskWrongState, t.state)
	}
	if t.skipList == nil {
		t.mu.Unlock()
		return fmt.Errorf("%w: skip-list acquire missing", ErrTaskWrongState)
	}

	if err := t.skipList.GotResponse(resp); err != nil {
		t.replayer.AbandonSkipList(t.tipHash)
		t.state = TaskStateFailed
		t.err = fmt.Errorf("verify skip-list proof: %w", err)
		t.mu.Unlock()
		return t.err
	}

	hashes := t.skipList.Hashes()
	// Need (depth-1) ancestor hashes (positions tipSeq-1 .. tipSeq-depth+1).
	// hashes[len-1] = parent(tip) = seq tipSeq-1, hashes[len-k] = seq tipSeq-k.
	needAncestors := int(t.depth) - 1
	if needAncestors > len(hashes) {
		t.replayer.AbandonSkipList(t.tipHash)
		t.state = TaskStateFailed
		t.err = fmt.Errorf("%w: have %d hashes, need %d",
			ErrTaskSkipListTooShort, len(hashes), needAncestors)
		t.mu.Unlock()
		return t.err
	}

	// Build chain oldest-first. The oldest needed seq is tipSeq-depth+1
	// (assuming the local anchor sits at tipSeq-depth). For depth=50
	// and tipSeq=N: chain[0]=N-49, chain[49]=N (tip).
	chain := make([]subtask, 0, t.depth)
	for k := needAncestors; k >= 1; k-- {
		chain = append(chain, subtask{
			hash: hashes[len(hashes)-k],
			seq:  t.tipSeq - uint32(k),
		})
	}
	chain = append(chain, subtask{hash: t.tipHash, seq: t.tipSeq})
	t.chain = chain
	t.state = TaskStateRunningDeltas

	// Skip-list is no longer needed; free the coordinator slot.
	t.replayer.CompleteSkipList(t.tipHash)
	t.skipList = nil

	t.logger.Info("replay task skip-list verified",
		"tip", hex.EncodeToString(t.tipHash[:8]),
		"chain_len", len(chain),
		"chain_lo", chain[0].seq,
		"chain_hi", chain[len(chain)-1].seq,
	)
	t.mu.Unlock()

	// Kick off the initial fan-out without holding the lock — the
	// sender may invoke recursive callbacks in tests, and we want to
	// allow that without deadlocking.
	t.dispatchPending()
	return nil
}

// OnDeltaResponse handles a TMReplayDeltaResponse routed via the
// Replayer to one of the task's in-flight subtasks. Marks the matching
// chain entry verified, frees its Replayer slot, fires the
// OnDeltaVerified callback, and dispatches the next queued chain
// entry. When the last entry verifies, transitions the task to
// TaskStateComplete and fires OnComplete.
//
// Returns the underlying ReplayDelta's verification error (if any) so
// the caller can charge the offending peer. The task does NOT abort
// on a single peer's bad-data — that subtask is abandoned and the
// chain entry stays pending for the next peer rotation. (For this
// initial implementation we abort the task on any subtask failure;
// the multi-peer retry path is a follow-up.)
func (t *LedgerReplayTask) OnDeltaResponse(resp *message.ReplayDeltaResponse) error {
	t.mu.Lock()

	if t.state != TaskStateRunningDeltas {
		t.mu.Unlock()
		return fmt.Errorf("%w: OnDeltaResponse from state %d", ErrTaskWrongState, t.state)
	}

	respHash, ok := toHash32(resp.LedgerHash)
	if !ok {
		t.mu.Unlock()
		return ErrNoMatchingAcquisition
	}

	idx := -1
	for i := range t.chain {
		if t.chain[i].hash == respHash && t.chain[i].acquired && !t.chain[i].verified {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.mu.Unlock()
		return ErrNoMatchingAcquisition
	}

	rd, err := t.replayer.HandleResponse(resp)
	if err != nil {
		t.replayer.Abandon(respHash)
		t.state = TaskStateFailed
		t.err = fmt.Errorf("verify delta seq=%d: %w", t.chain[idx].seq, err)
		t.mu.Unlock()
		return t.err
	}
	t.chain[idx].delta = rd
	t.chain[idx].verified = true
	t.completed++
	t.replayer.Complete(respHash)

	cb := t.onDeltaVerified
	finished := t.completed == len(t.chain)
	verifiedSeq := t.chain[idx].seq
	verifiedHash := t.chain[idx].hash
	onDone := t.onComplete
	if finished {
		t.state = TaskStateComplete
	}
	t.mu.Unlock()

	if cb != nil {
		cb(verifiedSeq, verifiedHash, rd)
	}
	if !finished {
		t.dispatchPending()
	} else if onDone != nil {
		onDone()
	}
	return nil
}

// Abort releases all Replayer slots the task is holding and marks
// it failed with the supplied reason. Idempotent. Safe to call from
// a callback (re-entrancy: only touches t.mu and the replayer).
func (t *LedgerReplayTask) Abort(reason error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == TaskStateComplete || t.state == TaskStateFailed {
		return
	}
	if t.skipList != nil {
		t.replayer.AbandonSkipList(t.tipHash)
		t.skipList = nil
	}
	for i := range t.chain {
		if t.chain[i].acquired && !t.chain[i].verified {
			t.replayer.Abandon(t.chain[i].hash)
		}
	}
	t.state = TaskStateFailed
	t.err = reason
}

// dispatchPending fans out un-acquired chain entries through the
// Replayer until the global / per-peer caps refuse, or the chain
// drains. Called from OnSkipListResponse (initial fan-out) and after
// each OnDeltaResponse (refill on completion). Caller must NOT hold
// t.mu.
func (t *LedgerReplayTask) dispatchPending() {
	for {
		t.mu.Lock()
		if t.state != TaskStateRunningDeltas {
			t.mu.Unlock()
			return
		}

		// Find next un-acquired chain entry in oldest-first order.
		idx := -1
		for i := range t.chain {
			if !t.chain[i].acquired {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.mu.Unlock()
			return
		}

		// Pick the next peer in rotation. Iterate until we either
		// successfully acquire or every peer has refused.
		entry := t.chain[idx]
		acquired := false
		attempts := 0
		for attempts < len(t.peers) {
			peer := t.peers[t.cursor]
			t.cursor = (t.cursor + 1) % len(t.peers)
			attempts++

			_, err := t.replayer.Acquire(entry.hash, peer, nil)
			if err == nil {
				if wireErr := t.sender.RequestReplayDelta(peer, entry.hash); wireErr != nil {
					// Wire send failed — release the slot and try the
					// next peer. This is rare (transport-level error).
					t.replayer.Abandon(entry.hash)
					continue
				}
				t.chain[idx].acquired = true
				acquired = true
				break
			}
			// Acquire refused. The interesting cases are:
			//   - ErrPerPeerCapacityFull: try the next peer.
			//   - ErrCapacityFull: global cap reached — stop fanning out,
			//     wait for a completion to free a slot.
			//   - ErrAcquisitionExists: another task is acquiring the
			//     same hash (rare). Treat as success — when its response
			//     arrives, our HandleResponse-style lookup against
			//     resp.LedgerHash still routes it correctly because the
			//     Replayer.inFlight map is global. But the framing-
			//     verified callback won't fire on us. For now, surface
			//     this as a non-fatal "skip" — we'd need cross-task
			//     subscription to handle properly. We mark the entry
			//     acquired and rely on the eventual response routing to
			//     us via OnDeltaResponse. If the other task abandons
			//     before completing, we time out via the outer budget.
			if errors.Is(err, ErrCapacityFull) {
				// Wait for a completion. Don't keep trying more peers.
				t.mu.Unlock()
				return
			}
			if errors.Is(err, ErrAcquisitionExists) {
				t.chain[idx].acquired = true
				acquired = true
				break
			}
			// ErrPerPeerCapacityFull or other — try next peer.
		}

		if !acquired {
			// Every peer refused. Wait for a completion to retry.
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

// InFlightCount returns the number of chain entries currently
// acquired-but-not-yet-verified. Exposed for tests and metrics.
func (t *LedgerReplayTask) InFlightCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, st := range t.chain {
		if st.acquired && !st.verified {
			n++
		}
	}
	return n
}

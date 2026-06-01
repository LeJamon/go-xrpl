package inbound

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TaskState tracks LedgerReplayTask progress. Distinct from the
// package-wide State enum, which has no "running fan-out" notion.
// Mirrors rippled's LedgerReplayTask state model (WantSkipList →
// RunningDeltas → Complete/Failed).
type TaskState int

const (
	// TaskStateWantSkipList: TMProofPathRequest sent, awaiting proof.
	TaskStateWantSkipList TaskState = iota
	// TaskStateRunningDeltas: skip-list verified, fanning out
	// TMReplayDeltaRequest acquisitions under the Replayer's caps.
	TaskStateRunningDeltas
	// TaskStateComplete: every chain entry's framing has verified.
	TaskStateComplete
	// TaskStateFailed: skip-list proof rejected, or a delta failed
	// verification, or no peer could serve a needed delta.
	TaskStateFailed
)

// MaxBackwardDepth caps how many ledgers a single LedgerReplayTask can
// walk in one shot. Matches rippled's MAX_TASK_SIZE=256
// (rippled/src/xrpld/app/ledger/LedgerReplayer.h:31): the rolling-256
// skip-list of the tip ledger contains at most 256 ancestor hashes,
// and replaying further back requires another task on a deeper tip.
const MaxBackwardDepth = 256

// TaskSender is the wire-issuing interface the task needs. Mirrors
// the relevant OverlaySender methods so tests can fabricate a fake.
type TaskSender interface {
	RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mapType message.LedgerMapType) error
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
	acquired bool // RequestReplayDelta has been sent
	verified bool // framing-verified by the underlying ReplayDelta
	delta    *ReplayDelta
	// tried records peers we've already asked for this hash. On
	// verification failure we re-dispatch to a peer not in this set
	// rather than aborting the whole task. Mirrors rippled's per-
	// subtask peer rotation (LedgerReplayer.h:49-57).
	tried map[uint64]bool
}

// LedgerReplayTask coordinates a multi-ledger backward catch-up:
//
//  1. Acquire the tip's rolling-256 LedgerHashes SLE via
//     SkipListAcquire (one TMProofPathRequest).
//  2. Enumerate (depth-1) ancestor hashes + the tip in chain order.
//  3. Fan out per-ancestor TMReplayDeltaRequest acquisitions under
//     Replayer's caps, firing the supplied callback as each entry's
//     framing verifies so the caller can stitch the chain via Apply.
//
// Mirrors rippled's LedgerReplayTask
// (rippled/src/xrpld/app/ledger/detail/LedgerReplayTask.cpp:135-209)
// with two simplifications: the task does NOT run engine Apply (the
// caller does), and peer rotation happens only on Acquire refusal,
// not on response-level failure.
type LedgerReplayTask struct {
	tipHash   [32]byte
	tipSeq    uint32
	stateHash [32]byte // tip.AccountHash, used by SkipListAcquire
	depth     uint32

	peers  []uint64 // rotated through when per-peer cap is hit
	cursor int      // next peer index to try

	replayer *Replayer
	sender   TaskSender
	logger   *slog.Logger

	// Callbacks fire WITHOUT the task lock held so callers may
	// re-enter the task (e.g., Abort) from inside one.
	onDeltaVerified func(seq uint32, hash [32]byte, rd *ReplayDelta)
	onComplete      func()

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

// NewLedgerReplayTask constructs a task targeting tipHash (with
// AccountHash=stateHash) walking back `depth` ledgers. The caller's
// local anchor must sit at sequence tipSeq-depth.
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

func (t *LedgerReplayTask) State() TaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *LedgerReplayTask) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *LedgerReplayTask) IsComplete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state == TaskStateComplete
}

// ChainSeqs returns a defensive copy of the ordered sequence numbers
// the task is fetching: chain[0]=tipSeq-depth+1 ... chain[depth-1]=tipSeq.
func (t *LedgerReplayTask) ChainSeqs() []uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]uint32, len(t.chain))
	for i, st := range t.chain {
		out[i] = st.seq
	}
	return out
}

// ChainEntries returns defensive copies of every chain entry's
// (seq, hash), oldest-first. Empty until the skip-list verifies.
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

// Start arms the skip-list acquisition and issues the proof-path
// request to the first peer in the rotation.
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

// OnSkipListResponse verifies a TMProofPathResponse against the
// stored stateHash, populates the chain, and kicks off the delta
// fan-out. On proof failure the task transitions to TaskStateFailed.
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

	chain := make([]subtask, 0, t.depth)
	for k := needAncestors; k >= 1; k-- {
		chain = append(chain, subtask{
			hash:  hashes[len(hashes)-k],
			seq:   t.tipSeq - uint32(k),
			tried: make(map[uint64]bool),
		})
	}
	chain = append(chain, subtask{
		hash:  t.tipHash,
		seq:   t.tipSeq,
		tried: make(map[uint64]bool),
	})
	t.chain = chain
	t.state = TaskStateRunningDeltas

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

// OnDeltaResponse handles a TMReplayDeltaResponse for one of the
// task's in-flight subtasks: marks the chain entry verified, fires
// OnDeltaVerified, and refills the fan-out. On verification failure
// the entire task aborts; per-peer retry is a follow-up.
func (t *LedgerReplayTask) OnDeltaResponse(resp *message.ReplayDeltaResponse) error {
	t.mu.Lock()

	if t.state != TaskStateRunningDeltas {
		t.mu.Unlock()
		return fmt.Errorf("%w: OnDeltaResponse from state %d", ErrTaskWrongState, t.state)
	}

	respHash, ok := ToHash32(resp.LedgerHash)
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
		// Verification failed against this peer. Drop the
		// acquisition and mark the subtask as not-acquired so
		// dispatchPending re-issues to a fresh peer. The whole task
		// only fails if every peer has been tried for this hash.
		t.replayer.Abandon(respHash)
		t.chain[idx].acquired = false
		t.chain[idx].delta = nil
		untried := 0
		for _, p := range t.peers {
			if !t.chain[idx].tried[p] {
				untried++
			}
		}
		if untried == 0 {
			t.state = TaskStateFailed
			t.err = fmt.Errorf("verify delta seq=%d (all peers exhausted): %w",
				t.chain[idx].seq, err)
			t.mu.Unlock()
			return t.err
		}
		retryErr := fmt.Errorf("verify delta seq=%d: %w", t.chain[idx].seq, err)
		t.mu.Unlock()
		t.dispatchPending()
		return retryErr
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

// Abort releases all Replayer slots the task holds and marks it
// failed with the supplied reason. Idempotent and re-entrant from a
// callback.
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
// Replayer until the caps refuse or the chain drains. Caller must
// NOT hold t.mu.
func (t *LedgerReplayTask) dispatchPending() {
	for {
		t.mu.Lock()
		if t.state != TaskStateRunningDeltas {
			t.mu.Unlock()
			return
		}

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

		entry := t.chain[idx]
		acquired := false
		attempts := 0
		for attempts < len(t.peers) {
			peer := t.peers[t.cursor]
			t.cursor = (t.cursor + 1) % len(t.peers)
			attempts++

			// Skip peers that already returned bad data for this hash.
			if entry.tried[peer] {
				continue
			}

			_, err := t.replayer.Acquire(entry.hash, peer, nil)
			if err == nil {
				if wireErr := t.sender.RequestReplayDelta(peer, entry.hash); wireErr != nil {
					t.replayer.Abandon(entry.hash)
					continue
				}
				t.chain[idx].acquired = true
				t.chain[idx].tried[peer] = true
				acquired = true
				break
			}
			if errors.Is(err, ErrCapacityFull) {
				// Global cap reached — wait for a completion before
				// trying more peers.
				t.mu.Unlock()
				return
			}
			if errors.Is(err, ErrAcquisitionExists) {
				// Another task is fetching the same hash; trust the
				// global inFlight map to route the response and mark
				// acquired so we don't redundantly re-issue.
				t.chain[idx].acquired = true
				t.chain[idx].tried[peer] = true
				acquired = true
				break
			}
			// ErrPerPeerCapacityFull or other — try the next peer.
		}

		if !acquired {
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

// InFlightCount returns the number of chain entries acquired but not
// yet verified.
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
